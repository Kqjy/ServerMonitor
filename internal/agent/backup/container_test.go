package backup

import "testing"

func TestDetectContainer(t *testing.T) {
	noStat := func(string) error { return errNotPresent }
	statMarker := func(match string) func(string) error {
		return func(path string) error {
			if path == match {
				return nil
			}
			return errNotPresent
		}
	}
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	cases := []struct {
		name string
		get  func(string) string
		stat func(string) error
		want bool
	}{
		{"host fs root set", env(map[string]string{"SM_HOST_FS_ROOT": "/host"}), noStat, true},
		{"dockerenv marker", env(nil), statMarker("/.dockerenv"), true},
		{"containerenv marker", env(nil), statMarker("/run/.containerenv"), true},
		{"externally managed alone is not a container signal", env(map[string]string{"SM_EXTERNALLY_MANAGED": "1"}), noStat, false},
		{"host fs root is authoritative", env(map[string]string{"SM_EXTERNALLY_MANAGED": "false", "SM_HOST_FS_ROOT": "/host"}), noStat, true},
		{"nothing set", env(nil), noStat, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectContainer(tc.get, tc.stat); got != tc.want {
				t.Fatalf("DetectContainer = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHostRootFromEnv(t *testing.T) {
	get := func(v string) func(string) string {
		return func(string) string { return v }
	}
	if got := HostRootFromEnv(get("  /host/  ")); got != "/host" {
		t.Fatalf("HostRootFromEnv = %q, want /host", got)
	}
	if got := HostRootFromEnv(get("")); got != "" {
		t.Fatalf("HostRootFromEnv empty = %q, want empty", got)
	}
	if got := HostRootFromEnv(get("/host/..")); got != "/host/.." {
		t.Fatalf("HostRootFromEnv /host/.. = %q, want /host/.. (validateHostRoot then rejects the .. segment)", got)
	}
}

func TestValidateHostRoot(t *testing.T) {
	for _, ok := range []string{"", "/host", "/mnt/host..backup"} {
		if err := validateHostRoot(ok); err != nil {
			t.Fatalf("validateHostRoot(%q) should be allowed: %v", ok, err)
		}
	}
	for _, bad := range []string{"/", "host", "../escape", "/host/..", "/host/../tmp"} {
		if err := validateHostRoot(bad); err == nil {
			t.Fatalf("validateHostRoot(%q) should fail loud", bad)
		}
	}
}

func TestInPlaceTargetContainerized(t *testing.T) {
	if _, err := inPlaceTarget("linux", true); err == nil {
		t.Fatal("expected in-place refusal in a containerized agent")
	}
	if _, err := inPlaceTarget("windows", false); err == nil {
		t.Fatal("expected in-place refusal on Windows")
	}
	target, err := inPlaceTarget("linux", false)
	if err != nil {
		t.Fatalf("host in-place should be allowed: %v", err)
	}
	if target != "/" {
		t.Fatalf("host in-place target = %q, want /", target)
	}
}

type notPresentError struct{}

func (notPresentError) Error() string { return "not present" }

var errNotPresent = notPresentError{}
