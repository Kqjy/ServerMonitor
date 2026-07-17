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

func TestAggregateFS(t *testing.T) {
	tests := []struct {
		name      string
		entries   []fsAggregateEntry
		wantTotal uint64
		wantUsed  uint64
		wantOK    bool
	}{
		{
			name: "plain multi-device sum",
			entries: []fsAggregateEntry{
				{device: "/dev/sda1", fstype: "ext4", total: 1000, used: 400},
				{device: "/dev/sdb1", fstype: "xfs", total: 3000, used: 1200},
			},
			wantTotal: 4000,
			wantUsed:  1600,
			wantOK:    true,
		},
		{
			name: "same device mounted twice",
			entries: []fsAggregateEntry{
				{device: "/dev/sda1", fstype: "ext4", total: 1000, used: 400},
				{device: "/dev/sda1", fstype: "ext4", total: 1000, used: 450},
			},
			wantTotal: 1000,
			wantUsed:  400,
			wantOK:    true,
		},
		{
			name: "zfs multi-dataset pool",
			entries: []fsAggregateEntry{
				{device: "tank/root", fstype: "zfs", total: 1000, used: 300},
				{device: "tank/data", fstype: "zfs", total: 1200, used: 500},
				{device: "archive", fstype: "zfs", total: 500, used: 100},
			},
			wantTotal: 2000,
			wantUsed:  900,
			wantOK:    true,
		},
		{
			name: "remote and fuse excluded while fuseblk included",
			entries: []fsAggregateEntry{
				{device: "server:/data", fstype: "nfs4", total: 9000, used: 8000},
				{device: "pmxcfs", fstype: "fuse", total: 100, used: 90},
				{device: "remote", fstype: "fuse.sshfs", total: 500, used: 400},
				{device: `\\server\share`, fstype: "ntfs", total: 700, used: 600},
				{device: "/dev/sdc1", fstype: "fuseblk", total: 2000, used: 750},
			},
			wantTotal: 2000,
			wantUsed:  750,
			wantOK:    true,
		},
		{name: "empty result emits nothing"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			total, used, ok := aggregateFS(tc.entries)
			if total != tc.wantTotal || used != tc.wantUsed || ok != tc.wantOK {
				t.Fatalf("aggregateFS() = %d,%d,%v, want %d,%d,%v", total, used, ok, tc.wantTotal, tc.wantUsed, tc.wantOK)
			}
		})
	}
}
