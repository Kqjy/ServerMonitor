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

type fsAggregateEntry struct {
	device string
	fstype string
	total  uint64
	used   uint64
}

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
	aggregateEntries := make([]fsAggregateEntry, 0, len(parts))
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
		if aggregateFSEligible(p.Device, p.Fstype) {
			aggregateEntries = append(aggregateEntries, fsAggregateEntry{device: p.Device, fstype: p.Fstype, total: u.Total, used: u.Used})
		}
	}
	if total, used, ok := aggregateFS(aggregateEntries); ok {
		out = append(out,
			point(now, metrics.FSOverallTotal, nil, float64(total)),
			point(now, metrics.FSOverallUsed, nil, float64(used)),
			point(now, metrics.FSOverallUsedPct, nil, float64(used)/float64(total)*100),
		)
	}
	return out, nil
}

func aggregateFSEligible(device, fstype string) bool {
	t := strings.ToLower(fstype)
	if t == "fuse" || strings.HasPrefix(t, "fuse.") {
		return false
	}
	switch t {
	case "nfs", "nfs3", "nfs4", "cifs", "smbfs", "smb2", "sshfs", "9p", "ceph", "cephfs", "glusterfs", "afs", "davfs", "curlftpfs":
		return false
	}
	return !strings.HasPrefix(device, `\\`) && !strings.HasPrefix(device, "//")
}

func aggregateFS(entries []fsAggregateEntry) (total uint64, used uint64, ok bool) {
	type zfsPool struct {
		used  uint64
		avail uint64
	}
	seenDevices := map[string]struct{}{}
	zfsPools := map[string]zfsPool{}
	for _, entry := range entries {
		if entry.total == 0 || !aggregateFSEligible(entry.device, entry.fstype) {
			continue
		}
		if strings.EqualFold(entry.fstype, "zfs") {
			poolName := entry.device
			if i := strings.IndexByte(poolName, '/'); i >= 0 {
				poolName = poolName[:i]
			}
			pool := zfsPools[poolName]
			pool.used += entry.used
			avail := uint64(0)
			if entry.total > entry.used {
				avail = entry.total - entry.used
			}
			if avail > pool.avail {
				pool.avail = avail
			}
			zfsPools[poolName] = pool
			continue
		}
		if _, exists := seenDevices[entry.device]; exists {
			continue
		}
		seenDevices[entry.device] = struct{}{}
		total += entry.total
		used += entry.used
	}
	for _, pool := range zfsPools {
		used += pool.used
		total += pool.used + pool.avail
	}
	return total, used, total > 0
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
