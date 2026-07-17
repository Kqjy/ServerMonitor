package metrics

type ID int16

const (
	Unknown ID = 0

	CPUUserPct   ID = 100
	CPUSystemPct ID = 101
	CPUIdlePct   ID = 102
	CPUIOWaitPct ID = 103
	CPUStealPct  ID = 104
	CPUTotalPct  ID = 105
	CPUFreqMHz   ID = 106
	CPUCorePct   ID = 107
	LoadAvg1     ID = 110
	LoadAvg5     ID = 111
	LoadAvg15    ID = 112
	UptimeSec    ID = 120

	MemTotal     ID = 200
	MemUsed      ID = 201
	MemFree      ID = 202
	MemAvailable ID = 203
	MemBuffers   ID = 204
	MemCached    ID = 205
	MemUsedPct   ID = 206
	SwapTotal    ID = 210
	SwapUsed     ID = 211
	SwapFree     ID = 212
	SwapUsedPct  ID = 213

	DiskReadBytes    ID = 300
	DiskWriteBytes   ID = 301
	DiskReadOps      ID = 302
	DiskWriteOps     ID = 303
	DiskBusyPct      ID = 304
	FSTotal          ID = 310
	FSUsed           ID = 311
	FSFree           ID = 312
	FSUsedPct        ID = 313
	FSInodesUsed     ID = 314
	FSInodesFree     ID = 315
	FSOverallTotal   ID = 316
	FSOverallUsed    ID = 317
	FSOverallUsedPct ID = 318

	NetRxBytes   ID = 400
	NetTxBytes   ID = 401
	NetRxPackets ID = 402
	NetTxPackets ID = 403
	NetRxErrors  ID = 404
	NetTxErrors  ID = 405
	NetRxDropped ID = 406
	NetTxDropped ID = 407
	ConnEstab    ID = 410
	ConnListen   ID = 411
	ConnTimeWait ID = 412
	PortOpen     ID = 420
	WifiSignal   ID = 430
	WifiBitrate  ID = 431

	ProcCount       ID = 500
	ProcRunning     ID = 501
	ProcSleep       ID = 502
	ProcZombie      ID = 503
	ProcStop        ID = 504
	ProcThreads     ID = 505
	ProcScanSkipped ID = 506

	ContainerCount   ID = 600
	ContainerRunning ID = 601
	ContainerStopped ID = 602

	GPUUsagePct    ID = 700
	GPUMemUsed     ID = 701
	GPUMemTotal    ID = 702
	GPUMemUsedPct  ID = 703
	GPUTempC       ID = 704
	GPUPowerWatts  ID = 705
	GPUFanPct      ID = 706
	GPUClockMHz    ID = 707
	GPUMemClockMHz ID = 708

	SensorTempC      ID = 800
	SensorFanRPM     ID = 801
	BatteryPct       ID = 810
	BatteryChargingW ID = 811

	SmartTempC          ID = 900
	SmartReallocSectors ID = 901
	SmartPendingSectors ID = 902
	SmartPowerOnHours   ID = 903
	SmartHealthy        ID = 904

	SmartDataWrittenBytes ID = 905
	SmartDataReadBytes    ID = 906
	SmartPercentUsed      ID = 907
	SmartAvailableSpare   ID = 908
	SmartMediaErrors      ID = 909
	SmartPowerCycles      ID = 910
	SmartUnsafeShutdowns  ID = 911
	SmartOfflineUncorrect ID = 912
	SmartCRCErrors        ID = 913

	RaidDegraded ID = 950
	RaidSyncPct  ID = 951

	BackupLastSuccessAgeS    ID = 1000
	BackupLastRunOK          ID = 1001
	BackupDurationS          ID = 1002
	BackupAddedBytes         ID = 1003
	BackupTotalBytes         ID = 1004
	BackupSnapshotCount      ID = 1005
	BackupCheckAgeS          ID = 1006
	BackupCheckOK            ID = 1007
	BackupRunning            ID = 1008
	BackupRunElapsedS        ID = 1009
	BackupProgressPct        ID = 1010
	BackupProgressBytes      ID = 1011
	BackupProgressTotalBytes ID = 1012
	BackupAgentStale         ID = 1013
	BackupAgentUnexecutable  ID = 1014
)

type Meta struct {
	Name string
	Unit string
}

var meta = map[ID]Meta{
	CPUUserPct:               {"cpu_user_pct", "%"},
	CPUSystemPct:             {"cpu_system_pct", "%"},
	CPUIdlePct:               {"cpu_idle_pct", "%"},
	CPUIOWaitPct:             {"cpu_iowait_pct", "%"},
	CPUStealPct:              {"cpu_steal_pct", "%"},
	CPUTotalPct:              {"cpu_total_pct", "%"},
	CPUFreqMHz:               {"cpu_freq_mhz", "MHz"},
	CPUCorePct:               {"cpu_core_pct", "%"},
	LoadAvg1:                 {"load_avg_1", ""},
	LoadAvg5:                 {"load_avg_5", ""},
	LoadAvg15:                {"load_avg_15", ""},
	UptimeSec:                {"uptime_sec", "s"},
	MemTotal:                 {"mem_total", "B"},
	MemUsed:                  {"mem_used", "B"},
	MemFree:                  {"mem_free", "B"},
	MemAvailable:             {"mem_available", "B"},
	MemBuffers:               {"mem_buffers", "B"},
	MemCached:                {"mem_cached", "B"},
	MemUsedPct:               {"mem_used_pct", "%"},
	SwapTotal:                {"swap_total", "B"},
	SwapUsed:                 {"swap_used", "B"},
	SwapFree:                 {"swap_free", "B"},
	SwapUsedPct:              {"swap_used_pct", "%"},
	DiskReadBytes:            {"disk_read_bytes", "B"},
	DiskWriteBytes:           {"disk_write_bytes", "B"},
	DiskReadOps:              {"disk_read_ops", "ops"},
	DiskWriteOps:             {"disk_write_ops", "ops"},
	DiskBusyPct:              {"disk_busy_pct", "%"},
	FSTotal:                  {"fs_total", "B"},
	FSUsed:                   {"fs_used", "B"},
	FSFree:                   {"fs_free", "B"},
	FSUsedPct:                {"fs_used_pct", "%"},
	FSInodesUsed:             {"fs_inodes_used", ""},
	FSInodesFree:             {"fs_inodes_free", ""},
	FSOverallTotal:           {"fs_overall_total", "B"},
	FSOverallUsed:            {"fs_overall_used", "B"},
	FSOverallUsedPct:         {"fs_overall_used_pct", "%"},
	NetRxBytes:               {"net_rx_bytes", "B"},
	NetTxBytes:               {"net_tx_bytes", "B"},
	NetRxPackets:             {"net_rx_packets", "pkts"},
	NetTxPackets:             {"net_tx_packets", "pkts"},
	NetRxErrors:              {"net_rx_errors", ""},
	NetTxErrors:              {"net_tx_errors", ""},
	NetRxDropped:             {"net_rx_dropped", ""},
	NetTxDropped:             {"net_tx_dropped", ""},
	ConnEstab:                {"conn_estab", ""},
	ConnListen:               {"conn_listen", ""},
	ConnTimeWait:             {"conn_timewait", ""},
	PortOpen:                 {"port_open", ""},
	WifiSignal:               {"wifi_signal", "dBm"},
	WifiBitrate:              {"wifi_bitrate", "bps"},
	ProcCount:                {"proc_count", ""},
	ProcRunning:              {"proc_running", ""},
	ProcSleep:                {"proc_sleep", ""},
	ProcZombie:               {"proc_zombie", ""},
	ProcStop:                 {"proc_stop", ""},
	ProcThreads:              {"proc_threads", ""},
	ProcScanSkipped:          {"proc_scan_skipped", ""},
	ContainerCount:           {"container_count", ""},
	ContainerRunning:         {"container_running", ""},
	ContainerStopped:         {"container_stopped", ""},
	GPUUsagePct:              {"gpu_usage_pct", "%"},
	GPUMemUsed:               {"gpu_mem_used", "B"},
	GPUMemTotal:              {"gpu_mem_total", "B"},
	GPUMemUsedPct:            {"gpu_mem_used_pct", "%"},
	GPUTempC:                 {"gpu_temp_c", "C"},
	GPUPowerWatts:            {"gpu_power_w", "W"},
	GPUFanPct:                {"gpu_fan_pct", "%"},
	GPUClockMHz:              {"gpu_clock_mhz", "MHz"},
	GPUMemClockMHz:           {"gpu_mem_clock_mhz", "MHz"},
	SensorTempC:              {"sensor_temp_c", "C"},
	SensorFanRPM:             {"sensor_fan_rpm", "rpm"},
	BatteryPct:               {"battery_pct", "%"},
	BatteryChargingW:         {"battery_charging_w", "W"},
	SmartTempC:               {"smart_temp_c", "C"},
	SmartReallocSectors:      {"smart_realloc_sectors", ""},
	SmartPendingSectors:      {"smart_pending_sectors", ""},
	SmartPowerOnHours:        {"smart_power_on_hours", "h"},
	SmartHealthy:             {"smart_healthy", ""},
	SmartDataWrittenBytes:    {"smart_data_written_bytes", "B"},
	SmartDataReadBytes:       {"smart_data_read_bytes", "B"},
	SmartPercentUsed:         {"smart_percent_used", "%"},
	SmartAvailableSpare:      {"smart_available_spare", "%"},
	SmartMediaErrors:         {"smart_media_errors", ""},
	SmartPowerCycles:         {"smart_power_cycles", ""},
	SmartUnsafeShutdowns:     {"smart_unsafe_shutdowns", ""},
	SmartOfflineUncorrect:    {"smart_offline_uncorrectable", ""},
	SmartCRCErrors:           {"smart_crc_errors", ""},
	RaidDegraded:             {"raid_degraded", ""},
	RaidSyncPct:              {"raid_sync_pct", "%"},
	BackupLastSuccessAgeS:    {"backup_last_success_age_s", "s"},
	BackupLastRunOK:          {"backup_last_run_ok", "bool"},
	BackupDurationS:          {"backup_duration_s", "s"},
	BackupAddedBytes:         {"backup_added_bytes", "B"},
	BackupTotalBytes:         {"backup_total_bytes", "B"},
	BackupSnapshotCount:      {"backup_snapshot_count", "count"},
	BackupCheckAgeS:          {"backup_check_age_s", "s"},
	BackupCheckOK:            {"backup_check_ok", "bool"},
	BackupRunning:            {"backup_running", "bool"},
	BackupRunElapsedS:        {"backup_run_elapsed_s", "s"},
	BackupProgressPct:        {"backup_progress_pct", "%"},
	BackupProgressBytes:      {"backup_progress_bytes", "B"},
	BackupProgressTotalBytes: {"backup_progress_total_bytes", "B"},
	BackupAgentStale:         {"backup_agent_stale", "bool"},
	BackupAgentUnexecutable:  {"backup_agent_unexecutable", "bool"},
}

func (id ID) Meta() Meta {
	if m, ok := meta[id]; ok {
		return m
	}
	return Meta{Name: "unknown", Unit: ""}
}

func ByName(name string) (ID, bool) {
	for id, m := range meta {
		if m.Name == name {
			return id, true
		}
	}
	return Unknown, false
}

func All() map[ID]Meta {
	out := make(map[ID]Meta, len(meta))
	for k, v := range meta {
		out[k] = v
	}
	return out
}
