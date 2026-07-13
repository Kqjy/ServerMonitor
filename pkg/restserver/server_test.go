package restserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

type fakeTarget struct {
	id    int64
	pass  string
	quota int64
	used  int64
}

type fakeRegistry struct {
	mu         sync.Mutex
	targets    map[string]fakeTarget
	reserveErr error
}

func (f *fakeRegistry) ResolveTarget(ctx context.Context, repo, secret string) (int64, int64, int64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.targets[repo]
	if !ok || t.pass != secret {
		return 0, 0, 0, false, nil
	}
	return t.id, t.quota, t.used, true, nil
}

func (f *fakeRegistry) ReserveUsage(ctx context.Context, id int64, delta int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reserveErr != nil {
		return false, f.reserveErr
	}
	for k, t := range f.targets {
		if t.id == id {
			if t.quota > 0 && (delta > t.quota || t.used > t.quota-delta) {
				return false, nil
			}
			t.used += delta
			f.targets[k] = t
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeRegistry) ReleaseUsage(ctx context.Context, id int64, delta int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, t := range f.targets {
		if t.id == id {
			t.used -= delta
			if t.used < 0 {
				t.used = 0
			}
			f.targets[k] = t
		}
	}
	return nil
}

func (f *fakeRegistry) used(repo string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.targets[repo].used
}

const dataName = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func newTestServer(t *testing.T, reg *fakeRegistry) *httptest.Server {
	t.Helper()
	store, err := NewDiskStore(t.TempDir())
	if err != nil {
		t.Fatalf("disk store: %v", err)
	}
	srv := New(store, reg, 1<<20, nil)
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	return ts
}

func do(t *testing.T, ts *httptest.Server, method, path, user, pass string, body []byte, headers map[string]string) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, ts.URL+path, rdr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func TestProtocolConformanceAndAppendOnly(t *testing.T) {
	reg := &fakeRegistry{targets: map[string]fakeTarget{
		"host1": {id: 1, pass: "secret"},
	}}
	ts := newTestServer(t, reg)
	const u, p = "host1", "secret"

	if resp := do(t, ts, http.MethodPost, "/host1?create=true", u, p, []byte{}, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("create repo = %d", resp.StatusCode)
	}

	configBody := []byte("restic-config-blob")
	if resp := do(t, ts, http.MethodPost, "/host1/config", u, p, configBody, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("post config = %d", resp.StatusCode)
	}
	if resp := do(t, ts, http.MethodPost, "/host1/config", u, p, configBody, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("re-post config should be forbidden, got %d", resp.StatusCode)
	}
	resp := do(t, ts, http.MethodGet, "/host1/config", u, p, nil, nil)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.Equal(got, configBody) {
		t.Fatalf("get config = %d body=%q", resp.StatusCode, got)
	}

	blob := []byte("0123456789")
	if resp := do(t, ts, http.MethodPost, "/host1/data/"+dataName, u, p, blob, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("post data = %d", resp.StatusCode)
	}
	if resp := do(t, ts, http.MethodPost, "/host1/data/"+dataName, u, p, []byte("evil-overwrite"), nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("overwrite must be forbidden, got %d", resp.StatusCode)
	}

	head := do(t, ts, http.MethodHead, "/host1/data/"+dataName, u, p, nil, nil)
	head.Body.Close()
	if head.StatusCode != http.StatusOK || head.Header.Get("Content-Length") != "10" {
		t.Fatalf("head = %d len=%q", head.StatusCode, head.Header.Get("Content-Length"))
	}

	rng := do(t, ts, http.MethodGet, "/host1/data/"+dataName, u, p, nil, map[string]string{"Range": "bytes=2-5"})
	rangeBody, _ := io.ReadAll(rng.Body)
	rng.Body.Close()
	if rng.StatusCode != http.StatusPartialContent || string(rangeBody) != "2345" {
		t.Fatalf("range get = %d body=%q", rng.StatusCode, rangeBody)
	}

	v1 := do(t, ts, http.MethodGet, "/host1/data/", u, p, nil, nil)
	var names []string
	json.NewDecoder(v1.Body).Decode(&names)
	v1.Body.Close()
	if len(names) != 1 || names[0] != dataName {
		t.Fatalf("v1 listing = %#v", names)
	}

	v2 := do(t, ts, http.MethodGet, "/host1/data/", u, p, nil, map[string]string{"Accept": restV2ContentType})
	if ct := v2.Header.Get("Content-Type"); ct != restV2ContentType {
		t.Fatalf("v2 content-type = %q", ct)
	}
	var infos []BlobInfo
	json.NewDecoder(v2.Body).Decode(&infos)
	v2.Body.Close()
	if len(infos) != 1 || infos[0].Name != dataName || infos[0].Size != 10 {
		t.Fatalf("v2 listing = %#v", infos)
	}

	for _, path := range []string{"/host1/data/" + dataName, "/host1/config", "/host1"} {
		if resp := do(t, ts, http.MethodDelete, path, u, p, nil, nil); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("DELETE %s = %d, want 403", path, resp.StatusCode)
		}
	}

	if resp := do(t, ts, http.MethodPost, "/host1/locks/"+dataName, u, p, []byte("lock"), nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("post lock = %d", resp.StatusCode)
	}
	if resp := do(t, ts, http.MethodDelete, "/host1/locks/"+dataName, u, p, nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("delete lock should be allowed, got %d", resp.StatusCode)
	}
}

func TestNameTraversalRejected(t *testing.T) {
	reg := &fakeRegistry{targets: map[string]fakeTarget{"host1": {id: 1, pass: "secret"}}}
	ts := newTestServer(t, reg)
	for _, name := range []string{"..", ".", "bad$name", "with/slash"} {
		resp := do(t, ts, http.MethodGet, "/host1/data/"+name, "host1", "secret", nil, nil)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("traversal/invalid name %q returned 200", name)
		}
	}
}

func TestPerRepoIsolation(t *testing.T) {
	reg := &fakeRegistry{targets: map[string]fakeTarget{
		"hosta": {id: 1, pass: "secret-a"},
		"hostb": {id: 2, pass: "secret-b"},
	}}
	ts := newTestServer(t, reg)

	if resp := do(t, ts, http.MethodGet, "/hostb/config", "hosta", "secret-a", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cross-repo access with a's credential = %d, want 401", resp.StatusCode)
	}
	if resp := do(t, ts, http.MethodGet, "/host1/config", "host1", "nope", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown repo = %d, want 401", resp.StatusCode)
	}
	if resp := do(t, ts, http.MethodGet, "/hosta/config", "", "", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no auth = %d, want 401", resp.StatusCode)
	}
	if resp := do(t, ts, http.MethodGet, "/hosta/config", "hosta", "wrong", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong secret = %d, want 401", resp.StatusCode)
	}
}

func TestQuotaEnforced(t *testing.T) {
	reg := &fakeRegistry{targets: map[string]fakeTarget{
		"host1": {id: 1, pass: "secret", quota: 8, used: 0},
	}}
	ts := newTestServer(t, reg)
	if resp := do(t, ts, http.MethodPost, "/host1/data/"+dataName, "host1", "secret", []byte("0123456789"), nil); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("over-quota post = %d, want 413", resp.StatusCode)
	}
}

func TestQuotaReservationIsAtomicAcrossConcurrentUploads(t *testing.T) {
	reg := &fakeRegistry{targets: map[string]fakeTarget{
		"host1": {id: 1, pass: "secret", quota: 10},
	}}
	ts := newTestServer(t, reg)
	paths := []string{"/host1/data/" + dataName, "/host1/data/" + strings.Repeat("b", 64)}
	start := make(chan struct{})
	statuses := make(chan int, len(paths))
	for _, path := range paths {
		go func() {
			<-start
			resp := do(t, ts, http.MethodPost, path, "host1", "secret", []byte("123456"), nil)
			resp.Body.Close()
			statuses <- resp.StatusCode
		}()
	}
	close(start)
	counts := map[int]int{}
	for range paths {
		counts[<-statuses]++
	}
	if counts[http.StatusOK] != 1 || counts[http.StatusRequestEntityTooLarge] != 1 {
		t.Fatalf("concurrent quota statuses = %#v", counts)
	}
	if got := reg.used("host1"); got != 6 {
		t.Fatalf("reserved usage = %d, want 6", got)
	}
}

func TestUsageReservationFailurePreventsCommit(t *testing.T) {
	reg := &fakeRegistry{
		targets:    map[string]fakeTarget{"host1": {id: 1, pass: "secret"}},
		reserveErr: errors.New("accounting unavailable"),
	}
	ts := newTestServer(t, reg)
	resp := do(t, ts, http.MethodPost, "/host1/data/"+dataName, "host1", "secret", []byte("payload"), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("reservation failure status = %d, want 500", resp.StatusCode)
	}
	reg.mu.Lock()
	reg.reserveErr = nil
	reg.mu.Unlock()
	head := do(t, ts, http.MethodHead, "/host1/data/"+dataName, "host1", "secret", nil, nil)
	head.Body.Close()
	if head.StatusCode != http.StatusNotFound {
		t.Fatalf("object committed without accounting: status=%d", head.StatusCode)
	}
}

type commitThenErrorStore struct {
	Store
}

func (s commitThenErrorStore) Create(ctx context.Context, repo, typ, name string, size int64, r io.Reader) error {
	if err := s.Store.Create(ctx, repo, typ, name, size, r); err != nil {
		return err
	}
	return errors.New("response lost after commit")
}

func TestAmbiguousStoreFailureRetainsUsageReservation(t *testing.T) {
	reg := &fakeRegistry{targets: map[string]fakeTarget{"host1": {id: 1, pass: "secret"}}}
	disk, err := NewDiskStore(t.TempDir())
	if err != nil {
		t.Fatalf("disk store: %v", err)
	}
	ts := httptest.NewServer(New(commitThenErrorStore{Store: disk}, reg, 1<<20, nil).Routes())
	t.Cleanup(ts.Close)
	body := []byte("payload")
	resp := do(t, ts, http.MethodPost, "/host1/data/"+dataName, "host1", "secret", body, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("ambiguous commit status = %d, want 500", resp.StatusCode)
	}
	if got := reg.used("host1"); got != int64(len(body)) {
		t.Fatalf("usage after ambiguous committed write = %d, want %d", got, len(body))
	}
	if size, err := disk.Stat(context.Background(), "host1", "data", dataName); err != nil || size != int64(len(body)) {
		t.Fatalf("committed object = size %d err %v", size, err)
	}
}

func TestResticRoundTripAppendOnly(t *testing.T) {
	resticBin, err := exec.LookPath("restic")
	if err != nil {
		t.Skip("restic not installed")
	}
	reg := &fakeRegistry{targets: map[string]fakeTarget{"e2e": {id: 1, pass: "rest-pass"}}}
	ts := newTestServer(t, reg)
	repoURL := "rest:" + strings.Replace(ts.URL, "http://", "http://e2e:rest-pass@", 1) + "/e2e"

	env := append(os.Environ(), "RESTIC_PASSWORD=encryption-pass", "RESTIC_REPOSITORY="+repoURL)
	run := func(wantErr bool, args ...string) {
		t.Helper()
		cmd := exec.Command(resticBin, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if wantErr && err == nil {
			t.Fatalf("restic %v: expected failure, got success: %s", args, out)
		}
		if !wantErr && err != nil {
			t.Fatalf("restic %v: %v: %s", args, err, out)
		}
	}

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "file.txt"), []byte("hello backup"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	run(false, "init")
	run(false, "backup", src)
	run(false, "check")
	run(true, "prune")
	run(false, "unlock")

	restored := t.TempDir()
	run(false, "restore", "latest", "--target", restored)
	got, err := os.ReadFile(filepath.Join(restored, filepath.Base(src), "file.txt"))
	if err != nil {
		found := false
		_ = filepath.WalkDir(restored, func(p string, d os.DirEntry, _ error) error {
			if d != nil && d.Name() == "file.txt" {
				b, _ := os.ReadFile(p)
				if string(b) == "hello backup" {
					found = true
				}
			}
			return nil
		})
		if !found {
			t.Fatalf("restored file not found: %v", err)
		}
		return
	}
	if string(got) != "hello backup" {
		t.Fatalf("restored content = %q", got)
	}
}

func TestDiskStoreExclusiveCreateAndUsage(t *testing.T) {
	store, err := NewDiskStore(t.TempDir())
	if err != nil {
		t.Fatalf("disk store: %v", err)
	}
	ctx := context.Background()
	if err := store.Create(ctx, "host1", "data", dataName, 3, strings.NewReader("abc")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.Create(ctx, "host1", "data", dataName, 3, strings.NewReader("xyz")); err != ErrExists {
		t.Fatalf("double create = %v, want ErrExists", err)
	}
	if err := store.Create(ctx, "host1", "index", "idx01", 5, strings.NewReader("hello")); err != nil {
		t.Fatalf("create index: %v", err)
	}
	usage, err := store.RepoUsage(ctx, "host1")
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if usage != 8 {
		t.Fatalf("usage = %d, want 8", usage)
	}
	rc, size, err := store.Open(ctx, "host1", "data", dataName)
	if err != nil || size != 3 {
		t.Fatalf("open = %v size=%d", err, size)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "abc" {
		t.Fatalf("read back = %q", got)
	}
}

func TestDiskStoreRepoHasObjects(t *testing.T) {
	store, err := NewDiskStore(t.TempDir())
	if err != nil {
		t.Fatalf("disk store: %v", err)
	}
	ctx := context.Background()

	if has, err := store.RepoHasObjects(ctx, "ghost"); err != nil || has {
		t.Fatalf("nonexistent repo: has=%v err=%v, want has=false", has, err)
	}

	if err := store.EnsureRepo(ctx, "empty"); err != nil {
		t.Fatalf("ensure repo: %v", err)
	}
	if has, err := store.RepoHasObjects(ctx, "empty"); err != nil || has {
		t.Fatalf("repo with only empty dirs: has=%v err=%v, want has=false", has, err)
	}

	if err := store.Create(ctx, "zerobyte", "keys", "key01", 0, strings.NewReader("")); err != nil {
		t.Fatalf("create zero-byte object: %v", err)
	}
	if usage, err := store.RepoUsage(ctx, "zerobyte"); err != nil || usage != 0 {
		t.Fatalf("zero-byte usage = %d err=%v, want 0", usage, err)
	}
	if has, err := store.RepoHasObjects(ctx, "zerobyte"); err != nil || !has {
		t.Fatalf("repo with a zero-byte object: has=%v err=%v, want has=true", has, err)
	}
}

type fakeS3Client struct {
	conditionalPuts   int
	unconditionalPuts int
	heads             int
}

func (f *fakeS3Client) PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if aws.ToString(in.IfNoneMatch) == "*" {
		f.conditionalPuts++
		return nil, &smithy.GenericAPIError{Code: "NotImplemented", Message: "conditional put unsupported"}
	}
	f.unconditionalPuts++
	return &s3.PutObjectOutput{}, nil
}

func (f *fakeS3Client) HeadObject(ctx context.Context, in *s3.HeadObjectInput, opts ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.heads++
	return nil, &smithy.GenericAPIError{Code: "NotFound", Message: "missing"}
}

func (f *fakeS3Client) GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	return nil, errors.New("unexpected get")
}

func (f *fakeS3Client) ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, opts ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	return nil, errors.New("unexpected list")
}

func (f *fakeS3Client) DeleteObject(ctx context.Context, in *s3.DeleteObjectInput, opts ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	return nil, errors.New("unexpected delete")
}

func TestS3ConditionalCreateUnsupportedFailsClosedForImmutableObjects(t *testing.T) {
	client := &fakeS3Client{}
	store := &s3Store{client: client, bucket: "bucket", prefix: "backups"}
	err := store.Create(context.Background(), "host1", "data", dataName, 3, strings.NewReader("abc"))
	if err == nil || !strings.Contains(err.Error(), "append-only conditional create is unsupported") {
		t.Fatalf("immutable create error = %v", err)
	}
	if client.conditionalPuts != 1 || client.unconditionalPuts != 0 || client.heads != 0 {
		t.Fatalf("immutable create calls = conditional %d unconditional %d heads %d", client.conditionalPuts, client.unconditionalPuts, client.heads)
	}
}

func TestS3ConditionalCreateUnsupportedFallsBackOnlyForLocks(t *testing.T) {
	client := &fakeS3Client{}
	store := &s3Store{client: client, bucket: "bucket", prefix: "backups"}
	err := store.Create(context.Background(), "host1", "locks", dataName, 3, strings.NewReader("abc"))
	if err != nil {
		t.Fatalf("lock create: %v", err)
	}
	if client.conditionalPuts != 1 || client.unconditionalPuts != 1 || client.heads != 1 {
		t.Fatalf("lock create calls = conditional %d unconditional %d heads %d", client.conditionalPuts, client.unconditionalPuts, client.heads)
	}
}
