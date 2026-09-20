package restserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

var (
	ErrExists          = errors.New("object already exists")
	ErrExistsIdentical = errors.New("object already exists with identical content")
	ErrExistsCorrupt   = errors.New("stored object does not match its own name")
	ErrNotFound        = errors.New("object not found")
	ErrInvalidRef      = errors.New("invalid object reference")
)

const configType = "config"

const uploadTempPrefix = ".upload-"

var objectTypes = map[string]bool{
	"data":      true,
	"index":     true,
	"keys":      true,
	"locks":     true,
	"snapshots": true,
}

var nameRE = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

func validName(s string) error {
	if s == "" || s == "." || s == ".." || !nameRE.MatchString(s) || strings.HasPrefix(s, uploadTempPrefix) {
		return fmt.Errorf("%w: name %q", ErrInvalidRef, s)
	}
	return nil
}

func deletableType(typ string) bool {
	return typ == "locks"
}

func validType(typ string) error {
	if !objectTypes[typ] {
		return fmt.Errorf("%w: type %q", ErrInvalidRef, typ)
	}
	return nil
}

type BlobInfo struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type BackendInfo struct {
	Kind     string `json:"kind"`
	Location string `json:"location"`
}

type Store interface {
	EnsureRepo(ctx context.Context, repo string) error
	Create(ctx context.Context, repo, typ, name string, size int64, r io.Reader) error
	Open(ctx context.Context, repo, typ, name string) (io.ReadSeekCloser, int64, error)
	Stat(ctx context.Context, repo, typ, name string) (int64, error)
	List(ctx context.Context, repo, typ string) ([]BlobInfo, error)
	DeleteLock(ctx context.Context, repo, name string) (int64, error)
	RepoUsage(ctx context.Context, repo string) (int64, error)
	RepoHasObjects(ctx context.Context, repo string) (bool, error)
	Backend() BackendInfo
}

type existingObject struct {
	size               int64
	verifyStoredDigest bool
	open               func() (io.ReadCloser, error)
}

func existsUnverified(err error) error {
	return fmt.Errorf("%w: the existing object could not be verified against this retry: %w", ErrExists, err)
}

func sha256Of(r io.Reader) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func classifyDuplicate(typ, name string, incoming io.Reader, incomingSize int64, existing existingObject) error {
	if existing.size != incomingSize {
		return ErrExists
	}
	incomingSum, read, err := sha256Of(incoming)
	if err != nil {
		return existsUnverified(err)
	}
	if read != incomingSize {
		return ErrExists
	}
	if typ != configType {
		if incomingSum != name {
			return ErrExists
		}
		if !existing.verifyStoredDigest {
			return ErrExistsIdentical
		}
	}
	stored, err := existing.open()
	if err != nil {
		return existsUnverified(err)
	}
	defer stored.Close()
	storedSum, _, err := sha256Of(stored)
	if err != nil {
		return existsUnverified(err)
	}
	if typ == configType {
		if storedSum != incomingSum {
			return ErrExists
		}
		return ErrExistsIdentical
	}
	if storedSum != name {
		return fmt.Errorf("%w: %s/%s", ErrExistsCorrupt, typ, name)
	}
	return ErrExistsIdentical
}
