package collectors

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"servermonitor/pkg/metrics"
	"servermonitor/pkg/wire"
)

func TestSmartReadStateLatch(t *testing.T) {
	c := &smartCollector{}

	if c.updateReadStateLocked(3, 0, 0) {
		t.Fatalf("all-good tick should not warn")
	}
	if c.state != smartStateOK {
		t.Fatalf("state = %q, want %q", c.state, smartStateOK)
	}

	if !c.updateReadStateLocked(0, 2, 2) {
		t.Fatalf("first all-failed tick should warn")
	}
	if c.state != smartStateReadFailed {
		t.Fatalf("state = %q, want %q", c.state, smartStateReadFailed)
	}
	if !strings.Contains(c.stateMsg, "0 of 2") {
		t.Fatalf("read-failed message %q should report the read tally", c.stateMsg)
	}

	if c.updateReadStateLocked(0, 2, 2) {
		t.Fatalf("repeated bad tick must not warn again")
	}

	if c.updateReadStateLocked(2, 0, 0) {
		t.Fatalf("recovery tick should not warn")
	}
	if c.state != smartStateOK || c.warned {
		t.Fatalf("recovery must reset state to ok and clear the warned latch")
	}

	if !c.updateReadStateLocked(1, 1, 0) {
		t.Fatalf("partial failure should warn on the transition")
	}
	if c.state != smartStateReadFailed {
		t.Fatalf("state = %q, want %q", c.state, smartStateReadFailed)
	}
}

func TestSmartCannotRead(t *testing.T) {
	cases := []struct {
		exitStatus int
		want       bool
	}{
		{0, false},
		{1, true},
		{2, true},
		{3, true},
		{8, false},
		{16, false},
		{64, false},
		{8 | 2, true},
	}
	for _, tc := range cases {
		if got := smartCannotRead(tc.exitStatus); got != tc.want {
			t.Errorf("smartCannotRead(%d) = %v, want %v", tc.exitStatus, got, tc.want)
		}
	}
}

func TestIsNVMeDevice(t *testing.T) {
	cases := []struct {
		name     string
		typ      string
		protocol string
		want     bool
	}{
		{"/dev/nvme0", "nvme", "NVMe", true},
		{"/dev/nvme0n1", "", "", true},
		{"/dev/disk0", "", "NVMe", true},
		{"/dev/bus/0", "nvme", "", true},
		{"/dev/sda", "sat", "ATA", false},
		{"/dev/sdb", "scsi", "SCSI", false},
	}
	for _, tc := range cases {
		if got := isNVMeDevice(tc.name, tc.typ, tc.protocol); got != tc.want {
			t.Errorf("isNVMeDevice(%q, %q, %q) = %v, want %v", tc.name, tc.typ, tc.protocol, got, tc.want)
		}
	}
}

func indexPoints(pts []wire.Point) map[metrics.ID]float64 {
	m := make(map[metrics.ID]float64, len(pts))
	for _, p := range pts {
		m[p.Metric] = p.Value
	}
	return m
}

func TestDeviceLabel(t *testing.T) {
	cases := []struct {
		name    string
		devType string
		want    string
	}{
		{"/dev/sda", "sat", "sda"},
		{"/dev/nvme0", "nvme", "nvme0"},
		{"/dev/bus/0", "megaraid,0", "bus/0#0"},
		{"/dev/bus/0", "sat+megaraid,7", "bus/0#7"},
	}
	for _, tc := range cases {
		if got := deviceLabel(tc.name, tc.devType); got != tc.want {
			t.Errorf("deviceLabel(%q, %q) = %q, want %q", tc.name, tc.devType, got, tc.want)
		}
	}
}

func TestSmartPointsNVMe(t *testing.T) {
	const body = `{
		"temperature": {"current": 52},
		"power_on_time": {"hours": 3682},
		"smart_status": {"passed": true},
		"nvme_smart_health_information_log": {
			"data_units_written": 120566935,
			"data_units_read": 218853325,
			"percentage_used": 2,
			"available_spare": 73,
			"media_errors": 0,
			"power_cycles": 81007,
			"unsafe_shutdowns": 80
		}
	}`
	var s smartView
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	m := indexPoints(smartPoints(time.Now(), map[string]string{"device": "nvme0"}, &s))

	if got := m[metrics.SmartDataWrittenBytes]; got != 120566935*nvmeDataUnitBytes {
		t.Errorf("data written = %v, want %v", got, float64(120566935)*nvmeDataUnitBytes)
	}
	if got := m[metrics.SmartDataReadBytes]; got != 218853325*nvmeDataUnitBytes {
		t.Errorf("data read = %v", got)
	}
	if m[metrics.SmartPercentUsed] != 2 {
		t.Errorf("percent used = %v, want 2", m[metrics.SmartPercentUsed])
	}
	if m[metrics.SmartAvailableSpare] != 73 {
		t.Errorf("available spare = %v, want 73", m[metrics.SmartAvailableSpare])
	}
	if m[metrics.SmartPowerCycles] != 81007 {
		t.Errorf("power cycles = %v, want 81007", m[metrics.SmartPowerCycles])
	}
	if m[metrics.SmartUnsafeShutdowns] != 80 {
		t.Errorf("unsafe shutdowns = %v, want 80", m[metrics.SmartUnsafeShutdowns])
	}
	if _, ok := m[metrics.SmartMediaErrors]; !ok {
		t.Errorf("media errors point should be emitted even at 0")
	}
	if _, ok := m[metrics.SmartReallocSectors]; ok {
		t.Errorf("NVMe device should not emit ATA realloc sectors")
	}
	if m[metrics.SmartHealthy] != 1 {
		t.Errorf("healthy = %v, want 1", m[metrics.SmartHealthy])
	}
}

func TestSmartPointsATA(t *testing.T) {
	const body = `{
		"temperature": {"current": 26},
		"power_on_time": {"hours": 39327},
		"smart_status": {"passed": true},
		"ata_smart_attributes": {"table": [
			{"name": "Reallocated_Sector_Ct", "raw": {"value": 0}},
			{"name": "Current_Pending_Sector", "raw": {"value": 0}},
			{"name": "Offline_Uncorrectable", "raw": {"value": 0}},
			{"name": "UDMA_CRC_Error_Count", "raw": {"value": 4}},
			{"name": "Power_Cycle_Count", "raw": {"value": 33}},
			{"name": "Total_LBAs_Written", "raw": {"value": 1000000}}
		]}
	}`
	var s smartView
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	m := indexPoints(smartPoints(time.Now(), map[string]string{"device": "sda"}, &s))

	if got := m[metrics.SmartDataWrittenBytes]; got != 1000000*512 {
		t.Errorf("data written = %v, want %v (default 512-byte sector)", got, 1000000*512)
	}
	if m[metrics.SmartCRCErrors] != 4 {
		t.Errorf("crc errors = %v, want 4", m[metrics.SmartCRCErrors])
	}
	if m[metrics.SmartPowerCycles] != 33 {
		t.Errorf("power cycles = %v, want 33", m[metrics.SmartPowerCycles])
	}
	if _, ok := m[metrics.SmartOfflineUncorrect]; !ok {
		t.Errorf("offline uncorrectable should be emitted")
	}
	if _, ok := m[metrics.SmartPercentUsed]; ok {
		t.Errorf("ATA device should not emit NVMe percent used")
	}
}
