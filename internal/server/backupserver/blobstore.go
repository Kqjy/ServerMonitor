package backupserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
)

var (
	ErrExists   = errors.New("object already exists")
	ErrNotFound = errors.New("object not found")
)

const configType = "config"

var objectTypes = map[string]bool{
	"data":      true,
	"index":     true,
	"keys":      true,
	"locks":     true,
	"snapshots": true,
}

var nameRE = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

func validName(s string) error {
	if s == "" || s == "." || s == ".." || !nameRE.MatchString(s) {
		return fmt.Errorf("invalid name %q", s)
	}
	return nil
}

func validType(typ string) error {
	if !objectTypes[typ] {
		return fmt.Errorf("invalid type %q", typ)
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
	DeleteLock(ctx context.Context, repo, name string) error
	RepoUsage(ctx context.Context, repo string) (int64, error)
	RepoHasObjects(ctx context.Context, repo string) (bool, error)
	Backend() BackendInfo
}
