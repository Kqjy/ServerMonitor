package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"servermonitor/internal/server/secretbox"
)

const (
	BackupDestinationDirectS3   = "direct_s3"
	DefaultDirectPrefixTemplate = "backups/hosts/{host_id}-{hostname}/{repository}"
)

var (
	ErrBackupDestinationNameTaken = errors.New("backup destination name already in use")
	ErrBackupDestinationInUse     = errors.New("backup destination is still used by repositories")
	ErrBackupCredentialsMissing   = errors.New("direct backup credentials are not configured")
)

type BackupS3Credentials struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	SessionToken    string `json:"session_token,omitempty"`
}

func (c BackupS3Credentials) Empty() bool {
	return c.AccessKeyID == "" && c.SecretAccessKey == "" && c.SessionToken == ""
}

func (c BackupS3Credentials) Validate() error {
	if c.Empty() {
		return nil
	}
	if strings.TrimSpace(c.AccessKeyID) == "" || strings.TrimSpace(c.SecretAccessKey) == "" {
		return errors.New("access_key_id and secret_access_key must be set together")
	}
	for _, value := range []string{c.AccessKeyID, c.SecretAccessKey, c.SessionToken} {
		if strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("credentials must not contain newlines or NUL bytes")
		}
	}
	return nil
}

type BackupDestination struct {
	ID                    int64
	Name                  string
	Kind                  string
	Endpoint              string
	Bucket                string
	Region                string
	PrefixTemplate        string
	UsePathStyle          bool
	CredentialsConfigured bool
	RepositoryCount       int
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type BackupDestinationInput struct {
	Name           string
	Kind           string
	Endpoint       string
	Bucket         string
	Region         string
	PrefixTemplate string
	UsePathStyle   bool
	Credentials    BackupS3Credentials
}

type ManagedBackupRepository struct {
	ID          int64
	Name        string
	URL         string
	S3Region    string
	S3PathStyle bool
	Credentials BackupS3Credentials
	Version     int64
}

func (b *BackupTargets) CreateDestination(ctx context.Context, in BackupDestinationInput) (int64, error) {
	in = normalizeDestinationInput(in)
	if err := validateDestinationInput(in); err != nil {
		return 0, err
	}
	tx, err := b.db.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var id int64
	err = tx.QueryRow(ctx, `
		INSERT INTO backup_destinations (name, kind, endpoint, bucket, region, prefix_template, use_path_style)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id
	`, in.Name, in.Kind, in.Endpoint, in.Bucket, in.Region, in.PrefixTemplate, in.UsePathStyle).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, ErrBackupDestinationNameTaken
		}
		return 0, err
	}
	if !in.Credentials.Empty() {
		sealed, sealErr := b.sealCredentials(in.Credentials, destinationCredentialsAAD(id))
		if sealErr != nil {
			return 0, sealErr
		}
		if _, err = tx.Exec(ctx, `UPDATE backup_destinations SET credentials_ciphertext = $2 WHERE id = $1`, id, sealed); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return id, nil
}

func (b *BackupTargets) ListDestinations(ctx context.Context) ([]BackupDestination, error) {
	rows, err := b.db.Pool.Query(ctx, `
		SELECT d.id, d.name, d.kind, d.endpoint, d.bucket, d.region, d.prefix_template, d.use_path_style,
		       d.credentials_ciphertext IS NOT NULL, COUNT(t.id), d.created_at, d.updated_at
		FROM backup_destinations d
		LEFT JOIN backup_targets t ON t.destination_id = d.id
		GROUP BY d.id
		ORDER BY lower(d.name), d.id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BackupDestination{}
	for rows.Next() {
		var d BackupDestination
		if err := rows.Scan(&d.ID, &d.Name, &d.Kind, &d.Endpoint, &d.Bucket, &d.Region, &d.PrefixTemplate,
			&d.UsePathStyle, &d.CredentialsConfigured, &d.RepositoryCount, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (b *BackupTargets) GetDestination(ctx context.Context, id int64) (BackupDestination, error) {
	var d BackupDestination
	err := b.db.Pool.QueryRow(ctx, `
		SELECT d.id, d.name, d.kind, d.endpoint, d.bucket, d.region, d.prefix_template, d.use_path_style,
		       d.credentials_ciphertext IS NOT NULL, COUNT(t.id), d.created_at, d.updated_at
		FROM backup_destinations d
		LEFT JOIN backup_targets t ON t.destination_id = d.id
		WHERE d.id = $1
		GROUP BY d.id
	`, id).Scan(&d.ID, &d.Name, &d.Kind, &d.Endpoint, &d.Bucket, &d.Region, &d.PrefixTemplate,
		&d.UsePathStyle, &d.CredentialsConfigured, &d.RepositoryCount, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return BackupDestination{}, ErrNotFound
	}
	return d, err
}

func (b *BackupTargets) SetDestinationCredentials(ctx context.Context, id int64, credentials BackupS3Credentials) error {
	if err := credentials.Validate(); err != nil {
		return err
	}
	if credentials.Empty() {
		return ErrBackupCredentialsMissing
	}
	sealed, err := b.sealCredentials(credentials, destinationCredentialsAAD(id))
	if err != nil {
		return err
	}
	tag, err := b.db.Pool.Exec(ctx, `
		UPDATE backup_destinations SET credentials_ciphertext = $2, updated_at = now() WHERE id = $1
	`, id, sealed)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (b *BackupTargets) DeleteDestination(ctx context.Context, id int64) error {
	tag, err := b.db.Pool.Exec(ctx, `DELETE FROM backup_destinations WHERE id = $1`, id)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return ErrBackupDestinationInUse
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (b *BackupTargets) CreateDirectRepository(ctx context.Context, name string, hostID int64, quota *int64, secret, namespacePrefix string, destinationID int64, credentials BackupS3Credentials) (int64, error) {
	if err := credentials.Validate(); err != nil {
		return 0, err
	}
	tx, err := b.db.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var destinationCredentials []byte
	if err := tx.QueryRow(ctx, `SELECT credentials_ciphertext FROM backup_destinations WHERE id = $1`, destinationID).Scan(&destinationCredentials); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	if credentials.Empty() && len(destinationCredentials) == 0 {
		return 0, ErrBackupCredentialsMissing
	}
	var id int64
	err = tx.QueryRow(ctx, `
		INSERT INTO backup_targets (name, secret_hash, host_id, quota_bytes, destination_id, namespace_prefix)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id
	`, name, tokenHash(secret), hostID, quota, destinationID, namespacePrefix).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, ErrBackupTargetNameTaken
		}
		return 0, err
	}
	if !credentials.Empty() {
		sealed, sealErr := b.sealCredentials(credentials, repositoryCredentialsAAD(id))
		if sealErr != nil {
			return 0, sealErr
		}
		if _, err := tx.Exec(ctx, `UPDATE backup_targets SET direct_credentials_ciphertext = $2 WHERE id = $1`, id, sealed); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	b.invalidate()
	return id, nil
}

func (b *BackupTargets) SetDirectRepositoryCredentials(ctx context.Context, id int64, credentials BackupS3Credentials) error {
	if err := credentials.Validate(); err != nil {
		return err
	}
	if credentials.Empty() {
		return ErrBackupCredentialsMissing
	}
	sealed, err := b.sealCredentials(credentials, repositoryCredentialsAAD(id))
	if err != nil {
		return err
	}
	tag, err := b.db.Pool.Exec(ctx, `
		UPDATE backup_targets SET direct_credentials_ciphertext = $2, updated_at = now()
		WHERE id = $1 AND destination_id IS NOT NULL
	`, id, sealed)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (b *BackupTargets) ManagedRepositoriesForHost(ctx context.Context, hostID int64) ([]ManagedBackupRepository, error) {
	rows, err := b.db.Pool.Query(ctx, `
		SELECT t.id, t.name, d.id, d.endpoint, d.bucket, t.namespace_prefix, d.region, d.use_path_style,
		       t.direct_credentials_ciphertext, d.credentials_ciphertext,
		       GREATEST(t.updated_at, d.updated_at)
		FROM backup_targets t
		JOIN backup_destinations d ON d.id = t.destination_id
		WHERE t.host_id = $1 AND t.revoked_at IS NULL AND d.kind = 'direct_s3'
		ORDER BY lower(t.name), t.id
	`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ManagedBackupRepository{}
	for rows.Next() {
		var (
			r                               ManagedBackupRepository
			destinationID                   int64
			endpoint, bucket, prefix        string
			targetCipher, destinationCipher []byte
			updated                         time.Time
		)
		if err := rows.Scan(&r.ID, &r.Name, &destinationID, &endpoint, &bucket, &prefix, &r.S3Region, &r.S3PathStyle,
			&targetCipher, &destinationCipher, &updated); err != nil {
			return nil, err
		}
		ciphertext := targetCipher
		aad := repositoryCredentialsAAD(r.ID)
		if len(ciphertext) == 0 {
			ciphertext = destinationCipher
			aad = destinationCredentialsAAD(destinationID)
		}
		if len(ciphertext) == 0 {
			return nil, fmt.Errorf("repository %d: %w", r.ID, ErrBackupCredentialsMissing)
		}
		credentials, err := b.openCredentials(ciphertext, aad)
		if err != nil {
			return nil, fmt.Errorf("repository %d credentials: %w", r.ID, err)
		}
		r.Credentials = credentials
		r.URL = DirectS3RepositoryURL(endpoint, bucket, prefix)
		r.Version = updated.UnixMicro()
		out = append(out, r)
	}
	return out, rows.Err()
}

func (b *BackupTargets) ValidateStoredCredentials(ctx context.Context) error {
	rows, err := b.db.Pool.Query(ctx, `
		SELECT 'destination', id, credentials_ciphertext
		FROM backup_destinations
		WHERE credentials_ciphertext IS NOT NULL
		UNION ALL
		SELECT 'repository', id, direct_credentials_ciphertext
		FROM backup_targets
		WHERE direct_credentials_ciphertext IS NOT NULL
	`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var id int64
		var ciphertext []byte
		if err := rows.Scan(&kind, &id, &ciphertext); err != nil {
			return err
		}
		aad := destinationCredentialsAAD(id)
		if kind == "repository" {
			aad = repositoryCredentialsAAD(id)
		}
		if _, err := b.openCredentials(ciphertext, aad); err != nil {
			return fmt.Errorf("decrypt stored backup %s credentials for record %d: %w", kind, id, err)
		}
	}
	return rows.Err()
}

func normalizeDestinationInput(in BackupDestinationInput) BackupDestinationInput {
	in.Name = strings.TrimSpace(in.Name)
	in.Kind = strings.TrimSpace(in.Kind)
	if in.Kind == "" {
		in.Kind = BackupDestinationDirectS3
	}
	in.Endpoint = strings.TrimRight(strings.TrimSpace(in.Endpoint), "/")
	in.Bucket = strings.Trim(strings.TrimSpace(in.Bucket), "/")
	in.Region = strings.TrimSpace(in.Region)
	in.PrefixTemplate = strings.Trim(strings.TrimSpace(in.PrefixTemplate), "/")
	if in.PrefixTemplate == "" {
		in.PrefixTemplate = DefaultDirectPrefixTemplate
	}
	in.Credentials.AccessKeyID = strings.TrimSpace(in.Credentials.AccessKeyID)
	in.Credentials.SecretAccessKey = strings.TrimSpace(in.Credentials.SecretAccessKey)
	in.Credentials.SessionToken = strings.TrimSpace(in.Credentials.SessionToken)
	return in
}

func validateDestinationInput(in BackupDestinationInput) error {
	if in.Name == "" {
		return errors.New("destination name is required")
	}
	if len(in.Name) > 128 || strings.ContainsAny(in.Name, "\r\n\x00") {
		return errors.New("destination name must be at most 128 characters and contain no control characters")
	}
	if in.Kind != BackupDestinationDirectS3 {
		return errors.New("destination kind must be direct_s3")
	}
	if in.Bucket == "" {
		return errors.New("bucket is required")
	}
	if !bucketNameRE.MatchString(in.Bucket) {
		return errors.New("bucket may contain only letters, digits, dot, dash, and underscore")
	}
	if in.Region != "" && !regionNameRE.MatchString(in.Region) {
		return errors.New("region may contain only letters, digits, dot, dash, and underscore")
	}
	if err := validateDirectS3Endpoint(in.Endpoint); err != nil {
		return err
	}
	if !strings.Contains(in.PrefixTemplate, "{host_id}") || !strings.Contains(in.PrefixTemplate, "{repository}") {
		return errors.New("prefix_template must contain {host_id} and {repository}")
	}
	if _, err := ExpandDirectPrefix(in.PrefixTemplate, 1, "example-host", "example-repo"); err != nil {
		return fmt.Errorf("prefix_template: %w", err)
	}
	return in.Credentials.Validate()
}

var unsafePrefixSegmentRE = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)
var directPrefixTemplateRE = regexp.MustCompile(`^[a-zA-Z0-9._/{}-]+$`)
var bucketNameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$`)
var regionNameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

func validateDirectS3Endpoint(endpoint string) error {
	if endpoint == "" {
		return nil
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return errors.New("endpoint must be an https:// origin (leave blank for AWS S3)")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return errors.New("endpoint must not contain credentials, a path, query, or fragment")
	}
	return nil
}

func ExpandDirectPrefix(template string, hostID int64, hostname, repository string) (string, error) {
	if hostID <= 0 {
		return "", errors.New("host id must be positive")
	}
	if !directPrefixTemplateRE.MatchString(template) || strings.ContainsAny(template, "\r\n\x00\\") {
		return "", errors.New("template contains unsupported characters")
	}
	unknownCheck := strings.NewReplacer("{host_id}", "", "{hostname}", "", "{repository}", "").Replace(template)
	if strings.ContainsAny(unknownCheck, "{}") {
		return "", errors.New("template contains an unknown placeholder")
	}
	hostname = strings.Trim(unsafePrefixSegmentRE.ReplaceAllString(strings.TrimSpace(hostname), "-"), ".-")
	if hostname == "" {
		hostname = "host"
	}
	repository = strings.Trim(unsafePrefixSegmentRE.ReplaceAllString(strings.TrimSpace(repository), "-"), ".-")
	if repository == "" {
		return "", errors.New("repository name is empty")
	}
	replacer := strings.NewReplacer(
		"{host_id}", strconv.FormatInt(hostID, 10),
		"{hostname}", hostname,
		"{repository}", repository,
	)
	prefix := strings.Trim(replacer.Replace(template), "/")
	if prefix == "" || strings.Contains(prefix, "\\") {
		return "", errors.New("expanded prefix is empty or contains a backslash")
	}
	for _, segment := range strings.Split(prefix, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", errors.New("expanded prefix contains an empty, dot, or parent segment")
		}
	}
	return path.Clean(prefix), nil
}

func DirectS3RepositoryURL(endpoint, bucket, prefix string) string {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if endpoint == "" {
		endpoint = "s3.amazonaws.com"
	}
	return "s3:" + endpoint + "/" + path.Join(strings.Trim(bucket, "/"), strings.Trim(prefix, "/"))
}

func (b *BackupTargets) sealCredentials(credentials BackupS3Credentials, aad []byte) ([]byte, error) {
	if b.secrets == nil {
		return nil, secretbox.ErrNotConfigured
	}
	data, err := json.Marshal(credentials)
	if err != nil {
		return nil, err
	}
	return b.secrets.Seal(data, aad)
}

func (b *BackupTargets) openCredentials(ciphertext, aad []byte) (BackupS3Credentials, error) {
	if b.secrets == nil {
		return BackupS3Credentials{}, secretbox.ErrNotConfigured
	}
	data, err := b.secrets.Open(ciphertext, aad)
	if err != nil {
		return BackupS3Credentials{}, err
	}
	var credentials BackupS3Credentials
	if err := json.Unmarshal(data, &credentials); err != nil {
		return BackupS3Credentials{}, err
	}
	if err := credentials.Validate(); err != nil {
		return BackupS3Credentials{}, err
	}
	return credentials, nil
}

func destinationCredentialsAAD(id int64) []byte {
	return []byte(fmt.Sprintf("servermonitor:backup-destination:%d:v1", id))
}

func repositoryCredentialsAAD(id int64) []byte {
	return []byte(fmt.Sprintf("servermonitor:backup-repository:%d:v1", id))
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
