package collectors

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"servermonitor/pkg/metrics"
	"servermonitor/pkg/wire"
)

var smartctlCandidates = map[string][]string{
	"linux": {
		"/usr/sbin/smartctl",
		"/usr/local/sbin/smartctl",
		"/sbin/smartctl",
		"/usr/bin/smartctl",
	},
	"darwin": {
		"/usr/local/sbin/smartctl",
		"/opt/homebrew/sbin/smartctl",
		"/usr/sbin/smartctl",
	},
	"windows": {
		`C:\Program Files\smartmontools\bin\smartctl.exe`,
		`C:\Program Files (x86)\smartmontools\bin\smartctl.exe`,
	},
}

const (
	smartStateUnknown    = "unknown"
	smartStateOK         = "ok"
	smartStateBinary     = "binary_missing"
	smartStateScanFail   = "scan_failed"
	smartStateNoDevs     = "no_devices"
	smartStateReadFailed = "read_failed"
	smartStateRAIDHidden = "raid_unreadable"
)

const (
	smartReadWorkers  = 4
	raidProbeBudget   = 2 * time.Minute
	raidMaxCandidates = 8
)

type smartDevice struct {
	name    string
	devType string
	isNVMe  bool
}

type smartCollector struct {
	mu         sync.Mutex
	probed     bool
	smartctl   string
	goos       string
	scanned    []smartDevice
	raidExtra  []smartDevice
	raidHide   map[string]bool
	raidNote   string
	raidBusy   bool
	raidRunAt  time.Time
	devicesAt  time.Time
	devicesTTL time.Duration
	state      string
	stateMsg   string
	warned     bool
	execFn     func(ctx context.Context, bin string, args ...string) ([]byte, error)
	fileExists func(path string) bool
}

func init() { Register(&smartCollector{}) }

func (c *smartCollector) Name() string        { return "smart" }
func (c *smartCollector) Platforms() []string { return []string{"linux", "darwin", "windows"} }

func (c *smartCollector) Status() wire.CollectorStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.state
	if state == "" {
		state = smartStateUnknown
	}
	return wire.CollectorStatus{State: state, Message: c.stateMsg}
}

func (c *smartCollector) setStateLocked(state, msg string) {
	c.state = state
	c.stateMsg = msg
}

func (c *smartCollector) runBin(ctx context.Context, bin string, args ...string) ([]byte, error) {
	if c.execFn != nil {
		return c.execFn(ctx, bin, args...)
	}
	return run(ctx, bin, args...)
}

func (c *smartCollector) devicesLocked() []smartDevice {
	out := make([]smartDevice, 0, len(c.scanned)+len(c.raidExtra))
	for _, d := range c.scanned {
		if c.raidHide[d.name] {
			continue
		}
		out = append(out, d)
	}
	return append(out, c.raidExtra...)
}

func (c *smartCollector) emptyStateLocked() {
	switch {
	case c.raidNote != "":
		c.setStateLocked(smartStateRAIDHidden, c.raidNote)
	case c.raidBusy:
		c.setStateLocked(smartStateNoDevs, "no directly scannable devices; probing for drives behind a hardware RAID controller")
	default:
		c.setStateLocked(smartStateNoDevs, "smartctl scanned successfully but reports no SMART-capable devices")
	}
}

func (c *smartCollector) ensure(ctx context.Context) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.devicesTTL == 0 {
		c.devicesTTL = 5 * time.Minute
	}
	if c.goos == "" {
		c.goos = runtime.GOOS
	}
	if c.fileExists == nil {
		c.fileExists = func(path string) bool {
			_, err := os.Stat(path)
			return err == nil
		}
	}
	if !c.probed {
		c.probed = true
		c.smartctl = resolveTrustedBin(smartctlCandidates[runtime.GOOS])
	}
	if c.smartctl == "" {
		c.setStateLocked(smartStateBinary, "smartctl binary not found; install smartmontools")
		return false
	}
	if time.Since(c.devicesAt) < c.devicesTTL && c.scanned != nil {
		c.maybeDiscoverRAIDLocked()
		if len(c.devicesLocked()) > 0 {
			return true
		}
		c.emptyStateLocked()
		return false
	}
	out, err := c.runBin(ctx, c.smartctl, "--scan", "--json=c")
	if err != nil {
		c.setStateLocked(smartStateScanFail, "smartctl --scan failed: "+err.Error())
		return false
	}
	var scan struct {
		Devices []struct {
			Name     string `json:"name"`
			Type     string `json:"type"`
			Protocol string `json:"protocol"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(out, &scan); err != nil {
		c.setStateLocked(smartStateScanFail, "smartctl --scan returned unparseable output")
		return false
	}
	c.scanned = make([]smartDevice, 0, len(scan.Devices))
	for _, d := range scan.Devices {
		c.scanned = append(c.scanned, smartDevice{name: d.Name, devType: d.Type, isNVMe: isNVMeDevice(d.Name, d.Type, d.Protocol)})
	}
	c.devicesAt = time.Now()
	c.maybeDiscoverRAIDLocked()
	if len(c.devicesLocked()) == 0 {
		c.emptyStateLocked()
		return false
	}
	c.setStateLocked(smartStateOK, "")
	return true
}

func (c *smartCollector) maybeDiscoverRAIDLocked() {
	if c.goos != "linux" && c.goos != "windows" {
		return
	}
	if c.raidBusy || time.Since(c.raidRunAt) < c.devicesTTL {
		return
	}
	candidates := raidCandidates(c.scanned)
	ioctlFallback := c.goos == "linux" &&
		len(passthroughFamilies(c.scanned)) == 0 &&
		c.fileExists("/dev/megaraid_sas_ioctl_node")
	c.raidRunAt = time.Now()
	if len(candidates) == 0 && !ioctlFallback {
		c.raidExtra = nil
		c.raidHide = nil
		c.raidNote = ""
		return
	}
	c.raidBusy = true
	go c.discoverRAID(c.smartctl, c.goos, candidates, ioctlFallback, append([]smartDevice(nil), c.scanned...))
}

func raidCandidates(scanned []smartDevice) []smartDevice {
	out := make([]smartDevice, 0, raidMaxCandidates)
	for _, d := range scanned {
		if len(out) == raidMaxCandidates {
			break
		}
		if d.isNVMe {
			continue
		}
		if d.devType == "" || d.devType == "scsi" {
			out = append(out, d)
		}
	}
	return out
}

var raidPassthroughKeywords = []string{"megaraid", "cciss", "aacraid", "3ware", "areca"}

func passthroughFamilies(devs []smartDevice) map[string]bool {
	out := map[string]bool{}
	for _, d := range devs {
		for _, k := range raidPassthroughKeywords {
			if strings.Contains(d.devType, k) {
				out[k] = true
			}
		}
	}
	return out
}

func (c *smartCollector) discoverRAID(bin, goos string, candidates []smartDevice, ioctlFallback bool, scanned []smartDevice) {
	ctx, cancel := context.WithTimeout(context.Background(), raidProbeBudget)
	defer cancel()
	found, hide, note := c.probeRAIDPassthrough(ctx, bin, goos, candidates, ioctlFallback, scanned)
	c.mu.Lock()
	c.raidBusy = false
	c.raidExtra = found
	c.raidHide = hide
	c.raidNote = note
	c.mu.Unlock()
	if len(found) > 0 {
		slog.Info("smart raid passthrough drives discovered", "collector", "smart", "drives", len(found))
	} else if note != "" {
		slog.Warn("smart raid passthrough probe found no drives", "collector", "smart", "detail", note)
	}
}

func (c *smartCollector) probeRAIDPassthrough(ctx context.Context, bin, goos string, candidates []smartDevice, ioctlFallback bool, scanned []smartDevice) ([]smartDevice, map[string]bool, string) {
	skip := passthroughFamilies(scanned)
	seenSerials := map[string]bool{}
	seenDevs := map[string]bool{}
	for _, d := range scanned {
		seenDevs[d.name+"|"+d.devType] = true
	}
	var found []smartDevice
	hide := map[string]bool{}
	blockedName, blockedModel := "", ""
	for _, cand := range candidates {
		if ctx.Err() != nil {
			break
		}
		identity, opened := c.readIdentity(ctx, bin, cand.name, "")
		families, explicit := raidFamiliesFor(identity, opened, goos)
		hits := 0
		covered := false
		for _, fam := range families {
			if skip[fam] {
				covered = true
				continue
			}
			drives := c.walkFamily(ctx, bin, fam, cand.name, seenSerials, seenDevs)
			if len(drives) > 0 {
				found = append(found, drives...)
				hide[cand.name] = true
				hits = len(drives)
				break
			}
		}
		if hits == 0 && covered {
			hide[cand.name] = true
			continue
		}
		if hits == 0 && explicit && blockedName == "" {
			blockedName = strings.TrimPrefix(cand.name, "/dev/")
			blockedModel = identityModel(identity)
		}
	}
	if ioctlFallback && !skip["megaraid"] {
		for _, node := range []string{"/dev/bus/0", "/dev/bus/1"} {
			if ctx.Err() != nil {
				break
			}
			found = append(found, c.walkFamily(ctx, bin, "megaraid", node, seenSerials, seenDevs)...)
		}
	}
	note := ""
	if blockedName != "" && len(found) == 0 {
		note = fmt.Sprintf("hardware RAID virtual disk %s (%s) hides its member drives and the smartctl passthrough probe found none; check the controller with its own CLI (storcli/perccli/ssacli) or run smartctl -d megaraid,N manually", blockedName, blockedModel)
	}
	return found, hide, note
}

var raidSignatures = [][2]string{
	{"megaraid", "megaraid"},
	{"mraid", "megaraid"},
	{"perc", "megaraid"},
	{"lsi", "megaraid"},
	{"avago", "megaraid"},
	{"broadcom", "megaraid"},
	{"smc", "megaraid"},
	{"serveraid", "megaraid"},
	{"mr9", "megaraid"},
	{"logical volume", "cciss"},
	{"smart array", "cciss"},
	{"smartraid", "cciss"},
	{"adaptec", "aacraid"},
	{"aacraid", "aacraid"},
	{"3ware", "3ware"},
	{"amcc", "3ware"},
	{"areca", "areca"},
	{"raid", "megaraid"},
}

func raidFamiliesFor(identity *smartView, opened bool, goos string) ([]string, bool) {
	if !opened || smartSupported(identity) {
		return nil, false
	}
	model := strings.ToLower(identityModel(identity))
	for _, sig := range raidSignatures {
		if strings.Contains(model, sig[0]) {
			return familiesForSignature(sig[1], goos), true
		}
	}
	if goos == "windows" {
		return []string{"megaraid"}, false
	}
	return []string{"megaraid", "cciss"}, false
}

func familiesForSignature(family, goos string) []string {
	if goos == "windows" {
		if family == "megaraid" {
			return []string{"megaraid"}
		}
		return nil
	}
	if family == "megaraid" {
		return []string{"megaraid", "cciss"}
	}
	return []string{family}
}

func (c *smartCollector) walkFamily(ctx context.Context, bin, family, node string, seenSerials, seenDevs map[string]bool) []smartDevice {
	switch family {
	case "megaraid":
		return c.walkIDs(ctx, bin, node, 0, 63, 16, seenSerials, seenDevs, func(i int) string { return fmt.Sprintf("megaraid,%d", i) })
	case "cciss":
		return c.walkIDs(ctx, bin, node, 0, 47, 16, seenSerials, seenDevs, func(i int) string { return fmt.Sprintf("cciss,%d", i) })
	case "aacraid":
		return c.walkIDs(ctx, bin, node, 0, 31, 8, seenSerials, seenDevs, func(i int) string { return fmt.Sprintf("aacraid,0,0,%d", i) })
	case "3ware":
		for _, n := range []string{"/dev/twl0", "/dev/twa0", "/dev/twe0"} {
			if !c.fileExists(n) {
				continue
			}
			if hits := c.walkIDs(ctx, bin, n, 0, 31, 8, seenSerials, seenDevs, func(i int) string { return fmt.Sprintf("3ware,%d", i) }); len(hits) > 0 {
				return hits
			}
		}
	case "areca":
		for _, n := range arecaNodes(node, c.fileExists) {
			if hits := c.walkIDs(ctx, bin, n, 1, 24, 8, seenSerials, seenDevs, func(i int) string { return fmt.Sprintf("areca,%d", i) }); len(hits) > 0 {
				return hits
			}
		}
	}
	return nil
}

func arecaNodes(scannedNode string, exists func(string) bool) []string {
	out := []string{scannedNode}
	for i := 0; i < 8; i++ {
		n := fmt.Sprintf("/dev/sg%d", i)
		if exists != nil && exists(n) {
			out = append(out, n)
		}
	}
	return out
}

func (c *smartCollector) walkIDs(ctx context.Context, bin, node string, lo, hi, missCutoff int, seenSerials, seenDevs map[string]bool, devType func(int) string) []smartDevice {
	var out []smartDevice
	misses := 0
	for i := lo; i <= hi && misses < missCutoff; i++ {
		if ctx.Err() != nil {
			break
		}
		t := devType(i)
		identity, opened := c.readIdentity(ctx, bin, node, t)
		if !opened {
			misses++
			continue
		}
		misses = 0
		if !identityIsDisk(identity) {
			continue
		}
		key := node + "|" + t
		if seenDevs[key] {
			continue
		}
		if sn := identity.SerialNumber; sn != "" {
			if seenSerials[sn] {
				continue
			}
			seenSerials[sn] = true
		}
		seenDevs[key] = true
		out = append(out, smartDevice{name: node, devType: t})
	}
	return out
}

func (c *smartCollector) readIdentity(ctx context.Context, bin, node, devType string) (*smartView, bool) {
	args := []string{"-i", "--json=c"}
	if devType != "" {
		args = append(args, "-d", devType)
	}
	args = append(args, node)
	body, _ := c.runBin(ctx, bin, args...)
	var v smartView
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, false
	}
	exitStatus := 0
	if v.Smartctl != nil {
		exitStatus = v.Smartctl.ExitStatus
	}
	if smartCannotRead(exitStatus) {
		return &v, false
	}
	return &v, true
}

func smartSupported(v *smartView) bool {
	return v != nil && v.SmartSupport != nil && v.SmartSupport.Available
}

func identityIsDisk(v *smartView) bool {
	if v == nil {
		return false
	}
	if v.DeviceType != nil && v.DeviceType.Name != "" && v.DeviceType.Name != "disk" {
		return false
	}
	return true
}

func identityModel(v *smartView) string {
	if v == nil {
		return "unknown"
	}
	if v.ModelName != "" {
		return v.ModelName
	}
	m := strings.TrimSpace(strings.TrimSpace(v.Vendor) + " " + strings.TrimSpace(v.Product))
	if m == "" {
		return "unknown"
	}
	return m
}

func smartReadArgs(dev smartDevice) []string {
	args := []string{"-a", "--json=c"}
	if !smartTypeAutoDetect(dev.devType) {
		args = append(args, "-d", dev.devType)
	}
	return append(args, dev.name)
}

func smartTypeAutoDetect(scanType string) bool {
	switch scanType {
	case "", "scsi", "ata":
		return true
	}
	return false
}

func (c *smartCollector) Collect(ctx context.Context) ([]wire.Point, error) {
	if !c.ensure(ctx) {
		return nil, nil
	}
	c.mu.Lock()
	devices := c.devicesLocked()
	bin := c.smartctl
	c.mu.Unlock()

	now := time.Now()
	var (
		tallyMu    sync.Mutex
		wg         sync.WaitGroup
		out        = make([]wire.Point, 0, len(devices)*4)
		readOK     int
		failed     int
		nvmeFailed int
	)
	sem := make(chan struct{}, smartReadWorkers)
	for _, dev := range devices {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(dev smartDevice) {
			defer wg.Done()
			defer func() { <-sem }()
			body, _ := c.runBin(ctx, bin, smartReadArgs(dev)...)
			var s smartView
			parseErr := json.Unmarshal(body, &s)
			exitStatus := 0
			if s.Smartctl != nil {
				exitStatus = s.Smartctl.ExitStatus
			}
			tallyMu.Lock()
			defer tallyMu.Unlock()
			if parseErr != nil || smartCannotRead(exitStatus) {
				if ctx.Err() != nil {
					return
				}
				failed++
				if dev.isNVMe {
					nvmeFailed++
				}
				return
			}
			readOK++
			labels := map[string]string{"device": deviceLabel(dev.name, dev.devType)}
			if m := identityModel(&s); m != "unknown" {
				labels["model"] = m
			}
			out = append(out, smartPoints(now, labels, &s)...)
		}(dev)
	}
	wg.Wait()

	c.mu.Lock()
	logHint := c.updateReadStateLocked(readOK, failed, nvmeFailed)
	msg := c.stateMsg
	c.mu.Unlock()
	if logHint {
		slog.Warn("smart per-device read failed", "collector", "smart", "detail", msg)
	}
	return out, nil
}

func (c *smartCollector) updateReadStateLocked(readOK, failed, nvmeFailed int) bool {
	if failed == 0 && c.raidNote == "" {
		c.setStateLocked(smartStateOK, "")
		c.warned = false
		return false
	}
	if failed > 0 {
		hint := smartReadFailHint(readOK, failed, nvmeFailed)
		if c.raidNote != "" {
			hint += "; " + c.raidNote
		}
		c.setStateLocked(smartStateReadFailed, hint)
	} else {
		c.setStateLocked(smartStateRAIDHidden, c.raidNote)
	}
	if c.warned {
		return false
	}
	c.warned = true
	return true
}

func smartReadFailHint(readOK, failed, nvmeFailed int) string {
	base := fmt.Sprintf("read SMART data from %d of %d devices", readOK, readOK+failed)
	if nvmeFailed > 0 && runtime.GOOS == "linux" {
		return fmt.Sprintf("%s; %d NVMe device(s) returned no data — NVMe SMART needs CAP_SYS_ADMIN, which CAP_SYS_RAWIO does not grant", base, nvmeFailed)
	}
	return base + "; smartctl could not read the rest — check that the agent has the privileges its devices require"
}

const nvmeDataUnitBytes = 512000

func deviceLabel(name, devType string) string {
	label := strings.TrimPrefix(name, "/dev/")
	if i := strings.LastIndex(devType, ","); i >= 0 {
		label += "#" + devType[i+1:]
	}
	return label
}

func smartPoints(now time.Time, labels map[string]string, s *smartView) []wire.Point {
	out := make([]wire.Point, 0, 12)
	if s.Temperature != nil && s.Temperature.Current > 0 {
		out = append(out, point(now, metrics.SmartTempC, labels, float64(s.Temperature.Current)))
	}
	if s.PowerOnTime != nil && s.PowerOnTime.Hours > 0 {
		out = append(out, point(now, metrics.SmartPowerOnHours, labels, float64(s.PowerOnTime.Hours)))
	}
	if s.SmartStatus != nil {
		healthy := 0.0
		if s.SmartStatus.Passed {
			healthy = 1
		}
		out = append(out, point(now, metrics.SmartHealthy, labels, healthy))
	}
	if n := s.NVMeLog; n != nil {
		if n.DataUnitsWritten > 0 {
			out = append(out, point(now, metrics.SmartDataWrittenBytes, labels, float64(n.DataUnitsWritten)*nvmeDataUnitBytes))
		}
		if n.DataUnitsRead > 0 {
			out = append(out, point(now, metrics.SmartDataReadBytes, labels, float64(n.DataUnitsRead)*nvmeDataUnitBytes))
		}
		out = append(out, point(now, metrics.SmartPercentUsed, labels, float64(n.PercentageUsed)))
		out = append(out, point(now, metrics.SmartAvailableSpare, labels, float64(n.AvailableSpare)))
		out = append(out, point(now, metrics.SmartMediaErrors, labels, float64(n.MediaErrors)))
		out = append(out, point(now, metrics.SmartPowerCycles, labels, float64(n.PowerCycles)))
		out = append(out, point(now, metrics.SmartUnsafeShutdowns, labels, float64(n.UnsafeShutdowns)))
	}
	sectorBytes := s.LogicalBlockSize
	if sectorBytes <= 0 {
		sectorBytes = 512
	}
	for _, a := range s.AtaSmartAttributes.Table {
		switch a.Name {
		case "Reallocated_Sector_Ct":
			out = append(out, point(now, metrics.SmartReallocSectors, labels, float64(a.Raw.Value)))
		case "Current_Pending_Sector":
			out = append(out, point(now, metrics.SmartPendingSectors, labels, float64(a.Raw.Value)))
		case "Offline_Uncorrectable":
			out = append(out, point(now, metrics.SmartOfflineUncorrect, labels, float64(a.Raw.Value)))
		case "UDMA_CRC_Error_Count":
			out = append(out, point(now, metrics.SmartCRCErrors, labels, float64(a.Raw.Value)))
		case "Power_Cycle_Count":
			out = append(out, point(now, metrics.SmartPowerCycles, labels, float64(a.Raw.Value)))
		case "Total_LBAs_Written":
			out = append(out, point(now, metrics.SmartDataWrittenBytes, labels, float64(a.Raw.Value)*float64(sectorBytes)))
		case "Total_LBAs_Read":
			out = append(out, point(now, metrics.SmartDataReadBytes, labels, float64(a.Raw.Value)*float64(sectorBytes)))
		}
	}
	return out
}

func isNVMeDevice(name, typ, protocol string) bool {
	if strings.EqualFold(protocol, "nvme") {
		return true
	}
	if strings.HasPrefix(strings.ToLower(typ), "nvme") {
		return true
	}
	return strings.HasPrefix(name, "/dev/nvme")
}

const smartctlOpenFailMask = 0x03

func smartCannotRead(exitStatus int) bool {
	return exitStatus&smartctlOpenFailMask != 0
}

type smartctlMeta struct {
	ExitStatus int `json:"exit_status"`
}

type smartView struct {
	Smartctl    *smartctlMeta `json:"smartctl"`
	Temperature *struct {
		Current int `json:"current"`
	} `json:"temperature"`
	PowerOnTime *struct {
		Hours int `json:"hours"`
	} `json:"power_on_time"`
	SmartStatus *struct {
		Passed bool `json:"passed"`
	} `json:"smart_status"`
	SmartSupport *struct {
		Available bool `json:"available"`
	} `json:"smart_support"`
	DeviceType *struct {
		Name      string `json:"name"`
		SCSIValue int    `json:"scsi_value"`
	} `json:"device_type"`
	Vendor           string `json:"vendor"`
	Product          string `json:"product"`
	ModelName        string `json:"model_name"`
	SerialNumber     string `json:"serial_number"`
	LogicalBlockSize int64  `json:"logical_block_size"`
	NVMeLog          *struct {
		DataUnitsWritten int64 `json:"data_units_written"`
		DataUnitsRead    int64 `json:"data_units_read"`
		PercentageUsed   int   `json:"percentage_used"`
		AvailableSpare   int   `json:"available_spare"`
		MediaErrors      int64 `json:"media_errors"`
		PowerCycles      int64 `json:"power_cycles"`
		UnsafeShutdowns  int64 `json:"unsafe_shutdowns"`
	} `json:"nvme_smart_health_information_log"`
	AtaSmartAttributes struct {
		Table []struct {
			Name string `json:"name"`
			Raw  struct {
				Value int64 `json:"value"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
}

func run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return exec.CommandContext(cmdCtx, name, args...).Output()
}
