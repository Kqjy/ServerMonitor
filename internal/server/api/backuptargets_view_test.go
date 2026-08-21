package api

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"servermonitor/internal/server/storage"
	"servermonitor/pkg/restserver"
)

type pathTestStore struct{ backend restserver.BackendInfo }

func (s pathTestStore) EnsureRepo(context.Context, string) error { return nil }
func (s pathTestStore) Create(context.Context, string, string, string, int64, io.Reader) error {
	return nil
}
func (s pathTestStore) Open(context.Context, string, string, string) (io.ReadSeekCloser, int64, error) {
	return nil, 0, nil
}
func (s pathTestStore) Stat(context.Context, string, string, string) (int64, error) {
	return 0, nil
}
func (s pathTestStore) List(context.Context, string, string) ([]restserver.BlobInfo, error) {
	return nil, nil
}
func (s pathTestStore) DeleteLock(context.Context, string, string) (int64, error) { return 0, nil }
func (s pathTestStore) RepoUsage(context.Context, string) (int64, error)          { return 0, nil }
func (s pathTestStore) RepoHasObjects(context.Context, string) (bool, error)      { return false, nil }
func (s pathTestStore) Backend() restserver.BackendInfo                           { return s.backend }

func TestBackupRepositoryViewsExposeExactEffectivePath(t *testing.T) {
	destinationID := int64(9)
	nodeID := int64(12)
	diskServer := restserver.New(pathTestStore{backend: restserver.BackendInfo{Kind: "disk", Location: "/srv/backups"}}, nil, 0, slog.New(slog.DiscardHandler))
	s3Server := restserver.New(pathTestStore{backend: restserver.BackendInfo{Kind: "s3", Location: "gateway-bucket/backups"}}, nil, 0, slog.New(slog.DiscardHandler))

	cases := []struct {
		name   string
		target storage.BackupTarget
		server *restserver.Server
		want   string
	}{
		{"server disk", storage.BackupTarget{Name: "disk"}, diskServer, "Agent → Server disk"},
		{"server S3 gateway", storage.BackupTarget{Name: "gateway"}, s3Server, "Agent → ServerMonitor → S3"},
		{"storage node", storage.BackupTarget{Name: "node", NodeHostID: &nodeID, NodeHostname: "nas-01"}, nil, "Agent → storage node"},
		{"direct S3", storage.BackupTarget{Name: "direct", DestinationID: &destinationID, DestinationName: "R2"}, nil, "Agent → S3 directly"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			view := toBackupTargetView(tc.target, tc.server)
			if view.DataPath.Label != tc.want {
				t.Fatalf("data path = %q, want %q", view.DataPath.Label, tc.want)
			}
			if view.StorageBackend.Kind == "" || view.RepositoryNamespace.Name != tc.target.Name {
				t.Fatalf("concept fields missing: %+v", view)
			}
		})
	}
}
