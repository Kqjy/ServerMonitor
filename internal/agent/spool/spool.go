package spool

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	ErrFull   = errors.New("spool full")
	ErrEmpty  = errors.New("spool empty")
	bucketKey = []byte("batches")
)

type Spool struct {
	mu       sync.Mutex
	db       *bolt.DB
	path     string
	maxBytes int64
}

func Open(path string, maxBytes int64) (*Spool, error) {
	if path == "" {
		return nil, errors.New("spool path required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(bucketKey)
		return err
	})
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Spool{db: db, path: path, maxBytes: maxBytes}, nil
}

func (s *Spool) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Spool) Size() (count int, bytes int64, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketKey)
		st := b.Stats()
		count = st.KeyN
		bytes = int64(st.LeafInuse)
		return nil
	})
	return
}

func (s *Spool) Enqueue(body []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.maxBytes > 0 {
		fi, err := os.Stat(s.path)
		if err == nil && fi.Size() > s.maxBytes {
			if err := s.dropOldest(int64(len(body)) + 4096); err != nil {
				return err
			}
		}
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketKey)
		id, _ := b.NextSequence()
		key := make([]byte, 16)
		binary.BigEndian.PutUint64(key[:8], uint64(time.Now().UnixNano()))
		binary.BigEndian.PutUint64(key[8:], id)
		return b.Put(key, body)
	})
}

func (s *Spool) Peek() (key []byte, body []byte, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketKey)
		c := b.Cursor()
		k, v := c.First()
		if k == nil {
			return ErrEmpty
		}
		key = append([]byte(nil), k...)
		body = append([]byte(nil), v...)
		return nil
	})
	return
}

func (s *Spool) Delete(key []byte) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketKey).Delete(key)
	})
}

func (s *Spool) dropOldest(minFreed int64) error {
	freed := int64(0)
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketKey)
		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			if err := b.Delete(k); err != nil {
				return err
			}
			freed += int64(len(v))
			if freed >= minFreed {
				return nil
			}
		}
		return nil
	})
}
