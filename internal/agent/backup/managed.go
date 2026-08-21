package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"servermonitor/pkg/wire"
)

const managedBackupConfigVersion = 1

type managedBackupFile struct {
	Version       int               `json:"version"`
	ServerVersion int64             `json:"server_version"`
	Repositories  []managedRepoFile `json:"repositories"`
}

type managedRepoFile struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	EnvFile     string `json:"env_file"`
	S3Region    string `json:"s3_region,omitempty"`
	S3PathStyle bool   `json:"s3_path_style,omitempty"`
}

func ManagedBackupDir(statusPath string) string {
	statusPath = strings.TrimSpace(statusPath)
	if statusPath == "" {
		statusPath = DefaultStatusPath()
	}
	return filepath.Join(filepath.Dir(statusPath), "managed-backups")
}

func ManagedBackupConfigPath(statusPath string) string {
	return filepath.Join(ManagedBackupDir(statusPath), "repositories.json")
}

func ManagedBackupConfigExists(statusPath string) bool {
	_, err := os.Stat(ManagedBackupConfigPath(statusPath))
	return err == nil
}

func HasManagedBackupRepositories(statusPath string) bool {
	data, err := os.ReadFile(ManagedBackupConfigPath(statusPath))
	if err != nil {
		return false
	}
	var file managedBackupFile
	if err := json.Unmarshal(data, &file); err != nil {
		return false
	}
	return file.Version == managedBackupConfigVersion && len(file.Repositories) > 0
}

func ApplyManagedBackupConfig(statusPath string, remote wire.ManagedBackupConfig) error {
	if strings.TrimSpace(statusPath) == "" {
		statusPath = DefaultStatusPath()
	}
	previousNames := managedBackupRepositoryNames(statusPath)
	dir := ManagedBackupDir(statusPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create managed backup directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil && !errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("secure managed backup directory: %w", err)
	}

	file := managedBackupFile{Version: managedBackupConfigVersion, ServerVersion: remote.Version, Repositories: []managedRepoFile{}}
	keep := map[string]bool{}
	seenNames := map[string]bool{}
	seenIDs := map[int64]bool{}
	for _, repository := range remote.Repositories {
		if repository.ID <= 0 {
			return errors.New("managed repository id must be positive")
		}
		name := strings.TrimSpace(repository.Name)
		if !tunnelRepoNameRE.MatchString(name) || name == "." || name == ".." {
			return fmt.Errorf("managed repository %d has invalid name", repository.ID)
		}
		if seenNames[name] || seenIDs[repository.ID] {
			return fmt.Errorf("managed repository %q is duplicated", name)
		}
		seenNames[name] = true
		seenIDs[repository.ID] = true
		url := strings.TrimSpace(repository.URL)
		if !strings.HasPrefix(strings.ToLower(url), "s3:") || strings.ContainsAny(url, "\r\n\x00") {
			return fmt.Errorf("managed repository %q has an invalid direct S3 URL", name)
		}
		if err := validateManagedCredential(repository.AccessKeyID, repository.SecretAccessKey, repository.SessionToken); err != nil {
			return fmt.Errorf("managed repository %q credentials: %w", name, err)
		}
		credName := "repository-" + strconv.FormatInt(repository.ID, 10) + ".env"
		credPath := filepath.Join(dir, credName)
		credentials := "AWS_ACCESS_KEY_ID=" + repository.AccessKeyID + "\n" +
			"AWS_SECRET_ACCESS_KEY=" + repository.SecretAccessKey + "\n"
		if repository.SessionToken != "" {
			credentials += "AWS_SESSION_TOKEN=" + repository.SessionToken + "\n"
		}
		if err := writeFileAtomic(credPath, []byte(credentials), 0o600); err != nil {
			return fmt.Errorf("write managed repository %d credentials: %w", repository.ID, err)
		}
		keep[credName] = true
		file.Repositories = append(file.Repositories, managedRepoFile{
			ID:          repository.ID,
			Name:        name,
			URL:         url,
			EnvFile:     credPath,
			S3Region:    strings.TrimSpace(repository.S3Region),
			S3PathStyle: repository.S3PathStyle,
		})
	}
	sort.Slice(file.Repositories, func(i, j int) bool {
		if file.Repositories[i].Name != file.Repositories[j].Name {
			return file.Repositories[i].Name < file.Repositories[j].Name
		}
		return file.Repositories[i].ID < file.Repositories[j].ID
	})
	data, err := json.Marshal(file)
	if err != nil {
		return err
	}
	retiredNames := map[string]bool{}
	for name := range previousNames {
		if !seenNames[name] {
			retiredNames[name] = true
		}
	}
	if err := removeManagedRepoStatuses(statusPath, retiredNames); err != nil {
		return fmt.Errorf("remove retired managed repository status: %w", err)
	}
	if err := writeFileAtomic(ManagedBackupConfigPath(statusPath), data, 0o600); err != nil {
		return fmt.Errorf("write managed backup config: %w", err)
	}
	return removeStaleManagedCredentials(dir, keep)
}

func managedBackupRepositoryNames(statusPath string) map[string]bool {
	names := map[string]bool{}
	data, err := os.ReadFile(ManagedBackupConfigPath(statusPath))
	if err != nil {
		return names
	}
	var file managedBackupFile
	if json.Unmarshal(data, &file) != nil || file.Version != managedBackupConfigVersion {
		return names
	}
	for _, repository := range file.Repositories {
		if name := strings.TrimSpace(repository.Name); name != "" {
			names[name] = true
		}
	}
	return names
}

func removeManagedRepoStatuses(statusPath string, retired map[string]bool) error {
	if len(retired) == 0 {
		return nil
	}
	status, err := readStatusFile(statusPath)
	if err != nil {
		return err
	}
	next := removeRepoStatus(status, retired)
	if len(next.Repos) == len(status.Repos) {
		return nil
	}
	return writeStatusAtomic(statusPath, next)
}

func validateManagedCredential(accessKeyID, secretAccessKey, sessionToken string) error {
	if strings.TrimSpace(accessKeyID) == "" || strings.TrimSpace(secretAccessKey) == "" {
		return errors.New("access key id and secret access key are required")
	}
	for _, value := range []string{accessKeyID, secretAccessKey, sessionToken} {
		if strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("credential contains a newline or NUL byte")
		}
	}
	return nil
}

var managedCredentialNameRE = regexp.MustCompile(`^repository-[1-9][0-9]*\.env$`)

func removeStaleManagedCredentials(dir string, keep map[string]bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !managedCredentialNameRE.MatchString(name) || keep[name] {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove stale managed credential %s: %w", name, err)
		}
	}
	return nil
}

func loadManagedRepos(statusPath, defaultPasswordFile string) ([]Repo, error) {
	data, err := os.ReadFile(ManagedBackupConfigPath(statusPath))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read managed backup config: %w", err)
	}
	var file managedBackupFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse managed backup config: %w", err)
	}
	if file.Version != managedBackupConfigVersion {
		return nil, fmt.Errorf("managed backup config version %d is not supported", file.Version)
	}
	repos := make([]Repo, 0, len(file.Repositories))
	for _, repository := range file.Repositories {
		repos = append(repos, Repo{
			Name:         strings.TrimSpace(repository.Name),
			URL:          strings.TrimSpace(repository.URL),
			PasswordFile: defaultPasswordFile,
			EnvFile:      strings.TrimSpace(repository.EnvFile),
			S3Region:     strings.TrimSpace(repository.S3Region),
			S3PathStyle:  repository.S3PathStyle,
			managed:      true,
		})
	}
	return repos, nil
}
