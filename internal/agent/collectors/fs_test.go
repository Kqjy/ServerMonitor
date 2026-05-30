package collectors

import "testing"

func TestFSUsagePath(t *testing.T) {
	cases := []struct {
		hostRoot   string
		mountpoint string
		want       string
	}{
		{"", "/", "/"},
		{"", "/mnt/data", "/mnt/data"},
		{"/host", "/", "/host"},
		{"/host", "/mnt/data", "/host/mnt/data"},
		{"/host", "/var/lib", "/host/var/lib"},
	}
	for _, tc := range cases {
		if got := fsUsagePath(tc.hostRoot, tc.mountpoint); got != tc.want {
			t.Errorf("fsUsagePath(%q, %q) = %q, want %q", tc.hostRoot, tc.mountpoint, got, tc.want)
		}
	}
}
