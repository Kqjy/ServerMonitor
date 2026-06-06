package collectors

import (
	"context"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/disk"

	"servermonitor/pkg/metrics"
	"servermonitor/pkg/wire"
)

type fsCollector struct{ hostRoot string }

func init() { Register(&fsCollector{hostRoot: os.Getenv("SM_HOST_FS_ROOT")}) }

func (c *fsCollector) Name() string        { return "fs" }
func (c *fsCollector) Platforms() []string { return []string{"all"} }

func (c *fsCollector) Collect(ctx context.Context) ([]wire.Point, error) {
	now := time.Now()
	parts, err := disk.PartitionsWithContext(ctx, true)
	if err != nil {
		return nil, err
	}
	out := make([]wire.Point, 0, len(parts)*5)
	for _, p := range parts {
		if skipFSType(p.Fstype) || isBindMount(p.Opts) {
			continue
		}
		u, err := disk.UsageWithContext(ctx, fsUsagePath(c.hostRoot, p.Mountpoint))
		if err != nil || u.Total == 0 {
			continue
		}
		labels := map[string]string{"mount": p.Mountpoint, "fstype": p.Fstype}
		out = append(out,
			point(now, metrics.FSTotal, labels, float64(u.Total)),
			point(now, metrics.FSUsed, labels, float64(u.Used)),
			point(now, metrics.FSFree, labels, float64(u.Free)),
			point(now, metrics.FSUsedPct, labels, u.UsedPercent),
		)
		if u.InodesTotal > 0 {
			out = append(out,
				point(now, metrics.FSInodesUsed, labels, float64(u.InodesUsed)),
				point(now, metrics.FSInodesFree, labels, float64(u.InodesFree)),
			)
		}
	}
	return out, nil
}

func skipFSType(t string) bool {
	t = strings.ToLower(t)
	switch t {
	case "tmpfs", "devtmpfs", "devfs", "devpts", "overlay", "squashfs", "iso9660", "autofs",
		"proc", "sysfs", "cgroup", "cgroup2", "pstore", "binfmt_misc", "debugfs",
		"securityfs", "selinuxfs", "tracefs", "configfs", "fusectl", "hugetlbfs", "mqueue",
		"nsfs", "ramfs", "rootfs", "efivarfs", "rpc_pipefs", "bpf":
		return true
	}
	return false
}

func isBindMount(opts []string) bool {
	return slices.Contains(opts, "bind")
}

func fsUsagePath(hostRoot, mountpoint string) string {
	if hostRoot == "" {
		return mountpoint
	}
	return path.Join(hostRoot, mountpoint)
}
