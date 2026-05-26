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
}

type Batch struct {
	Host       HostInfo    `json:"host"`
	Points     []Point     `json:"points"`
	Processes  []Process   `json:"processes,omitempty"`
	Containers []Container `json:"containers,omitempty"`
	Ports      []Port      `json:"ports,omitempty"`
	Sent       time.Time   `json:"sent"`
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
