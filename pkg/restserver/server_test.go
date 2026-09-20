package restserver

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
	ts, _ := newTestServerWithDir(t, reg)
	return ts
}

func newTestServerWithDir(t *testing.T, reg *fakeRegistry) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := NewDiskStore(dir)
	if err != nil {
		t.Fatalf("disk store: %v", err)
	}
	srv := New(store, reg, 1<<20, nil)
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	return ts, dir
}

func contentName(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func objectDiskPath(dir, repo, typ, name string) string {
	if typ == "data" {
		return filepath.Join(dir, repo, typ, name[:2], name)
	}
	return filepath.Join(dir, repo, typ, name)
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
	if resp := do(t, ts, http.MethodPost, "/host1/config", u, p, configBody, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("identical config re-post should be idempotent, got %d", resp.StatusCode)
	}
	if resp := do(t, ts, http.MethodPost, "/host1/config", u, p, []byte("different-config!!"), nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("config overwrite must be forbidden, got %d", resp.StatusCode)
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

func TestIdenticalRePostIsIdempotent(t *testing.T) {
	reg := &fakeRegistry{targets: map[string]fakeTarget{"host1": {id: 1, pass: "secret"}}}
	ts, dir := newTestServerWithDir(t, reg)
	const u, p = "host1", "secret"

	var wantUsage int64
	for _, typ := range []string{"data", "index", "snapshots", "keys", "locks"} {
		body := []byte("payload for " + typ)
		name := contentName(body)
		route := "/host1/" + typ + "/" + name
		if resp := do(t, ts, http.MethodPost, route, u, p, body, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("first post %s = %d", typ, resp.StatusCode)
		}
		wantUsage += int64(len(body))
		if got := reg.used("host1"); got != wantUsage {
			t.Fatalf("usage after first post %s = %d, want %d", typ, got, wantUsage)
		}
		stored := objectDiskPath(dir, "host1", typ, name)
		before, err := os.Stat(stored)
		if err != nil {
			t.Fatalf("stat stored %s: %v", typ, err)
		}
		if resp := do(t, ts, http.MethodPost, route, u, p, body, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("identical re-post %s = %d, want 200", typ, resp.StatusCode)
		}
		if got := reg.used("host1"); got != wantUsage {
			t.Fatalf("usage after identical re-post %s = %d, want %d", typ, got, wantUsage)
		}
		after, err := os.Stat(stored)
		if err != nil {
			t.Fatalf("stat stored %s after retry: %v", typ, err)
		}
		if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
			t.Fatalf("stored %s object was rewritten", typ)
		}
		onDisk, err := os.ReadFile(stored)
		if err != nil || !bytes.Equal(onDisk, body) {
			t.Fatalf("stored %s bytes = %q err=%v", typ, onDisk, err)
		}
	}
}

func TestSameLengthDifferentContentStillForbidden(t *testing.T) {
	reg := &fakeRegistry{targets: map[string]fakeTarget{"host1": {id: 1, pass: "secret"}}}
	ts, dir := newTestServerWithDir(t, reg)
	body := []byte("original-blob")
	name := contentName(body)
	if resp := do(t, ts, http.MethodPost, "/host1/data/"+name, "host1", "secret", body, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("first post = %d", resp.StatusCode)
	}
	usage := reg.used("host1")
	impostor := []byte("IMPOSTOR-blob")
	if len(impostor) != len(body) {
		t.Fatalf("test setup: lengths differ")
	}
	if resp := do(t, ts, http.MethodPost, "/host1/data/"+name, "host1", "secret", impostor, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("same-length different content = %d, want 403", resp.StatusCode)
	}
	if got := reg.used("host1"); got != usage {
		t.Fatalf("usage after rejected overwrite = %d, want %d", got, usage)
	}
	onDisk, err := os.ReadFile(objectDiskPath(dir, "host1", "data", name))
	if err != nil || !bytes.Equal(onDisk, body) {
		t.Fatalf("stored bytes = %q err=%v", onDisk, err)
	}
}

func TestCorruptStoredObjectKeepsRefusingRetry(t *testing.T) {
	reg := &fakeRegistry{targets: map[string]fakeTarget{"host1": {id: 1, pass: "secret"}}}
	ts, dir := newTestServerWithDir(t, reg)
	body := []byte("honest-payload")
	name := contentName(body)
	stored := objectDiskPath(dir, "host1", "data", name)
	if err := os.MkdirAll(filepath.Dir(stored), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	corrupt := []byte("rotten-payload")
	if len(corrupt) != len(body) {
		t.Fatalf("test setup: lengths differ")
	}
	if err := os.WriteFile(stored, corrupt, 0o600); err != nil {
		t.Fatalf("write corrupt object: %v", err)
	}
	if resp := do(t, ts, http.MethodPost, "/host1/data/"+name, "host1", "secret", body, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("retry against corrupt stored object = %d, want 403", resp.StatusCode)
	}
	if got := reg.used("host1"); got != 0 {
		t.Fatalf("usage after refused retry = %d, want 0", got)
	}
	onDisk, err := os.ReadFile(stored)
	if err != nil || !bytes.Equal(onDisk, corrupt) {
		t.Fatalf("corrupt object was touched: %q err=%v", onDisk, err)
	}
	direct, err := NewDiskStore(dir)
	if err != nil {
		t.Fatalf("disk store: %v", err)
	}
	if err := direct.Create(context.Background(), "host1", "data", name, int64(len(body)), bytes.NewReader(body)); !errors.Is(err, ErrExistsCorrupt) {
		t.Fatalf("store classification = %v, want ErrExistsCorrupt", err)
	}
}

func TestDuplicateCostsNoQuotaAtTheLimit(t *testing.T) {
	body := []byte("0123456789")
	name := contentName(body)
	reg := &fakeRegistry{targets: map[string]fakeTarget{
		"host1": {id: 1, pass: "secret", quota: int64(len(body))},
	}}
	ts := newTestServer(t, reg)
	if resp := do(t, ts, http.MethodPost, "/host1/data/"+name, "host1", "secret", body, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("first post = %d", resp.StatusCode)
	}
	if got := reg.used("host1"); got != int64(len(body)) {
		t.Fatalf("usage after first post = %d", got)
	}
	if resp := do(t, ts, http.MethodPost, "/host1/data/"+name, "host1", "secret", body, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("duplicate at quota limit = %d, want 200", resp.StatusCode)
	}
	if got := reg.used("host1"); got != int64(len(body)) {
		t.Fatalf("usage after duplicate = %d, want %d", got, len(body))
	}
	other := []byte("abcdefghij")
	if resp := do(t, ts, http.MethodPost, "/host1/data/"+contentName(other), "host1", "secret", other, nil); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("fresh object at quota limit = %d, want 413", resp.StatusCode)
	}
}

func TestDiskStoreIgnoresUploadTempFiles(t *testing.T) {
	dir := t.TempDir()
	store, err := NewDiskStore(dir)
	if err != nil {
		t.Fatalf("disk store: %v", err)
	}
	ctx := context.Background()
	body := []byte("real-object")
	name := contentName(body)
	if err := store.Create(ctx, "host1", "data", name, int64(len(body)), bytes.NewReader(body)); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "host1", "index"), 0o700); err != nil {
		t.Fatalf("mkdir index: %v", err)
	}
	temps := []string{
		filepath.Join(dir, "host1", "data", name[:2], uploadTempPrefix+"870585863"),
		filepath.Join(dir, "host1", "index", uploadTempPrefix+"12345"),
	}
	for _, tmp := range temps {
		if err := os.WriteFile(tmp, bytes.Repeat([]byte("x"), 1024), 0o600); err != nil {
			t.Fatalf("write temp %s: %v", tmp, err)
		}
	}
	infos, err := store.List(ctx, "host1", "data")
	if err != nil {
		t.Fatalf("list data: %v", err)
	}
	if len(infos) != 1 || infos[0].Name != name {
		t.Fatalf("data listing = %#v", infos)
	}
	if infos, err := store.List(ctx, "host1", "index"); err != nil || len(infos) != 0 {
		t.Fatalf("index listing = %#v err=%v", infos, err)
	}
	if usage, err := store.RepoUsage(ctx, "host1"); err != nil || usage != int64(len(body)) {
		t.Fatalf("usage = %d err=%v, want %d", usage, err, len(body))
	}
	if err := os.Remove(objectDiskPath(dir, "host1", "data", name)); err != nil {
		t.Fatalf("remove object: %v", err)
	}
	if has, err := store.RepoHasObjects(ctx, "host1"); err != nil || has {
		t.Fatalf("repo with only temp files: has=%v err=%v, want false", has, err)
	}
}

func TestDiskStoreSweepsOnlyStaleUploadTemps(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "host1", "index"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stale := filepath.Join(dir, "host1", "index", uploadTempPrefix+"stale")
	fresh := filepath.Join(dir, "host1", "index", uploadTempPrefix+"fresh")
	keep := filepath.Join(dir, "host1", "index", "idx01")
	for _, target := range []string{stale, fresh, keep} {
		if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", target, err)
		}
	}
	old := time.Now().Add(-staleUploadAge - time.Hour)
	for _, target := range []string{stale, keep} {
		if err := os.Chtimes(target, old, old); err != nil {
			t.Fatalf("chtimes %s: %v", target, err)
		}
	}
	if _, err := NewDiskStore(dir); err != nil {
		t.Fatalf("disk store: %v", err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale temp survived the sweep: %v", err)
	}
	for _, target := range []string{fresh, keep} {
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("%s was removed: %v", target, err)
		}
	}
}

type conditionalS3Client struct {
	mu              sync.Mutex
	stored          map[string][]byte
	getErr          error
	conditionalPuts int
	gets            int
}

func (c *conditionalS3Client) PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := aws.ToString(in.Key)
	if aws.ToString(in.IfNoneMatch) == "*" {
		c.conditionalPuts++
		if _, ok := c.stored[key]; ok {
			return nil, &smithy.GenericAPIError{Code: "PreconditionFailed", Message: "object exists"}
		}
	}
	body, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	if c.stored == nil {
		c.stored = map[string][]byte{}
	}
	c.stored[key] = body
	return &s3.PutObjectOutput{}, nil
}

func (c *conditionalS3Client) HeadObject(ctx context.Context, in *s3.HeadObjectInput, opts ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	body, ok := c.stored[aws.ToString(in.Key)]
	if !ok {
		return nil, &smithy.GenericAPIError{Code: "NotFound", Message: "missing"}
	}
	return &s3.HeadObjectOutput{ContentLength: aws.Int64(int64(len(body)))}, nil
}

func (c *conditionalS3Client) GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gets++
	if c.getErr != nil {
		return nil, c.getErr
	}
	body, ok := c.stored[aws.ToString(in.Key)]
	if !ok {
		return nil, &smithy.GenericAPIError{Code: "NoSuchKey", Message: "missing"}
	}
	var off int64
	if rng := aws.ToString(in.Range); rng != "" {
		if _, err := fmt.Sscanf(rng, "bytes=%d-", &off); err != nil {
			return nil, err
		}
	}
	if off > int64(len(body)) {
		off = int64(len(body))
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(body[off:]))}, nil
}

func (c *conditionalS3Client) ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, opts ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	return nil, errors.New("unexpected list")
}

func (c *conditionalS3Client) DeleteObject(ctx context.Context, in *s3.DeleteObjectInput, opts ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	return nil, errors.New("unexpected delete")
}

func TestS3PreconditionFailureClassifiesDuplicate(t *testing.T) {
	client := &conditionalS3Client{}
	store := &s3Store{client: client, bucket: "bucket", prefix: "backups"}
	ctx := context.Background()
	body := []byte("s3-pack-content")
	name := contentName(body)
	if err := store.Create(ctx, "host1", "data", name, int64(len(body)), bytes.NewReader(body)); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if err := store.Create(ctx, "host1", "data", name, int64(len(body)), bytes.NewReader(body)); !errors.Is(err, ErrExistsIdentical) {
		t.Fatalf("identical re-create = %v, want ErrExistsIdentical", err)
	}
	if client.gets != 0 {
		t.Fatalf("duplicate retry re-read the stored object %d times", client.gets)
	}
	impostor := []byte("s3-evil-content")
	if len(impostor) != len(body) {
		t.Fatalf("test setup: lengths differ")
	}
	if err := store.Create(ctx, "host1", "data", name, int64(len(impostor)), bytes.NewReader(impostor)); !errors.Is(err, ErrExists) {
		t.Fatalf("same-length different content = %v, want ErrExists", err)
	}
	if got := client.stored[path.Join("backups", "host1", "data", name)]; !bytes.Equal(got, body) {
		t.Fatalf("stored object was overwritten: %q", got)
	}
	if client.conditionalPuts != 3 {
		t.Fatalf("conditional puts = %d, want 3", client.conditionalPuts)
	}
}

type truncatedReader struct {
	remaining []byte
	err       error
}

func (r *truncatedReader) Read(p []byte) (int, error) {
	if len(r.remaining) == 0 {
		return 0, r.err
	}
	n := copy(p, r.remaining)
	r.remaining = r.remaining[n:]
	return n, nil
}

func TestUnverifiableDuplicateIsStillNotCommitted(t *testing.T) {
	dir := t.TempDir()
	store, err := NewDiskStore(dir)
	if err != nil {
		t.Fatalf("disk store: %v", err)
	}
	ctx := context.Background()
	body := []byte("payload-bytes")
	name := contentName(body)
	if err := store.Create(ctx, "host1", "data", name, int64(len(body)), bytes.NewReader(body)); err != nil {
		t.Fatalf("first create: %v", err)
	}
	aborted := &truncatedReader{remaining: body[:3], err: io.ErrUnexpectedEOF}
	err = store.Create(ctx, "host1", "data", name, int64(len(body)), aborted)
	if !errors.Is(err, ErrExists) {
		t.Fatalf("aborted duplicate upload = %v, want an ErrExists classification", err)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("aborted duplicate upload lost its cause: %v", err)
	}
	onDisk, readErr := os.ReadFile(objectDiskPath(dir, "host1", "data", name))
	if readErr != nil || !bytes.Equal(onDisk, body) {
		t.Fatalf("stored bytes = %q err=%v", onDisk, readErr)
	}
}

func TestAbortedDuplicateUploadReleasesItsReservation(t *testing.T) {
	reg := &fakeRegistry{targets: map[string]fakeTarget{"host1": {id: 1, pass: "secret"}}}
	ts := newTestServer(t, reg)
	body := []byte("0123456789")
	name := contentName(body)
	if resp := do(t, ts, http.MethodPost, "/host1/data/"+name, "host1", "secret", body, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("first post = %d", resp.StatusCode)
	}
	if got := reg.used("host1"); got != int64(len(body)) {
		t.Fatalf("usage after first post = %d, want %d", got, len(body))
	}
	addr := strings.TrimPrefix(ts.URL, "http://")
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	auth := base64.StdEncoding.EncodeToString([]byte("host1:secret"))
	head := fmt.Sprintf("POST /host1/data/%s HTTP/1.1\r\nHost: %s\r\nAuthorization: Basic %s\r\nContent-Length: %d\r\n\r\n", name, addr, auth, len(body))
	if _, err := io.WriteString(conn, head+"012"); err != nil {
		t.Fatalf("write truncated request: %v", err)
	}
	if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatalf("half close: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatalf("read deadline: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("aborted duplicate retry = %d, want 403", resp.StatusCode)
	}
	if got := reg.used("host1"); got != int64(len(body)) {
		t.Fatalf("usage after an aborted duplicate retry = %d, want %d", got, len(body))
	}
}

func TestS3ConfigDuplicateStaysNotCommittedWhenTheStoredObjectIsUnreadable(t *testing.T) {
	client := &conditionalS3Client{}
	store := &s3Store{client: client, bucket: "bucket", prefix: "backups"}
	ctx := context.Background()
	body := []byte("s3-config-blob")
	if err := store.Create(ctx, "host1", configType, configType, int64(len(body)), bytes.NewReader(body)); err != nil {
		t.Fatalf("first create: %v", err)
	}
	client.getErr = errors.New("s3 read unavailable")
	err := store.Create(ctx, "host1", configType, configType, int64(len(body)), bytes.NewReader(body))
	if !errors.Is(err, ErrExists) {
		t.Fatalf("config duplicate with an unreadable stored object = %v, want an ErrExists classification", err)
	}
	if got := client.stored[path.Join("backups", "host1", "config")]; !bytes.Equal(got, body) {
		t.Fatalf("stored config was overwritten: %q", got)
	}
}

func TestUploadTempPrefixIsNotAReachableObjectName(t *testing.T) {
	reg := &fakeRegistry{targets: map[string]fakeTarget{"host1": {id: 1, pass: "secret"}}}
	ts, dir := newTestServerWithDir(t, reg)
	smuggled := uploadTempPrefix + "smuggled"
	if resp := do(t, ts, http.MethodPost, "/host1/index/"+smuggled, "host1", "secret", []byte("smuggled"), nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("post %s = %d, want 400", smuggled, resp.StatusCode)
	}
	if got := reg.used("host1"); got != 0 {
		t.Fatalf("usage after a refused temp-name post = %d, want 0", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "host1", "index", smuggled)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp-named object was stored: %v", err)
	}
	store, err := NewDiskStore(dir)
	if err != nil {
		t.Fatalf("disk store: %v", err)
	}
	if err := store.Create(context.Background(), "host1", "index", smuggled, 3, strings.NewReader("abc")); !errors.Is(err, ErrInvalidRef) {
		t.Fatalf("store create with a temp name = %v, want ErrInvalidRef", err)
	}
	if resp := do(t, ts, http.MethodPost, "/"+smuggled, smuggled, "secret", nil, nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("repository named %s = %d, want 400", smuggled, resp.StatusCode)
	}
}

type deleteRacingStore struct {
	*diskStore
	beforeCreate func()
}

func (s *deleteRacingStore) Create(ctx context.Context, repo, typ, name string, size int64, r io.Reader) error {
	if s.beforeCreate != nil {
		race := s.beforeCreate
		s.beforeCreate = nil
		race()
	}
	return s.diskStore.Create(ctx, repo, typ, name, size, r)
}

func TestAtQuotaDuplicateLockCannotCommitUnaccountedBytes(t *testing.T) {
	dir := t.TempDir()
	disk, err := NewDiskStore(dir)
	if err != nil {
		t.Fatalf("disk store: %v", err)
	}
	lock := []byte("lockbody12")
	name := contentName(lock)
	reg := &fakeRegistry{targets: map[string]fakeTarget{"host1": {id: 1, pass: "secret", quota: int64(len(lock))}}}
	racing := &deleteRacingStore{diskStore: disk}
	ts := httptest.NewServer(New(racing, reg, 1<<20, nil).Routes())
	t.Cleanup(ts.Close)

	if resp := do(t, ts, http.MethodPost, "/host1/locks/"+name, "host1", "secret", lock, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("first lock post = %d", resp.StatusCode)
	}
	if got := reg.used("host1"); got != int64(len(lock)) {
		t.Fatalf("usage after first lock post = %d, want %d", got, len(lock))
	}

	ctx := context.Background()
	racing.beforeCreate = func() {
		freed, delErr := disk.DeleteLock(ctx, "host1", name)
		if delErr != nil {
			t.Errorf("racing delete: %v", delErr)
			return
		}
		_ = reg.ReleaseUsage(ctx, 1, freed)
	}
	oversized := bytes.Repeat([]byte("z"), 4096)
	if resp := do(t, ts, http.MethodPost, "/host1/locks/"+name, "host1", "secret", oversized, nil); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("over-quota duplicate lock post = %d, want 413", resp.StatusCode)
	}
	if racing.beforeCreate == nil {
		t.Fatal("an over-quota lock post reached the store")
	}
	if got := reg.used("host1"); got != int64(len(lock)) {
		t.Fatalf("recorded usage = %d, want %d", got, len(lock))
	}
	stored, err := disk.RepoUsage(ctx, "host1")
	if err != nil {
		t.Fatalf("repo usage: %v", err)
	}
	if stored != int64(len(lock)) {
		t.Fatalf("stored bytes = %d, want %d", stored, len(lock))
	}
}
