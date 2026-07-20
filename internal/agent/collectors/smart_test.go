package collectors

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
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

func TestSmartReadArgs(t *testing.T) {
	cases := []struct {
		dev  smartDevice
		want string
	}{
		{smartDevice{name: "/dev/sda", devType: "scsi"}, "-a --json=c -l devstat /dev/sda"},
		{smartDevice{name: "/dev/sda", devType: "ata"}, "-a --json=c -l devstat /dev/sda"},
		{smartDevice{name: "/dev/sda", devType: ""}, "-a --json=c -l devstat /dev/sda"},
		{smartDevice{name: "/dev/sda", devType: "sat"}, "-a --json=c -l devstat -d sat /dev/sda"},
		{smartDevice{name: "/dev/nvme0", devType: "nvme"}, "-a --json=c -d nvme /dev/nvme0"},
		{smartDevice{name: "/dev/sdb", devType: "nvme"}, "-a --json=c -d nvme /dev/sdb"},
		{smartDevice{name: "/dev/bus/0", devType: "megaraid,0"}, "-a --json=c -l devstat -d megaraid,0 /dev/bus/0"},
		{smartDevice{name: "/dev/bus/0", devType: "sat+megaraid,7"}, "-a --json=c -l devstat -d sat+megaraid,7 /dev/bus/0"},
	}
	for _, tc := range cases {
		if got := strings.Join(smartReadArgs(tc.dev), " "); got != tc.want {
			t.Errorf("smartReadArgs(%+v) = %q, want %q", tc.dev, got, tc.want)
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

type fakeSmartctl struct {
	mu        sync.Mutex
	responses map[string]string
	fallback  string
	calls     []string
}

func (f *fakeSmartctl) exec(_ context.Context, _ string, args ...string) ([]byte, error) {
	key := strings.Join(args, " ")
	f.mu.Lock()
	f.calls = append(f.calls, key)
	f.mu.Unlock()
	if r, ok := f.responses[key]; ok {
		return []byte(r), nil
	}
	if f.fallback != "" {
		return []byte(f.fallback), nil
	}
	return []byte(`{"smartctl":{"exit_status":2}}`), nil
}

func (f *fakeSmartctl) called(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == key {
			return true
		}
	}
	return false
}

const (
	fakeVDIdentity = `{"smartctl":{"exit_status":0},"vendor":"AVAGO","product":"SMC3108","smart_support":{"available":false},"device_type":{"scsi_value":0,"name":"disk"}}`
	fakeSASDisk8   = `{"smartctl":{"exit_status":0},"model_name":"ST4000NM0023","serial_number":"Z1Z0AAAA","smart_support":{"available":true},"device_type":{"scsi_value":0,"name":"disk"}}`
	fakeSASDisk9   = `{"smartctl":{"exit_status":0},"model_name":"ST4000NM0023","serial_number":"Z1Z0BBBB","smart_support":{"available":true},"device_type":{"scsi_value":0,"name":"disk"}}`
	fakeEnclosure  = `{"smartctl":{"exit_status":0},"vendor":"LSI","product":"SAS2X28","device_type":{"scsi_value":13,"name":"enclosure"}}`
)

func newRAIDTestCollector(f *fakeSmartctl) *smartCollector {
	return &smartCollector{
		probed:        true,
		smartctl:      "/fake/smartctl",
		storcliProbed: true,
		goos:          "linux",
		devicesTTL:    time.Hour,
		execFn:        f.exec,
		fileExists:    func(string) bool { return false },
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func collectSmartSample(t *testing.T, c *smartCollector) []wire.Point {
	t.Helper()
	if _, err := c.Collect(context.Background()); err != nil {
		t.Fatalf("start SMART sample: %v", err)
	}
	waitFor(t, "SMART sample", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.pendingSample != nil
	})
	points, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect SMART sample: %v", err)
	}
	return points
}

func TestSetSMARTSampleIntervalClamp(t *testing.T) {
	defaultSmartCollector.mu.Lock()
	original := defaultSmartCollector.sampleInterval
	defaultSmartCollector.mu.Unlock()
	defer func() {
		defaultSmartCollector.mu.Lock()
		defaultSmartCollector.sampleInterval = original
		defaultSmartCollector.mu.Unlock()
	}()
	for _, tc := range []struct {
		input time.Duration
		want  time.Duration
	}{
		{0, smartSampleDefault},
		{30 * time.Second, smartSampleMin},
		{2 * time.Hour, smartSampleMax},
		{10 * time.Minute, 10 * time.Minute},
	} {
		SetSMARTSampleInterval(tc.input)
		defaultSmartCollector.mu.Lock()
		got := defaultSmartCollector.sampleInterval
		defaultSmartCollector.mu.Unlock()
		if got != tc.want {
			t.Fatalf("SetSMARTSampleInterval(%v) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestCollectDoesNotRepeatSMARTReadsBetweenSamples(t *testing.T) {
	f := &fakeSmartctl{fallback: `{"smartctl":{"exit_status":0},"temperature":{"current":31},"smart_status":{"passed":true}}`}
	c := &smartCollector{
		probed:         true,
		smartctl:       "/fake/smartctl",
		storcli:        "/fake/storcli",
		storcliProbed:  true,
		goos:           "darwin",
		scanned:        []smartDevice{{name: "/dev/sda", devType: "ata"}, {name: "/dev/sdb", devType: "ata"}},
		cliDrives:      []storcliDrive{{ctl: 0, did: 8, cliOnly: true}},
		cliAt:          time.Now().Add(-time.Hour),
		devicesAt:      time.Now(),
		devicesTTL:     time.Hour,
		sampleInterval: time.Hour,
		execFn:         f.exec,
	}
	for range 20 {
		if _, err := c.Collect(context.Background()); err != nil {
			t.Fatalf("Collect: %v", err)
		}
	}
	waitFor(t, "SMART sample completion", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.pendingSample != nil
	})
	for range 20 {
		if _, err := c.Collect(context.Background()); err != nil {
			t.Fatalf("Collect: %v", err)
		}
	}
	waitFor(t, "storcli refresh completion", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return !c.cliBusy
	})
	f.mu.Lock()
	defer f.mu.Unlock()
	reads := 0
	storcliEnumerations := 0
	for _, call := range f.calls {
		if strings.HasPrefix(call, "-a ") {
			reads++
		}
		if call == "show ctrlcount J" {
			storcliEnumerations++
		}
	}
	if reads != len(c.scanned) {
		t.Fatalf("SMART read calls = %d, want one per device (%d): %v", reads, len(c.scanned), f.calls)
	}
	if storcliEnumerations > 1 {
		t.Fatalf("storcli enumerations = %d, want at most 1: %v", storcliEnumerations, f.calls)
	}
}

func TestRAIDFamiliesFor(t *testing.T) {
	unavail := &smartView{SmartSupport: &struct {
		Available bool `json:"available"`
	}{false}}
	avail := &smartView{SmartSupport: &struct {
		Available bool `json:"available"`
	}{true}}
	vd := &smartView{Vendor: "AVAGO", Product: "SMC3108", SmartSupport: unavail.SmartSupport}
	hp := &smartView{Vendor: "HP", Product: "LOGICAL VOLUME", SmartSupport: unavail.SmartSupport}
	adaptec := &smartView{Vendor: "Adaptec", Product: "ASR8805", SmartSupport: unavail.SmartSupport}

	cases := []struct {
		name         string
		identity     *smartView
		opened       bool
		goos         string
		want         []string
		wantExplicit bool
	}{
		{"healthy sas disk", avail, true, "linux", nil, false},
		{"unopened device", nil, false, "linux", nil, false},
		{"megaraid vd", vd, true, "linux", []string{"megaraid", "cciss"}, true},
		{"hp logical volume", hp, true, "linux", []string{"cciss"}, true},
		{"adaptec", adaptec, true, "linux", []string{"aacraid"}, true},
		{"unknown no-smart", unavail, true, "linux", []string{"megaraid", "cciss"}, false},
		{"unknown no-smart windows", unavail, true, "windows", []string{"megaraid"}, false},
		{"hp on windows", hp, true, "windows", nil, true},
	}
	for _, tc := range cases {
		got, explicit := raidFamiliesFor(tc.identity, tc.opened, tc.goos)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") || explicit != tc.wantExplicit {
			t.Errorf("%s: raidFamiliesFor = (%v, %v), want (%v, %v)", tc.name, got, explicit, tc.want, tc.wantExplicit)
		}
	}
}

func TestRAIDCandidates(t *testing.T) {
	scanned := []smartDevice{
		{name: "/dev/nvme0", devType: "nvme", isNVMe: true},
		{name: "/dev/sda", devType: "scsi"},
		{name: "/dev/sdb", devType: "sat"},
		{name: "/dev/sdc", devType: ""},
	}
	got := raidCandidates(scanned)
	if len(got) != 2 || got[0].name != "/dev/sda" || got[1].name != "/dev/sdc" {
		t.Fatalf("raidCandidates = %+v, want sda and sdc only", got)
	}
}

func TestPassthroughFamilies(t *testing.T) {
	fams := passthroughFamilies([]smartDevice{
		{name: "/dev/bus/0", devType: "megaraid,4"},
		{name: "/dev/sda", devType: "scsi"},
	})
	if !fams["megaraid"] || len(fams) != 1 {
		t.Fatalf("passthroughFamilies = %v, want megaraid only", fams)
	}
}

func TestWalkIDsMissStreakAndDedupe(t *testing.T) {
	f := &fakeSmartctl{responses: map[string]string{
		"-i --json=c -d megaraid,8 /dev/sda":  fakeSASDisk8,
		"-i --json=c -d megaraid,9 /dev/sda":  fakeSASDisk9,
		"-i --json=c -d megaraid,11 /dev/sda": fakeEnclosure,
	}}
	c := newRAIDTestCollector(f)
	serials := map[string]bool{}
	devs := map[string]bool{}
	got := c.walkIDs(context.Background(), c.smartctl, "/dev/sda", 0, 63, 16, serials, devs, func(i int) string {
		return "megaraid," + strconv.Itoa(i)
	}, nil)
	if len(got) != 2 {
		t.Fatalf("walkIDs found %d devices, want 2 (enclosure filtered): %+v", len(got), got)
	}
	if got[0].devType != "megaraid,8" || got[1].devType != "megaraid,9" {
		t.Fatalf("walkIDs devices = %+v", got)
	}
	if f.called("-i --json=c -d megaraid,40 /dev/sda") {
		t.Fatalf("walkIDs should stop after 16 consecutive misses, probed id 40")
	}

	f2 := &fakeSmartctl{responses: map[string]string{
		"-i --json=c -d megaraid,8 /dev/sdb": fakeSASDisk8,
	}}
	c2 := newRAIDTestCollector(f2)
	got2 := c2.walkIDs(context.Background(), c2.smartctl, "/dev/sdb", 0, 63, 16, serials, devs, func(i int) string {
		return "megaraid," + strconv.Itoa(i)
	}, nil)
	if len(got2) != 0 {
		t.Fatalf("walkIDs must skip serials already claimed by another node, got %+v", got2)
	}
}

func TestProbeRAIDPassthroughNoDrivesSetsNote(t *testing.T) {
	f := &fakeSmartctl{responses: map[string]string{
		"-i --json=c /dev/sda": fakeVDIdentity,
	}}
	c := newRAIDTestCollector(f)
	found, hide, note, _, _ := c.probeRAIDPassthrough(context.Background(), c.smartctl, "linux",
		[]smartDevice{{name: "/dev/sda", devType: "scsi"}}, false,
		[]smartDevice{{name: "/dev/sda", devType: "scsi"}})
	if len(found) != 0 || len(hide) != 0 {
		t.Fatalf("expected nothing found, got %+v hide %v", found, hide)
	}
	if !strings.Contains(note, "SMC3108") || !strings.Contains(note, "sda") {
		t.Fatalf("note should name the controller and device, got %q", note)
	}
}

func TestProbeRAIDPassthroughSkipsScannedFamily(t *testing.T) {
	f := &fakeSmartctl{responses: map[string]string{
		"-i --json=c /dev/sda": fakeVDIdentity,
	}}
	c := newRAIDTestCollector(f)
	scanned := []smartDevice{
		{name: "/dev/sda", devType: "scsi"},
		{name: "/dev/bus/0", devType: "megaraid,8"},
	}
	found, _, note, _, _ := c.probeRAIDPassthrough(context.Background(), c.smartctl, "linux",
		[]smartDevice{{name: "/dev/sda", devType: "scsi"}}, false, scanned)
	if len(found) != 0 {
		t.Fatalf("expected no probed drives when scan already enumerated megaraid, got %+v", found)
	}
	for _, call := range f.calls {
		if strings.Contains(call, "-d megaraid,") {
			t.Fatalf("must not probe megaraid ids when scan already lists them, probed %q", call)
		}
	}
	if note != "" {
		t.Fatalf("note should stay empty when only skipped families remain unprobed, got %q", note)
	}
}

func TestCollectMegaRAIDEndToEnd(t *testing.T) {
	fullRead8 := `{"smartctl":{"exit_status":0},"model_name":"ST4000NM0023","serial_number":"Z1Z0AAAA",
		"temperature":{"current":34},"power_on_time":{"hours":41000},"smart_status":{"passed":true},
		"ata_smart_attributes":{"table":[{"name":"Reallocated_Sector_Ct","raw":{"value":0}}]}}`
	fullRead9 := `{"smartctl":{"exit_status":0},"model_name":"ST4000NM0023","serial_number":"Z1Z0BBBB",
		"temperature":{"current":36},"power_on_time":{"hours":41002},"smart_status":{"passed":true},
		"ata_smart_attributes":{"table":[{"name":"Reallocated_Sector_Ct","raw":{"value":3}}]}}`
	f := &fakeSmartctl{responses: map[string]string{
		"--scan --json=c":                               `{"devices":[{"name":"/dev/sda","type":"scsi","protocol":"SCSI"}]}`,
		"-i --json=c /dev/sda":                          fakeVDIdentity,
		"-i --json=c -d megaraid,8 /dev/sda":            fakeSASDisk8,
		"-i --json=c -d megaraid,9 /dev/sda":            fakeSASDisk9,
		"-a --json=c -l devstat /dev/sda":               `{"smartctl":{"exit_status":4}}`,
		"-a --json=c -l devstat -d megaraid,8 /dev/sda": fullRead8,
		"-a --json=c -l devstat -d megaraid,9 /dev/sda": fullRead9,
	}}
	c := newRAIDTestCollector(f)
	if !c.ensure(context.Background()) {
		t.Fatalf("ensure should succeed with the scanned virtual disk")
	}
	waitFor(t, "raid discovery", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return !c.raidBusy && len(c.raidExtra) == 2
	})
	c.mu.Lock()
	devs := c.devicesLocked()
	c.mu.Unlock()
	if len(devs) != 2 {
		t.Fatalf("virtual disk must be hidden once members are found, devices = %+v", devs)
	}
	pts := collectSmartSample(t, c)
	byDev := map[string]map[metrics.ID]float64{}
	models := map[string]string{}
	for _, p := range pts {
		d := p.Labels["device"]
		if byDev[d] == nil {
			byDev[d] = map[metrics.ID]float64{}
		}
		byDev[d][p.Metric] = p.Value
		models[d] = p.Labels["model"]
	}
	if len(byDev) != 2 {
		t.Fatalf("points for %d devices, want 2: %v", len(byDev), byDev)
	}
	if byDev["sda#8"][metrics.SmartTempC] != 34 || byDev["sda#9"][metrics.SmartTempC] != 36 {
		t.Fatalf("per-drive temps wrong: %v", byDev)
	}
	if byDev["sda#9"][metrics.SmartReallocSectors] != 3 {
		t.Fatalf("sda#9 realloc = %v, want 3", byDev["sda#9"][metrics.SmartReallocSectors])
	}
	if models["sda#8"] != "ST4000NM0023" {
		t.Fatalf("model label = %q, want ST4000NM0023", models["sda#8"])
	}
	if c.Status().State != smartStateOK {
		t.Fatalf("state = %q, want ok", c.Status().State)
	}
}

func TestCollectRAIDNoteSurfacesState(t *testing.T) {
	f := &fakeSmartctl{responses: map[string]string{
		"--scan --json=c":                 `{"devices":[{"name":"/dev/sda","type":"scsi","protocol":"SCSI"}]}`,
		"-i --json=c /dev/sda":            fakeVDIdentity,
		"-a --json=c -l devstat /dev/sda": `{"smartctl":{"exit_status":4}}`,
	}}
	c := newRAIDTestCollector(f)
	if !c.ensure(context.Background()) {
		t.Fatalf("ensure should succeed")
	}
	waitFor(t, "raid discovery", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return !c.raidBusy && c.raidNote != ""
	})
	collectSmartSample(t, c)
	st := c.Status()
	if st.State != smartStateRAIDHidden {
		t.Fatalf("state = %q, want %q", st.State, smartStateRAIDHidden)
	}
	if !strings.Contains(st.Message, "SMC3108") {
		t.Fatalf("message should carry the controller model, got %q", st.Message)
	}
}

func TestUpdateReadStateRAIDNote(t *testing.T) {
	c := &smartCollector{raidNote: "hardware RAID virtual disk sda (SMC3108) hides its member drives"}
	if !c.updateReadStateLocked(2, 0, 0) {
		t.Fatalf("raid note with clean reads should warn once")
	}
	if c.state != smartStateRAIDHidden {
		t.Fatalf("state = %q, want %q", c.state, smartStateRAIDHidden)
	}
	if c.updateReadStateLocked(2, 0, 0) {
		t.Fatalf("repeated raid-note tick must not warn again")
	}
	if !strings.Contains(c.stateMsg, "SMC3108") {
		t.Fatalf("stateMsg = %q", c.stateMsg)
	}
	if c.updateReadStateLocked(1, 1, 0) {
		t.Fatalf("warn latch already used")
	}
	if c.state != smartStateReadFailed || !strings.Contains(c.stateMsg, "SMC3108") {
		t.Fatalf("read failures should take precedence but keep the raid note, got %q %q", c.state, c.stateMsg)
	}
	c.raidNote = ""
	if c.updateReadStateLocked(2, 0, 0) {
		t.Fatalf("recovery should not warn")
	}
	if c.state != smartStateOK || c.warned {
		t.Fatalf("recovery must reset to ok")
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

func TestSmartPointsATADeviceStatistics(t *testing.T) {
	const body = `{
		"logical_block_size": 512,
		"ata_device_statistics": {"pages": [{"number": 7, "name": "Solid State Device Statistics", "table": [
			{"name": "Logical Sectors Written", "value": 1200},
			{"name": "Logical Sectors Read", "value": 3400},
			{"name": "Number of Reported Uncorrectable Errors", "value": 5}
		]}]}
	}`
	var s smartView
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	m := indexPoints(smartPoints(time.Now(), map[string]string{"device": "sda"}, &s))
	if m[metrics.SmartDataWrittenBytes] != 1200*512 || m[metrics.SmartDataReadBytes] != 3400*512 || m[metrics.SmartMediaErrors] != 5 {
		t.Fatalf("device statistics points = %#v", m)
	}
}

func TestSmartPointsATADeviceStatisticsWins(t *testing.T) {
	const body = `{
		"logical_block_size": 512,
		"ata_smart_attributes": {"table": [{"name": "Total_LBAs_Written", "raw": {"value": 99}}]},
		"ata_device_statistics": {"pages": [{"number": 7, "name": "Device Statistics", "table": [
			{"name": "Logical Sectors Written", "value": 1234}
		]}]}
	}`
	var s smartView
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	points := smartPoints(time.Now(), map[string]string{"device": "sda"}, &s)
	m := indexPoints(points)
	if m[metrics.SmartDataWrittenBytes] != 1234*512 {
		t.Fatalf("data written = %v, want %v", m[metrics.SmartDataWrittenBytes], 1234*512)
	}
	count := 0
	for _, p := range points {
		if p.Metric == metrics.SmartDataWrittenBytes {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("written point count = %d, want 1", count)
	}
}

func TestSmartPointsATADeviceStatistics4Kn(t *testing.T) {
	const body = `{
		"logical_block_size": 4096,
		"ata_device_statistics": {"pages": [{"number": 7, "name": "Device Statistics", "table": [
			{"name": "Logical Sectors Written", "value": 11},
			{"name": "Logical Sectors Read", "value": 17}
		]}]}
	}`
	var s smartView
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	m := indexPoints(smartPoints(time.Now(), map[string]string{"device": "sda"}, &s))
	if m[metrics.SmartDataWrittenBytes] != 11*4096 || m[metrics.SmartDataReadBytes] != 17*4096 {
		t.Fatalf("4Kn device statistics points = %#v", m)
	}
}

func TestSmartPointsSATAPercentUsed(t *testing.T) {
	const body = `{
		"ata_device_statistics": {"pages": [{"number": 7, "name": "Solid State Device Statistics", "table": [
			{"name": "Percentage Used Endurance Indicator", "value": 37}
		]}]}
	}`
	var s smartView
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	m := indexPoints(smartPoints(time.Now(), map[string]string{"device": "sda"}, &s))
	if m[metrics.SmartPercentUsed] != 37 {
		t.Fatalf("percent used = %v, want 37", m[metrics.SmartPercentUsed])
	}
}

func TestSmartPointsSCSI(t *testing.T) {
	const body = `{
		"scsi_grown_defect_list": 14,
		"scsi_error_counter_log": {
			"read": {"gigabytes_processed": "123.5", "total_uncorrected_errors": 2},
			"write": {"gigabytes_processed": "45.25", "total_uncorrected_errors": 3}
		}
	}`
	var s smartView
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	m := indexPoints(smartPoints(time.Now(), map[string]string{"device": "sda"}, &s))
	if m[metrics.SmartDataReadBytes] != 123.5e9 || m[metrics.SmartDataWrittenBytes] != 45.25e9 || m[metrics.SmartReallocSectors] != 14 || m[metrics.SmartMediaErrors] != 5 {
		t.Fatalf("SCSI points = %#v", m)
	}
}

func TestSmartDevstatUnsupportedStillReads(t *testing.T) {
	f := &fakeSmartctl{responses: map[string]string{
		"-a --json=c -l devstat /dev/sda": `{
			"smartctl":{"exit_status":4,"messages":[{"string":"Device Statistics log not supported","severity":"information"}]},
			"temperature":{"current":31},
			"smart_status":{"passed":true}
		}`,
	}}
	c := &smartCollector{
		probed:        true,
		smartctl:      "/fake/smartctl",
		storcliProbed: true,
		goos:          "darwin",
		scanned:       []smartDevice{{name: "/dev/sda", devType: "ata"}},
		devicesAt:     time.Now(),
		devicesTTL:    time.Hour,
		execFn:        f.exec,
	}
	points := collectSmartSample(t, c)
	m := indexPoints(points)
	if m[metrics.SmartTempC] != 31 || m[metrics.SmartHealthy] != 1 {
		t.Fatalf("normal SMART points missing: %#v", m)
	}
	if state := c.Status().State; state != smartStateOK {
		t.Fatalf("state = %q, want %q", state, smartStateOK)
	}
}
