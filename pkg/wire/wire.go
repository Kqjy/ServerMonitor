package wire

import (
	"time"

	"servermonitor/pkg/metrics"
)

type Point struct {
	Time   time.Time         `json:"t"`
	Metric metrics.ID        `json:"m"`
	Labels map[string]string `json:"l,omitempty"`
	Value  float64           `json:"v"`
}

type Process struct {
	Time     time.Time `json:"t"`
	PID      int32     `json:"pid"`
	Name     string    `json:"name"`
	User     string    `json:"user,omitempty"`
	Cmdline  string    `json:"cmd,omitempty"`
	CPUPct   float32   `json:"cpu_pct"`
	MemRSS   int64     `json:"mem_rss"`
	Status   string    `json:"status,omitempty"`
	NThreads int32     `json:"nthreads,omitempty"`
}

type Container struct {
	Time     time.Time `json:"t"`
	ID       string    `json:"cid"`
	Name     string    `json:"name"`
	Image    string    `json:"image,omitempty"`
	State    string    `json:"state"`
	CPUPct   float32   `json:"cpu_pct"`
	MemUsed  int64     `json:"mem_used"`
	MemLimit int64     `json:"mem_limit,omitempty"`
	RxBytes  int64     `json:"rx_bytes,omitempty"`
	TxBytes  int64     `json:"tx_bytes,omitempty"`
}

type Port struct {
	Time    time.Time `json:"t"`
	Proto   string    `json:"proto"`
	Addr    string    `json:"addr"`
	Port    uint16    `json:"port"`
	PID     int32     `json:"pid,omitempty"`
	Process string    `json:"process,omitempty"`
}

type BackupSnapshot struct {
	ID    string    `json:"id"`
	Time  time.Time `json:"time,omitempty"`
	Paths []string  `json:"paths,omitempty"`
}

type BackupRepoStatus struct {
	Name          string           `json:"name"`
	Engine        string           `json:"engine,omitempty"`
	LastStarted   *time.Time       `json:"last_started,omitempty"`
	LastFinished  *time.Time       `json:"last_finished,omitempty"`
	LastSuccess   *time.Time       `json:"last_success,omitempty"`
	Success       bool             `json:"success"`
	Error         string           `json:"error,omitempty"`
	DurationS     int64            `json:"duration_s,omitempty"`
	AddedBytes    int64            `json:"added_bytes,omitempty"`
	TotalBytes    int64            `json:"total_bytes,omitempty"`
	SnapshotCount int64            `json:"snapshot_count,omitempty"`
	CheckLast     *time.Time       `json:"check_last,omitempty"`
	CheckSuccess  *bool            `json:"check_success,omitempty"`
	Tunnel        bool             `json:"tunnel,omitempty"`
	Snapshots     []BackupSnapshot `json:"snapshots,omitempty"`
}

type TunnelEnrollRequest struct {
	PublicKey string `json:"public_key"`
}

type TunnelEnrollResponse struct {
	ServerPublicKey string `json:"server_public_key"`
	Endpoint        string `json:"endpoint"`
	TunnelIP        string `json:"tunnel_ip"`
	ServerTunnelIP  string `json:"server_tunnel_ip"`
	RestPort        int    `json:"rest_port"`
	ServerStorage   bool   `json:"server_storage"`
}

type TunnelNodeInfo struct {
	HostID    int64  `json:"host_id"`
	Hostname  string `json:"hostname"`
	PublicKey string `json:"public_key"`
	Endpoint  string `json:"endpoint"`
	TunnelIP  string `json:"tunnel_ip"`
	RestPort  int    `json:"rest_port"`
}

type TunnelNodesResponse struct {
	Nodes []TunnelNodeInfo `json:"nodes"`
}

type NodeTargetInfo struct {
	Name       string `json:"name"`
	SecretHash string `json:"secret_hash"`
	QuotaBytes int64  `json:"quota_bytes,omitempty"`
	Revoked    bool   `json:"revoked,omitempty"`
}

type NodePeerInfo struct {
	PublicKey string `json:"public_key"`
	TunnelIP  string `json:"tunnel_ip"`
}

type BackupNodeConfig struct {
	Enabled      bool             `json:"enabled"`
	UDPPort      int              `json:"udp_port,omitempty"`
	StoreDir     string           `json:"store_dir,omitempty"`
	MaxBlobBytes int64            `json:"max_blob_bytes,omitempty"`
	TunnelIP     string           `json:"tunnel_ip,omitempty"`
	RestPort     int              `json:"rest_port,omitempty"`
	Targets      []NodeTargetInfo `json:"targets,omitempty"`
	Peers        []NodePeerInfo   `json:"peers,omitempty"`
}

type NodeUsageEntry struct {
	Name      string `json:"name"`
	UsedBytes int64  `json:"used_bytes"`
}

type BackupNodeUsage struct {
	Targets []NodeUsageEntry `json:"targets"`
}

type CollectorStatus struct {
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
}

type HostInfo struct {
	Hostname        string                     `json:"hostname"`
	OS              string                     `json:"os"`
	Arch            string                     `json:"arch"`
	Kernel          string                     `json:"kernel,omitempty"`
	AgentVersion    string                     `json:"agent_version"`
	Collectors      []string                   `json:"collectors,omitempty"`
	CollectorStatus map[string]CollectorStatus `json:"collector_status,omitempty"`
	Tags            map[string]string          `json:"tags,omitempty"`

	ExternallyManaged *bool `json:"externally_managed,omitempty"`
}

type Batch struct {
	Host       HostInfo           `json:"host"`
	Points     []Point            `json:"points"`
	Processes  []Process          `json:"processes,omitempty"`
	Containers []Container        `json:"containers,omitempty"`
	Ports      []Port             `json:"ports,omitempty"`
	Backups    []BackupRepoStatus `json:"backups,omitempty"`
	Sent       time.Time          `json:"sent"`
}

type IngestAck struct {
	Accepted           int    `json:"accepted"`
	HostID             int64  `json:"host_id"`
	Message            string `json:"message,omitempty"`
	IntervalS          int    `json:"interval_s,omitempty"`
	LatestAgentVersion string `json:"latest_agent_version,omitempty"`
	AutoUpgrade        *bool  `json:"auto_upgrade,omitempty"`
	UpgradeNow         bool   `json:"upgrade_now,omitempty"`
	ServerPubkey       string `json:"server_pubkey,omitempty"`
}

type RegisterRequest struct {
	Hostname string `json:"hostname"`
	Token    string `json:"token"`
}

type RegisterResponse struct {
	HostID int64 `json:"host_id"`
}

type AlertEvent struct {
	Kind     string    `json:"kind"`
	HostID   int64     `json:"host_id"`
	RuleID   int32     `json:"rule_id"`
	RuleName string    `json:"rule_name"`
	Severity string    `json:"severity"`
	Metric   string    `json:"metric"`
	Value    float64   `json:"value"`
	Time     time.Time `json:"time"`
}
