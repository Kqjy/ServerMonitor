package collectors

import (
	"strings"
	"testing"
)

func TestSkipFSType(t *testing.T) {
	skip := []string{
		"tmpfs", "devtmpfs", "devpts", "overlay", "squashfs", "proc", "sysfs",
		"cgroup2", "pstore", "binfmt_misc", "debugfs", "securityfs", "selinuxfs",
		"tracefs", "configfs", "fusectl", "mqueue", "nsfs", "ramfs", "rootfs",
		"efivarfs", "bpf", "autofs",
	}
	for _, fs := range skip {
		if !skipFSType(fs) {
			t.Errorf("skipFSType(%q) = false, want true", fs)
		}
		if u := strings.ToUpper(fs); !skipFSType(u) {
			t.Errorf("skipFSType(%q) = false, want true", u)
		}
	}
	keep := []string{"ext4", "ext3", "xfs", "btrfs", "zfs", "vfat", "ntfs", "fuse", "fuseblk", "nfs", "nfs4", "cifs"}
	for _, fs := range keep {
		if skipFSType(fs) {
			t.Errorf("skipFSType(%q) = true, want false", fs)
		}
	}
}

func TestIsBindMount(t *testing.T) {
	bind := [][]string{
		{"rw", "relatime", "bind"},
		{"bind"},
		{"ro", "nosuid", "nodev", "bind"},
	}
	for _, opts := range bind {
		if !isBindMount(opts) {
			t.Errorf("isBindMount(%v) = false, want true", opts)
		}
	}
	plain := [][]string{
		{"rw", "relatime"},
		{},
		{"ro", "nosuid", "nodev"},
	}
	for _, opts := range plain {
		if isBindMount(opts) {
			t.Errorf("isBindMount(%v) = true, want false", opts)
		}
	}
}

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
