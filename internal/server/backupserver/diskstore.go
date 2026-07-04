package backupserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type diskStore struct {
	root string
}

func newDiskStore(root string) (*diskStore, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	return &diskStore{root: root}, nil
}

func (d *diskStore) Backend() BackendInfo {
	return BackendInfo{Kind: "disk", Location: d.root}
}

func (d *diskStore) repoDir(repo string) (string, error) {
	if err := validName(repo); err != nil {
		return "", err
	}
	return filepath.Join(d.root, repo), nil
}

func (d *diskStore) objectPath(repo, typ, name string) (string, error) {
	base, err := d.repoDir(repo)
	if err != nil {
		return "", err
	}
	if typ == configType {
		return filepath.Join(base, "config"), nil
	}
	if err := validType(typ); err != nil {
		return "", err
	}
	if err := validName(name); err != nil {
		return "", err
	}
	if typ == "data" && len(name) >= 2 {
		return filepath.Join(base, "data", name[:2], name), nil
	}
	return filepath.Join(base, typ, name), nil
}

func (d *diskStore) EnsureRepo(ctx context.Context, repo string) error {
	base, err := d.repoDir(repo)
	if err != nil {
		return err
	}
	for _, sub := range []string{"data", "index", "keys", "locks", "snapshots"} {
		if err := os.MkdirAll(filepath.Join(base, sub), 0o700); err != nil {
			return err
		}
	}
	return nil
}

func (d *diskStore) Create(ctx context.Context, repo, typ, name string, size int64, r io.Reader) error {
	path, err := d.objectPath(repo, typ, name)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		return ErrExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(tmpName, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrExists
		}
		if _, statErr := os.Lstat(path); statErr == nil {
			return ErrExists
		}
		return fmt.Errorf("commit backup object: %w", err)
	}
	committed = false
	syncDir(dir)
	return nil
}

func (d *diskStore) Open(ctx context.Context, repo, typ, name string) (io.ReadSeekCloser, int64, error) {
	path, err := d.objectPath(repo, typ, name)
	if err != nil {
		return nil, 0, err
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, ErrNotFound
		}
		return nil, 0, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	return f, fi.Size(), nil
}

func (d *diskStore) Stat(ctx context.Context, repo, typ, name string) (int64, error) {
	path, err := d.objectPath(repo, typ, name)
	if err != nil {
		return 0, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	return fi.Size(), nil
}

func (d *diskStore) List(ctx context.Context, repo, typ string) ([]BlobInfo, error) {
	if err := validType(typ); err != nil {
		return nil, err
	}
	base, err := d.repoDir(repo)
	if err != nil {
		return nil, err
	}
	typDir := filepath.Join(base, typ)
	out := []BlobInfo{}
	if typ == "data" {
		fanouts, err := os.ReadDir(typDir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return out, nil
			}
			return nil, err
		}
		for _, fanout := range fanouts {
			if !fanout.IsDir() {
				continue
			}
			entries, err := os.ReadDir(filepath.Join(typDir, fanout.Name()))
			if err != nil {
				return nil, err
			}
			for _, e := range entries {
				info, err := e.Info()
				if err != nil {
					return nil, err
				}
				out = append(out, BlobInfo{Name: e.Name(), Size: info.Size()})
			}
		}
		return out, nil
	}
	entries, err := os.ReadDir(typDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		out = append(out, BlobInfo{Name: e.Name(), Size: info.Size()})
	}
	return out, nil
}

func (d *diskStore) DeleteLock(ctx context.Context, repo, name string) error {
	path, err := d.objectPath(repo, "locks", name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (d *diskStore) RepoUsage(ctx context.Context, repo string) (int64, error) {
	base, err := d.repoDir(repo)
	if err != nil {
		return 0, err
	}
	var total int64
	err = filepath.WalkDir(base, func(_ string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

func (d *diskStore) RepoHasObjects(ctx context.Context, repo string) (bool, error) {
	base, err := d.repoDir(repo)
	if err != nil {
		return false, err
	}
	found := false
	err = filepath.WalkDir(base, func(_ string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		found = true
		return filepath.SkipAll
	})
	if err != nil {
		return false, err
	}
	return found, nil
}

func syncDir(dir string) {
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = f.Sync()
	_ = f.Close()
}
