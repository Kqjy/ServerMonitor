package backup

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"servermonitor/pkg/wire"
)

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func missingPath(string) error {
	return os.ErrNotExist
}

func defaultExcludesLine() string {
	quoted := make([]string, len(DefaultLinuxExcludes))
	for i, exclude := range DefaultLinuxExcludes {
		quoted[i] = strconv.Quote(exclude)
	}
	return "excludes = [" + strings.Join(quoted, ", ") + "]"
}

func TestRenderBackupTOMLIsValid(t *testing.T) {
	t.Setenv("SM_BACKUP_STATUS_PATH", "/tmp/backup-status.json")
	getenv := envFrom(map[string]string{
		"SM_BACKUP_REPOS":                "rest:https://sv/backup/web01,s3:https://s3/bucket",
		"SM_BACKUP_REPO_NAMES":           "web01",
		"SM_BACKUP_PATHS":                "/etc,/home",
		"SM_BACKUP_S3_REGION":            "us-east-1",
		"SM_BACKUP_S3_PATH_STYLE":        "1",
		"SM_BACKUP_TIME":                 "03:15",
		"SM_BACKUP_S3_ACCESS_KEY_ID":     "AKIA",
		"SM_BACKUP_S3_SECRET_ACCESS_KEY": "secret",
	})
	toml, err := renderBackupTOML(getenv, "/state/backup/backup.key", "/state/backup/repo-credentials.env", true, missingPath)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		`status_path = "/tmp/backup-status.json"`,
		`prune_mode = "external"`,
		"[[repo]]\nname = \"web01\"\nurl = \"rest:https://sv/backup/web01\"",
		"name = \"repo2\"\nurl = \"s3:https://s3/bucket\"",
		`env_file = "/state/backup/repo-credentials.env"`,
		`s3_region = "us-east-1"`,
		`s3_path_style = true`,
		"[schedule]\nenabled = true\nbackup_time = \"03:15\"",
	} {
		if !strings.Contains(toml, want) {
			t.Fatalf("rendered toml missing %q\n%s", want, toml)
		}
	}
	if runtime.GOOS == "windows" {
		return
	}
	path := filepath.Join(t.TempDir(), "backup.toml")
	if err := os.WriteFile(path, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("generated backup.toml does not parse/validate: %v\n%s", err, toml)
	}
	if cfg.PruneMode != "external" {
		t.Fatalf("container prune_mode should default to external, got %q", cfg.PruneMode)
	}
	if cfg.Schedule == nil || !cfg.Schedule.Enabled || cfg.Schedule.BackupTime != "03:15" {
		t.Fatalf("schedule not rendered: %#v", cfg.Schedule)
	}
}

func TestRenderBackupTOMLScopeOverrides(t *testing.T) {
	tests := []struct {
		name         string
		env          map[string]string
		wantExcludes string
		wantOneFS    string
	}{
		{name: "defaults", env: map[string]string{}, wantExcludes: defaultExcludesLine(), wantOneFS: "one_file_system = true"},
		{name: "no excludes", env: map[string]string{"SM_BACKUP_EXCLUDES": "none"}, wantExcludes: "excludes = []", wantOneFS: "one_file_system = true"},
		{name: "append excludes", env: map[string]string{"SM_BACKUP_EXCLUDES": "+/srv/cache"}, wantExcludes: strings.TrimSuffix(defaultExcludesLine(), "]") + ", \"/srv/cache\"]", wantOneFS: "one_file_system = true"},
		{name: "replace excludes", env: map[string]string{"SM_BACKUP_EXCLUDES": "/a,/b"}, wantExcludes: "excludes = [\"/a\", \"/b\"]", wantOneFS: "one_file_system = true"},
		{name: "cross filesystems", env: map[string]string{"SM_BACKUP_ONE_FILE_SYSTEM": "0"}, wantExcludes: defaultExcludesLine(), wantOneFS: "one_file_system = false"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.env["SM_BACKUP_REPOS"] = "rest:https://example/repo"
			rendered, err := renderBackupTOML(envFrom(test.env), "/key", "/creds", false, missingPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(rendered, test.wantExcludes+"\n") || !strings.Contains(rendered, test.wantOneFS+"\n") {
				t.Fatalf("scope rendering mismatch\n%s", rendered)
			}
		})
	}
}

func TestRenderBackupTOMLDockerVolumesPath(t *testing.T) {
	tests := []struct {
		name       string
		env        map[string]string
		statPath   string
		statResult error
		want       bool
	}{
		{name: "host path exists", env: map[string]string{}, statPath: dockerVolumesPath, want: true},
		{name: "container host path exists", env: map[string]string{"SM_HOST_FS_ROOT": "/host"}, statPath: "/host" + dockerVolumesPath, want: true},
		{name: "path missing", env: map[string]string{}, statPath: dockerVolumesPath, statResult: os.ErrNotExist, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.env["SM_BACKUP_REPOS"] = "rest:https://example/repo"
			called := ""
			rendered, err := renderBackupTOML(envFrom(test.env), "/key", "/creds", false, func(path string) error {
				called = path
				return test.statResult
			})
			if err != nil {
				t.Fatal(err)
			}
			if called != test.statPath {
				t.Fatalf("stat path = %q, want %q", called, test.statPath)
			}
			hasPath := strings.Contains(rendered, `"/var/lib/docker/volumes"`)
			if hasPath != test.want {
				t.Fatalf("volumes path presence = %v, want %v\n%s", hasPath, test.want, rendered)
			}
		})
	}

	called := false
	rendered, err := renderBackupTOML(envFrom(map[string]string{
		"SM_BACKUP_REPOS": "rest:https://example/repo",
		"SM_BACKUP_PATHS": "/srv",
	}), "/key", "/creds", false, func(string) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if called || strings.Contains(rendered, `"/var/lib/docker/volumes"`) {
		t.Fatalf("explicit paths must not stat or append Docker volumes\n%s", rendered)
	}
}

func TestRenderRecoveryKitRepositoryTypesAndCredentials(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "backup.toml")
	passwordPath := filepath.Join(dir, "backup.key")
	credsPath := filepath.Join(dir, "repo-credentials.env")
	if err := os.WriteFile(passwordPath, []byte("repository-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credsPath, []byte("RESTIC_REST_USERNAME=archive\nRESTIC_REST_PASSWORD=transport-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := `paths = ["/etc"]
[retention]
daily = 1
weekly = 0
monthly = 0
[[repo]]
name = "direct"
url = "rest:https://backup.example/direct"
password_file = "` + filepath.ToSlash(passwordPath) + `"
[[repo]]
name = "archive"
tunnel_name = "archive"
tunnel_node = "node-a"
password_file = "` + filepath.ToSlash(passwordPath) + `"
env_file = "` + filepath.ToSlash(credsPath) + `"
[tunnel]
private_key_file = "` + filepath.ToSlash(filepath.Join(dir, "tunnel.key")) + `"
local_ip = "10.83.0.2"
[[tunnel.node]]
host = "node-a"
public_key = "node-key"
endpoint = "node.example:51820"
ip = "10.83.1.1"
rest_port = 8443
`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	kit, err := RenderRecoveryKit(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"direct -> rest:https://backup.example/direct",
		"restic -r rest:https://backup.example/direct snapshots",
		"archive -> tunnel:node-a/archive on storage node node-a (endpoint node.example:51820)",
		"RESTIC_REST_PASSWORD=transport-secret",
		"sm-agent backup proxy --config " + configPath,
		`recreate backup.toml with tunnel_name = "archive" and tunnel_node = "node-a"`,
	} {
		if !strings.Contains(kit, want) {
			t.Fatalf("recovery kit missing %q\n%s", want, kit)
		}
	}
}

func TestLinuxInstallerDefaultExcludesStayInSync(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "install-agent-linux.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), defaultExcludesLine()) {
		t.Fatalf("Linux installer does not contain canonical excludes line %q", defaultExcludesLine())
	}
}

func TestRenderBackupTOMLTunnelRepositories(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "backup.key")
	credsPath := filepath.Join(dir, "repo-credentials.env")
	tests := []struct {
		name string
		spec string
		want []string
	}{
		{name: "server", spec: "tunnel:archive", want: []string{`name = "archive"`, `tunnel_name = "archive"`}},
		{name: "node", spec: "tunnel:store-01/archive", want: []string{`name = "archive"`, `tunnel_name = "archive"`, `tunnel_node = "store-01"`}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			getenv := envFrom(map[string]string{
				"SM_BACKUP_REPOS":            test.spec,
				"SM_BACKUP_REST_USERNAME":    "ignored",
				"SM_BACKUP_REST_PASSWORD":    "ignored",
				"SM_BACKUP_S3_ACCESS_KEY_ID": "ignored",
			})
			rendered, err := renderBackupTOML(getenv, keyPath, credsPath, true, missingPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range test.want {
				if !strings.Contains(rendered, want) {
					t.Fatalf("rendered toml missing %q\n%s", want, rendered)
				}
			}
			if strings.Contains(rendered, "url =") {
				t.Fatalf("tunnel repository rendered url\n%s", rendered)
			}
			if !strings.Contains(rendered, "env_file = ") {
				t.Fatalf("tunnel repository missing credentials file\n%s", rendered)
			}
		})
	}
}

func TestRenderBackupTOMLRejectsMalformedTunnelRepositories(t *testing.T) {
	dir := t.TempDir()
	for _, spec := range []string{"tunnel:", "tunnel:/archive", "tunnel:node/", "tunnel:a/b/c", "tunnel:bad name", "tunnel:bad node/archive"} {
		t.Run(spec, func(t *testing.T) {
			getenv := envFrom(map[string]string{"SM_BACKUP_REPOS": spec})
			if _, err := renderBackupTOML(getenv, filepath.Join(dir, "backup.key"), filepath.Join(dir, "creds.env"), false, missingPath); err == nil {
				t.Fatalf("malformed tunnel repository %q should fail", spec)
			}
		})
	}
}

func TestRenderBackupTOMLMixedRemoteAndTunnelRepositories(t *testing.T) {
	dir := t.TempDir()
	rendered, err := renderBackupTOML(envFrom(map[string]string{
		"SM_BACKUP_REPOS":         "rest:https://monitor.example/backup/direct,tunnel:node-a/archive",
		"SM_BACKUP_REPO_NAMES":    "direct,archive",
		"SM_BACKUP_REST_USERNAME": "direct",
	}), filepath.Join(dir, "backup.key"), filepath.Join(dir, "repo-credentials.env"), true, missingPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`name = "direct"`,
		`url = "rest:https://monitor.example/backup/direct"`,
		`name = "archive"`,
		`tunnel_name = "archive"`,
		`tunnel_node = "node-a"`,
	} {
		if !strings.Contains(filepath.ToSlash(rendered), want) {
			t.Fatalf("rendered toml missing %q\n%s", want, rendered)
		}
	}
	if strings.Count(rendered, "env_file =") != 2 {
		t.Fatalf("credentials should apply to both rest and tunnel repositories\n%s", rendered)
	}
}

func TestEnsureBackupKeyNeverRegenerates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup.key")
	gen, err := ensureBackupKey(path)
	if err != nil || !gen {
		t.Fatalf("first ensure should generate: gen=%v err=%v", gen, err)
	}
	first, _ := os.ReadFile(path)
	if len(strings.TrimSpace(string(first))) == 0 {
		t.Fatal("key file is empty")
	}
	gen, err = ensureBackupKey(path)
	if err != nil || gen {
		t.Fatalf("second ensure must NOT regenerate: gen=%v err=%v", gen, err)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Fatal("existing backup key must never be overwritten")
	}
}

func TestWriteCredsFileOmittedWhenNone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo-credentials.env")
	have, err := writeCredsFile(envFrom(nil), path)
	if err != nil || have {
		t.Fatalf("no creds should yield have=false: have=%v err=%v", have, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("no creds file should be written when none provided")
	}
	have, err = writeCredsFile(envFrom(map[string]string{"SM_BACKUP_REST_PASSWORD": "pw"}), path)
	if err != nil || !have {
		t.Fatalf("creds present should yield have=true: have=%v err=%v", have, err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "RESTIC_REST_PASSWORD=pw") {
		t.Fatalf("creds file missing value: %s", data)
	}
}

func TestEnsureTunnelEnrolledProvisionCycles(t *testing.T) {
	dir := t.TempDir()
	resticPath := filepath.Join(dir, "sm-restic")
	if err := os.WriteFile(resticPath, []byte("restic"), 0o755); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	postedKeys := []string{}
	nodeHits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agent/tunnel":
			var request wire.TunnelEnrollRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode enrollment: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			mu.Lock()
			postedKeys = append(postedKeys, request.PublicKey)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(wire.TunnelEnrollResponse{
				ServerPublicKey: "server-public-key",
				Endpoint:        "monitor.example:51820",
				TunnelIP:        "10.83.0.9",
				ServerTunnelIP:  "10.83.0.1",
				RestPort:        8443,
				ServerStorage:   true,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/agent/tunnel/nodes":
			mu.Lock()
			nodeHits++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(wire.TunnelNodesResponse{Nodes: []wire.TunnelNodeInfo{{
				Hostname:  "node-a",
				PublicKey: "node-public-key",
				Endpoint:  "node.example:51820",
				TunnelIP:  "10.83.1.1",
				RestPort:  8443,
			}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	configPath := filepath.Join(dir, "backup.toml")
	keyPath := filepath.Join(dir, "tunnel.key")
	getenv := envFrom(map[string]string{
		"SM_BACKUP_CONFIG": configPath,
		"SM_BACKUP_REPOS":  "tunnel:node-a/archive",
		"SM_RESTIC_SOURCE": resticPath,
	})
	opts := EnrollOptions{ServerURL: server.URL, Token: "token", KeyPath: keyPath, HTTPClient: server.Client()}
	firstProvision, err := Provision(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if !firstProvision.TunnelRepos {
		t.Fatal("provision result did not report tunnel repositories")
	}
	if err := EnsureTunnelEnrolled(context.Background(), configPath, opts); err != nil {
		t.Fatal(err)
	}
	firstKey, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "[tunnel]") || !strings.Contains(string(data), "[[tunnel.node]]") {
		t.Fatalf("enrolled tunnel settings missing\n%s", data)
	}
	if _, err := Provision(getenv); err != nil {
		t.Fatal(err)
	}
	if err := EnsureTunnelEnrolled(context.Background(), configPath, opts); err != nil {
		t.Fatal(err)
	}
	secondKey, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstKey) != string(secondKey) {
		t.Fatal("tunnel key changed after reprovisioning")
	}
	mu.Lock()
	if len(postedKeys) != 2 || postedKeys[0] == "" || postedKeys[0] != postedKeys[1] || nodeHits != 2 {
		t.Fatalf("enrollment calls after two provision cycles: keys=%v node_hits=%d", postedKeys, nodeHits)
	}
	mu.Unlock()
	if err := EnsureTunnelEnrolled(context.Background(), configPath, opts); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(postedKeys) != 2 || nodeHits != 2 {
		t.Fatalf("complete enrollment should be a network no-op: posts=%d node_hits=%d", len(postedKeys), nodeHits)
	}
}

func TestEnsureTunnelEnrolledNodeNotPromoted(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "backup.toml")
	rendered, err := renderBackupTOML(envFrom(map[string]string{"SM_BACKUP_REPOS": "tunnel:missing/archive"}), filepath.Join(dir, "backup.key"), filepath.Join(dir, "creds.env"), false, missingPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(rendered), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/agent/tunnel":
			_ = json.NewEncoder(w).Encode(wire.TunnelEnrollResponse{ServerPublicKey: "server-key", Endpoint: "server.example:51820", TunnelIP: "10.83.0.9", ServerTunnelIP: "10.83.0.1", RestPort: 8443})
		case "/api/v1/agent/tunnel/nodes":
			_ = json.NewEncoder(w).Encode(wire.TunnelNodesResponse{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	err = EnsureTunnelEnrolled(context.Background(), configPath, EnrollOptions{ServerURL: server.URL, Token: "token", KeyPath: filepath.Join(dir, "tunnel.key"), HTTPClient: server.Client()})
	if err == nil || !strings.Contains(err.Error(), "not a promoted backup node") {
		t.Fatalf("node-not-promoted error did not propagate: %v", err)
	}
}

func TestRecordTunnelEnrollmentFailure(t *testing.T) {
	dir := t.TempDir()
	statusPath := filepath.Join(dir, "backup-status.json")
	t.Setenv("SM_BACKUP_STATUS_PATH", statusPath)
	configPath := filepath.Join(dir, "backup.toml")
	rendered, err := renderBackupTOML(envFrom(map[string]string{
		"SM_BACKUP_REPOS":      "rest:https://monitor.example/backup/direct,tunnel:archive",
		"SM_BACKUP_REPO_NAMES": "direct,archive",
	}), filepath.Join(dir, "backup.key"), filepath.Join(dir, "creds.env"), false, missingPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(rendered), 0o600); err != nil {
		t.Fatal(err)
	}
	lastSuccess := time.Date(2026, 7, 14, 2, 30, 0, 0, time.UTC)
	direct := RepoStatus{Name: "direct", Engine: "restic", LastSuccess: &lastSuccess, Success: true, AddedBytes: 42}
	if err := writeStatusAtomic(statusPath, StatusFile{Repos: []RepoStatus{
		direct,
		{Name: "archive", Engine: "restic", LastSuccess: &lastSuccess, Success: true, Tunnel: true},
	}}); err != nil {
		t.Fatal(err)
	}
	finished := time.Date(2026, 7, 15, 3, 4, 5, 987, time.Local)
	if err := RecordTunnelEnrollmentFailure(configPath, errors.New("server unavailable"), finished); err != nil {
		t.Fatal(err)
	}
	status, err := readStatusFile(statusPath)
	if err != nil {
		t.Fatal(err)
	}
	var gotDirect *RepoStatus
	var gotTunnel *RepoStatus
	for i := range status.Repos {
		switch status.Repos[i].Name {
		case "direct":
			gotDirect = &status.Repos[i]
		case "archive":
			gotTunnel = &status.Repos[i]
		}
	}
	if gotDirect == nil || !gotDirect.Success || gotDirect.AddedBytes != direct.AddedBytes || gotDirect.LastSuccess == nil || !gotDirect.LastSuccess.Equal(lastSuccess) {
		t.Fatalf("non-tunnel status changed: %+v", gotDirect)
	}
	if gotTunnel == nil || gotTunnel.Success || gotTunnel.Error != "tunnel enrollment failed: server unavailable" || !gotTunnel.Tunnel {
		t.Fatalf("tunnel failure status missing: %+v", gotTunnel)
	}
	wantFinished := utcSecond(finished)
	if gotTunnel.LastFinished == nil || !gotTunnel.LastFinished.Equal(wantFinished) || gotTunnel.LastSuccess == nil || !gotTunnel.LastSuccess.Equal(lastSuccess) {
		t.Fatalf("tunnel timestamps wrong: %+v", gotTunnel)
	}
}
