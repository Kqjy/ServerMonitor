package collectors

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"servermonitor/pkg/metrics"
	"servermonitor/pkg/wire"
)

var storcliCandidates = map[string][]string{
	"linux": {
		"/opt/MegaRAID/storcli/storcli64",
		"/opt/MegaRAID/storcli/storcli",
		"/opt/MegaRAID/perccli/perccli64",
		"/opt/MegaRAID/perccli/perccli",
		"/usr/local/sbin/storcli64",
		"/usr/local/sbin/storcli",
		"/usr/sbin/storcli64",
		"/usr/sbin/storcli",
		"/usr/local/bin/storcli64",
		"/usr/local/bin/storcli",
		"/usr/bin/storcli64",
		"/usr/bin/storcli",
		"/usr/local/sbin/perccli64",
		"/usr/sbin/perccli64",
		"/usr/bin/perccli64",
	},
	"windows": {
		`C:\Program Files\MegaRAID\storcli\storcli64.exe`,
		`C:\Program Files (x86)\MegaRAID\storcli\storcli64.exe`,
	},
}

const (
	storcliCallTimeout = 20 * time.Second
	storcliRefreshAge  = 60 * time.Second
	storcliRefreshWait = 60 * time.Second
)

type storcliDrive struct {
	ctl        int
	did        int
	slot       string
	state      string
	model      string
	serial     string
	tempC      int
	mediaErr   int64
	otherErr   int64
	predFail   int64
	smartAlert bool
	node       string
	cliOnly    bool
}

type storcliVD struct {
	ctl   int
	vd    int
	typ   string
	state string
	node  string
}

type storcliEnvelope struct {
	Controllers []struct {
		CommandStatus struct {
			Status string `json:"Status"`
		} `json:"Command Status"`
		ResponseData json.RawMessage `json:"Response Data"`
	} `json:"Controllers"`
}

var (
	storcliTempPattern = regexp.MustCompile(`(-?\d+)\s*C`)
	storcliVDPattern   = regexp.MustCompile(`^/c(\d+)/v(\d+)$`)
	storcliPropPattern = regexp.MustCompile(`^VD(\d+) Properties$`)
)

func (c *smartCollector) resolveStorcli(goos string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.storcliProbed {
		c.storcli = resolveTrustedBin(storcliCandidates[goos])
		c.storcliProbed = true
	}
	return c.storcli
}

func (c *smartCollector) runBinTimeout(ctx context.Context, timeout time.Duration, bin string, args ...string) ([]byte, error) {
	if c.execFn != nil {
		return c.execFn(ctx, bin, args...)
	}
	return runTimeout(ctx, timeout, bin, args...)
}

func storcliResponseData(body []byte) (map[string]json.RawMessage, error) {
	var envelope storcliEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	if len(envelope.Controllers) == 0 {
		return nil, fmt.Errorf("storcli returned no controller response")
	}
	controller := envelope.Controllers[0]
	if !strings.EqualFold(strings.TrimSpace(controller.CommandStatus.Status), "Success") {
		return nil, fmt.Errorf("storcli command status %q", controller.CommandStatus.Status)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(controller.ResponseData, &data); err != nil {
		return nil, err
	}
	return data, nil
}

func parseStorcliControllerCount(body []byte) (int, error) {
	data, err := storcliResponseData(body)
	if err != nil {
		return 0, err
	}
	count, ok := asInt64(data["Controller Count"])
	if !ok || count < 0 {
		return 0, fmt.Errorf("storcli controller count is invalid")
	}
	return int(count), nil
}

func parseStorcliDrives(body []byte, ctl int) ([]storcliDrive, error) {
	data, err := storcliResponseData(body)
	if err != nil {
		return nil, err
	}
	drives := make([]storcliDrive, 0)
	byPath := map[string]int{}
	for key, raw := range data {
		if !strings.HasPrefix(key, "Drive ") || strings.HasSuffix(key, " - Detailed Information") {
			continue
		}
		path := strings.TrimSpace(strings.TrimPrefix(key, "Drive "))
		var rows []map[string]json.RawMessage
		if json.Unmarshal(raw, &rows) != nil || len(rows) == 0 {
			continue
		}
		did, ok := asInt64(rows[0]["DID"])
		if !ok {
			continue
		}
		slot := rawString(rows[0]["EID:Slt"])
		if slot == "" {
			parts := strings.Split(path, "/")
			if len(parts) > 0 && strings.HasPrefix(parts[len(parts)-1], "s") {
				slot = parts[len(parts)-1]
			}
		}
		byPath[path] = len(drives)
		drives = append(drives, storcliDrive{
			ctl:   ctl,
			did:   int(did),
			slot:  slot,
			state: rawString(rows[0]["State"]),
			model: rawString(rows[0]["Model"]),
		})
	}
	for key, raw := range data {
		if !strings.HasPrefix(key, "Drive ") || !strings.HasSuffix(key, " - Detailed Information") {
			continue
		}
		path := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(key, "Drive "), " - Detailed Information"))
		idx, ok := byPath[path]
		if !ok {
			continue
		}
		var detail map[string]json.RawMessage
		if json.Unmarshal(raw, &detail) != nil {
			continue
		}
		for detailKey, detailRaw := range detail {
			switch {
			case strings.HasSuffix(detailKey, " State"):
				applyStorcliDriveState(&drives[idx], detailRaw)
			case strings.HasSuffix(detailKey, " Device attributes"):
				applyStorcliDriveAttributes(&drives[idx], detailRaw)
			}
		}
	}
	sort.Slice(drives, func(i, j int) bool {
		if drives[i].did == drives[j].did {
			return drives[i].slot < drives[j].slot
		}
		return drives[i].did < drives[j].did
	})
	return drives, nil
}

func applyStorcliDriveState(drive *storcliDrive, raw json.RawMessage) {
	var state map[string]json.RawMessage
	if json.Unmarshal(raw, &state) != nil {
		return
	}
	if value, ok := asInt64(state["Media Error Count"]); ok {
		drive.mediaErr = value
	}
	if value, ok := asInt64(state["Other Error Count"]); ok {
		drive.otherErr = value
	}
	if value, ok := asInt64(state["Predictive Failure Count"]); ok {
		drive.predFail = value
	}
	drive.smartAlert = strings.EqualFold(rawString(state["S.M.A.R.T alert flagged by drive"]), "Yes")
	drive.tempC = parseStorcliTemp(rawString(state["Drive Temperature"]))
}

func applyStorcliDriveAttributes(drive *storcliDrive, raw json.RawMessage) {
	var attrs map[string]json.RawMessage
	if json.Unmarshal(raw, &attrs) != nil {
		return
	}
	drive.serial = rawString(attrs["SN"])
	if model := rawString(attrs["Model Number"]); model != "" {
		drive.model = model
	}
}

func parseStorcliVDs(body []byte, ctl int) ([]storcliVD, error) {
	data, err := storcliResponseData(body)
	if err != nil {
		return nil, err
	}
	vds := make([]storcliVD, 0)
	byVD := map[int]int{}
	for key, raw := range data {
		match := storcliVDPattern.FindStringSubmatch(key)
		if len(match) != 3 {
			continue
		}
		keyCtl, errCtl := strconv.Atoi(match[1])
		vd, errVD := strconv.Atoi(match[2])
		if errCtl != nil || errVD != nil || keyCtl != ctl {
			continue
		}
		var rows []map[string]json.RawMessage
		if json.Unmarshal(raw, &rows) != nil || len(rows) == 0 {
			continue
		}
		byVD[vd] = len(vds)
		vds = append(vds, storcliVD{
			ctl:   ctl,
			vd:    vd,
			typ:   rawString(rows[0]["TYPE"]),
			state: rawString(rows[0]["State"]),
		})
	}
	for key, raw := range data {
		match := storcliPropPattern.FindStringSubmatch(key)
		if len(match) != 2 {
			continue
		}
		vd, err := strconv.Atoi(match[1])
		idx, ok := byVD[vd]
		if err != nil || !ok {
			continue
		}
		var props map[string]json.RawMessage
		if json.Unmarshal(raw, &props) != nil {
			continue
		}
		vds[idx].node = rawString(props["OS Drive Name"])
	}
	sort.Slice(vds, func(i, j int) bool { return vds[i].vd < vds[j].vd })
	return vds, nil
}

func parseStorcliTemp(value string) int {
	match := storcliTempPattern.FindStringSubmatch(value)
	if len(match) != 2 {
		return 0
	}
	temp, err := strconv.Atoi(match[1])
	if err != nil {
		return 0
	}
	return temp
}

func asInt64(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		value, err := number.Int64()
		if err == nil {
			return value, true
		}
	}
	value, err := strconv.ParseInt(rawString(raw), 10, 64)
	return value, err == nil
}

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(string(raw))
}

func (c *smartCollector) enumerateStorcli(ctx context.Context, bin string) ([]storcliDrive, []storcliVD, error) {
	body, err := c.runBinTimeout(ctx, storcliCallTimeout, bin, "show", "ctrlcount", "J")
	if err != nil {
		return nil, nil, err
	}
	count, err := parseStorcliControllerCount(body)
	if err != nil || count == 0 {
		return nil, nil, err
	}
	var drives []storcliDrive
	var vds []storcliVD
	driveCalls := 0
	for ctl := 0; ctl < count && ctx.Err() == nil; ctl++ {
		driveBody, driveErr := c.runBinTimeout(ctx, storcliCallTimeout, bin, fmt.Sprintf("/c%d/eall/sall", ctl), "show", "all", "J")
		if driveErr == nil {
			parsed, parseErr := parseStorcliDrives(driveBody, ctl)
			if parseErr == nil {
				driveCalls++
				drives = append(drives, parsed...)
			}
		}
		vdBody, vdErr := c.runBinTimeout(ctx, storcliCallTimeout, bin, fmt.Sprintf("/c%d/vall", ctl), "show", "all", "J")
		if vdErr == nil {
			parsed, parseErr := parseStorcliVDs(vdBody, ctl)
			if parseErr == nil {
				vds = append(vds, parsed...)
			}
		}
	}
	for i := range drives {
		for _, vd := range vds {
			if vd.ctl == drives[i].ctl && vd.node != "" {
				drives[i].node = vd.node
				break
			}
		}
	}
	if driveCalls == 0 {
		return drives, vds, fmt.Errorf("storcli physical-drive enumeration failed")
	}
	return drives, vds, nil
}

func storcliDriveLabels(drive storcliDrive) map[string]string {
	device := fmt.Sprintf("c%d#%d", drive.ctl, drive.did)
	if drive.node != "" {
		device = deviceLabel(drive.node, fmt.Sprintf("megaraid,%d", drive.did))
	}
	labels := map[string]string{"device": device}
	if drive.model != "" {
		labels["model"] = drive.model
	}
	if drive.slot != "" {
		labels["slot"] = drive.slot
	}
	return labels
}

func storcliDrivePoints(now time.Time, drive storcliDrive) []wire.Point {
	labels := storcliDriveLabels(drive)
	out := make([]wire.Point, 0, 3)
	if drive.tempC > 0 {
		out = append(out, point(now, metrics.SmartTempC, labels, float64(drive.tempC)))
	}
	out = append(out, point(now, metrics.SmartMediaErrors, labels, float64(drive.mediaErr)))
	healthy := 1.0
	switch strings.ToLower(strings.TrimSpace(drive.state)) {
	case "failed", "offln", "msng":
		healthy = 0
	}
	if drive.predFail > 0 || drive.smartAlert {
		healthy = 0
	}
	out = append(out, point(now, metrics.SmartHealthy, labels, healthy))
	return out
}

func storcliMediaErrorPoint(now time.Time, drive storcliDrive) wire.Point {
	return point(now, metrics.SmartMediaErrors, storcliDriveLabels(drive), float64(drive.mediaErr))
}

func storcliVDPoint(now time.Time, vd storcliVD) wire.Point {
	value := 1.0
	if strings.EqualFold(strings.TrimSpace(vd.state), "Optl") {
		value = 0
	}
	return point(now, metrics.RaidDegraded, map[string]string{
		"array": fmt.Sprintf("c%d/v%d", vd.ctl, vd.vd),
		"type":  vd.typ,
	}, value)
}

func (c *smartCollector) maybeRefreshStorcliLocked() {
	if c.cliBusy || c.storcli == "" || len(c.cliDrives)+len(c.cliVDs) == 0 || time.Since(c.cliAt) <= storcliRefreshAge {
		return
	}
	previous := make(map[[2]int]storcliDrive, len(c.cliDrives))
	for _, drive := range c.cliDrives {
		previous[[2]int{drive.ctl, drive.did}] = drive
	}
	c.cliBusy = true
	go c.refreshStorcli(c.storcli, previous)
}

func (c *smartCollector) refreshStorcli(bin string, previous map[[2]int]storcliDrive) {
	ctx, cancel := context.WithTimeout(context.Background(), storcliRefreshWait)
	defer cancel()
	drives, vds, err := c.enumerateStorcli(ctx, bin)
	if err == nil {
		for i := range drives {
			if old, ok := previous[[2]int{drives[i].ctl, drives[i].did}]; ok {
				drives[i].cliOnly = old.cliOnly
				if drives[i].node == "" {
					drives[i].node = old.node
				}
			}
		}
		c.mu.Lock()
		c.cliDrives = drives
		c.cliVDs = vds
		c.cliAt = time.Now()
		c.cliBusy = false
		c.mu.Unlock()
		return
	}
	c.mu.Lock()
	c.cliBusy = false
	c.mu.Unlock()
}
