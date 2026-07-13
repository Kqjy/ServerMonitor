package restserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

type S3Config struct {
	Bucket       string
	Region       string
	Prefix       string
	Endpoint     string
	UsePathStyle bool
}

type s3Store struct {
	client   *s3.Client
	bucket   string
	prefix   string
	location string
}

func newS3Store(ctx context.Context, cfg S3Config) (*s3Store, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("backup s3 bucket is required")
	}
	prefix := strings.Trim(cfg.Prefix, "/")
	if prefix == "" {
		prefix = "backups"
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.UsePathStyle {
			o.UsePathStyle = true
		}
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
	})
	location := cfg.Bucket
	if cfg.Endpoint != "" {
		location = cfg.Bucket + "@" + cfg.Endpoint
	}
	return &s3Store{client: client, bucket: cfg.Bucket, prefix: prefix, location: location}, nil
}

func (s *s3Store) Backend() BackendInfo {
	return BackendInfo{Kind: "s3", Location: s.location}
}

func (s *s3Store) key(repo, typ, name string) (string, error) {
	if err := validName(repo); err != nil {
		return "", err
	}
	if typ == configType {
		return path.Join(s.prefix, repo, "config"), nil
	}
	if err := validType(typ); err != nil {
		return "", err
	}
	if err := validName(name); err != nil {
		return "", err
	}
	return path.Join(s.prefix, repo, typ, name), nil
}

func (s *s3Store) EnsureRepo(ctx context.Context, repo string) error {
	return validName(repo)
}

func (s *s3Store) Create(ctx context.Context, repo, typ, name string, size int64, r io.Reader) error {
	key, err := s.key(repo, typ, name)
	if err != nil {
		return err
	}
	spool, err := os.CreateTemp("", "sm-backup-*")
	if err != nil {
		return err
	}
	spoolName := spool.Name()
	defer func() {
		_ = spool.Close()
		_ = os.Remove(spoolName)
	}()
	if _, err := io.Copy(spool, r); err != nil {
		return err
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          spool,
		ContentLength: aws.Int64(size),
		IfNoneMatch:   aws.String("*"),
	})
	if err == nil {
		return nil
	}
	if isPreconditionFailed(err) {
		return ErrExists
	}
	if !isNotImplemented(err) {
		return err
	}
	if _, statErr := s.Stat(ctx, repo, typ, name); statErr == nil {
		return ErrExists
	} else if !errors.Is(statErr, ErrNotFound) {
		return statErr
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          spool,
		ContentLength: aws.Int64(size),
	})
	return err
}

func (s *s3Store) Open(ctx context.Context, repo, typ, name string) (io.ReadSeekCloser, int64, error) {
	size, err := s.Stat(ctx, repo, typ, name)
	if err != nil {
		return nil, 0, err
	}
	key, err := s.key(repo, typ, name)
	if err != nil {
		return nil, 0, err
	}
	return &s3ReadSeeker{ctx: ctx, client: s.client, bucket: s.bucket, key: key, size: size}, size, nil
}

func (s *s3Store) Stat(ctx context.Context, repo, typ, name string) (int64, error) {
	key, err := s.key(repo, typ, name)
	if err != nil {
		return 0, err
	}
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	if out.ContentLength == nil {
		return 0, nil
	}
	return *out.ContentLength, nil
}

func (s *s3Store) List(ctx context.Context, repo, typ string) ([]BlobInfo, error) {
	if err := validType(typ); err != nil {
		return nil, err
	}
	if err := validName(repo); err != nil {
		return nil, err
	}
	prefix := path.Join(s.prefix, repo, typ) + "/"
	out := []BlobInfo{}
	var token *string
	for {
		page, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			return nil, err
		}
		for _, obj := range page.Contents {
			if obj.Key == nil {
				continue
			}
			name := strings.TrimPrefix(*obj.Key, prefix)
			if name == "" || strings.Contains(name, "/") {
				continue
			}
			var size int64
			if obj.Size != nil {
				size = *obj.Size
			}
			out = append(out, BlobInfo{Name: name, Size: size})
		}
		if page.IsTruncated == nil || !*page.IsTruncated {
			break
		}
		token = page.NextContinuationToken
	}
	return out, nil
}

func (s *s3Store) DeleteLock(ctx context.Context, repo, name string) error {
	key, err := s.key(repo, "locks", name)
	if err != nil {
		return err
	}
	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	return err
}

func (s *s3Store) RepoUsage(ctx context.Context, repo string) (int64, error) {
	if err := validName(repo); err != nil {
		return 0, err
	}
	prefix := path.Join(s.prefix, repo) + "/"
	var total int64
	var token *string
	for {
		page, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			return 0, err
		}
		for _, obj := range page.Contents {
			if obj.Size != nil {
				total += *obj.Size
			}
		}
		if page.IsTruncated == nil || !*page.IsTruncated {
			break
		}
		token = page.NextContinuationToken
	}
	return total, nil
}

func (s *s3Store) RepoHasObjects(ctx context.Context, repo string) (bool, error) {
	if err := validName(repo); err != nil {
		return false, err
	}
	prefix := path.Join(s.prefix, repo) + "/"
	page, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  aws.String(s.bucket),
		Prefix:  aws.String(prefix),
		MaxKeys: aws.Int32(1),
	})
	if err != nil {
		return false, err
	}
	return len(page.Contents) > 0, nil
}

type s3ReadSeeker struct {
	ctx    context.Context
	client *s3.Client
	bucket string
	key    string
	size   int64
	off    int64
	body   io.ReadCloser
}

func (s *s3ReadSeeker) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = s.off + offset
	case io.SeekEnd:
		abs = s.size + offset
	default:
		return 0, errors.New("invalid whence")
	}
	if abs < 0 {
		return 0, errors.New("negative position")
	}
	if abs != s.off {
		s.closeBody()
	}
	s.off = abs
	return abs, nil
}

func (s *s3ReadSeeker) Read(p []byte) (int, error) {
	if s.off >= s.size {
		return 0, io.EOF
	}
	if s.body == nil {
		out, err := s.client.GetObject(s.ctx, &s3.GetObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(s.key),
			Range:  aws.String(fmt.Sprintf("bytes=%d-", s.off)),
		})
		if err != nil {
			return 0, err
		}
		s.body = out.Body
	}
	n, err := s.body.Read(p)
	s.off += int64(n)
	return n, err
}

func (s *s3ReadSeeker) closeBody() {
	if s.body != nil {
		_ = s.body.Close()
		s.body = nil
	}
}

func (s *s3ReadSeeker) Close() error {
	s.closeBody()
	return nil
}

func apiErrorCode(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode()
	}
	return ""
}

func isPreconditionFailed(err error) bool {
	return apiErrorCode(err) == "PreconditionFailed"
}

func isNotImplemented(err error) bool {
	code := apiErrorCode(err)
	return code == "NotImplemented" || code == "MethodNotAllowed"
}

func isNotFound(err error) bool {
	switch apiErrorCode(err) {
	case "NotFound", "NoSuchKey":
		return true
	}
	return false
}
