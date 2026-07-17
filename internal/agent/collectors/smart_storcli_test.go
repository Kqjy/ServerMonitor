package collectors

import (
	"context"
	"strings"
	"testing"
	"time"

	"servermonitor/pkg/metrics"
)

const storcliCtrlCountFixture = `{"Controllers":[{"Command Status":{"Status":"Success"},"Response Data":{"Controller Count":1}}]}`

const storcliDrivesFixture = `{
	"Controllers":[{
		"Command Status":{"Status":"Success"},
		"Response Data":{
			"Drive /c0/e252/s0":[{"EID:Slt":"252:0","DID":8,"State":"Onln","DG":0,"Size":"3.637 TB","Intf":"SATA","Med":"HDD","Model":"ST4000NM0023","Sp":"U"}],
			"Drive /c0/e252/s0 - Detailed Information":{
				"Drive /c0/e252/s0 State":{"Media Error Count":2,"Other Error Count":1,"Predictive Failure Count":0,"S.M.A.R.T alert flagged by drive":"No","Drive Temperature":" 34C (93.20 F)"},
				"Drive /c0/e252/s0 Device attributes":{"SN":"  Z1Z0AAAA  ","Model Number":"ST4000NM0023","WWN":"5000","Firmware Revision":"GS10"}
			},
			"Drive /c0/e252/s1":[{"EID:Slt":"252:1","DID":"9","State":"Onln","DG":0,"Size":"3.637 TB","Intf":"SATA","Med":"HDD","Model":"ST4000NM0023","Sp":"U"}],
			"Drive /c0/e252/s1 - Detailed Information":{
				"Drive /c0/e252/s1 State":{"Media Error Count":"0","Other Error Count":"0","Predictive Failure Count":"1","S.M.A.R.T alert flagged by drive":"Yes","Drive Temperature":" 35C (95.00 F)"},
				"Drive /c0/e252/s1 Device attributes":{"SN":"  Z1Z0BBBB  ","Model Number":"ST4000NM0023"}
			},
			"Drive /c0/s4":[{"DID":10,"State":"JBOD","DG":"-","Size":"1.819 TB","Intf":"SATA","Med":"SSD","Model":"SSD MODEL"}],
			"Drive /c0/s4 - Detailed Information":{
				"Drive /c0/s4 State":{"Media Error Count":0,"Other Error Count":3,"Predictive Failure Count":0,"S.M.A.R.T alert flagged by drive":"No","Drive Temperature":"N/A"},
				"Drive /c0/s4 Device attributes":{"SN":"  SSD123  ","Model Number":"SSD MODEL"}
			}
		}
	}]
}`

const storcliProbeDrivesFixture = `{
	"Controllers":[{
		"Command Status":{"Status":"Success"},
		"Response Data":{
			"Drive /c0/e252/s0":[{"EID:Slt":"252:0","DID":8,"State":"Onln","Model":"ST4000NM0023"}],
			"Drive /c0/e252/s0 - Detailed Information":{"Drive /c0/e252/s0 State":{"Media Error Count":2,"Other Error Count":1,"Predictive Failure Count":0,"S.M.A.R.T alert flagged by drive":"No","Drive Temperature":" 34C (93.20 F)"},"Drive /c0/e252/s0 Device attributes":{"SN":"  Z1Z0AAAA  ","Model Number":"ST4000NM0023"}},
			"Drive /c0/e252/s1":[{"EID:Slt":"252:1","DID":9,"State":"Onln","Model":"ST4000NM0023"}],
			"Drive /c0/e252/s1 - Detailed Information":{"Drive /c0/e252/s1 State":{"Media Error Count":0,"Other Error Count":0,"Predictive Failure Count":1,"S.M.A.R.T alert flagged by drive":"Yes","Drive Temperature":" 35C (95.00 F)"},"Drive /c0/e252/s1 Device attributes":{"SN":"  Z1Z0BBBB  ","Model Number":"ST4000NM0023"}}
		}
	}]
}`

const storcliVDsFixture = `{
	"Controllers":[{
		"Command Status":{"Status":"Success"},
		"Response Data":{
			"/c0/v0":[{"DG/VD":"0/0","TYPE":"RAID5","State":"Optl","Access":"RW","Size":"10.913 TB","Name":""}],
			"VD0 Properties":{"OS Drive Name":"/dev/sda","SCSI NAA Id":"6000"},
			"/c0/v1":[{"DG/VD":"1/1","TYPE":"RAID1","State":"Dgrd","Access":"RW","Size":"1.819 TB","Name":"data"}],
			"VD1 Properties":{"OS Drive Name":"/dev/sdb","SCSI NAA Id":"6001"}
		}
	}]
}`

const storcliVDsNoNodeFixture = `{
	"Controllers":[{
		"Command Status":{"Status":"Success"},
		"Response Data":{
			"/c0/v0":[{"DG/VD":"0/0","TYPE":"RAID5","State":"Optl","Access":"RW","Size":"10.913 TB","Name":""}],
			"VD0 Properties":{"OS Drive Name":"","SCSI NAA Id":"6000"}
		}
	}]
}`

const (
	storcliFullRead8 = `{"smartctl":{"exit_status":0},"model_name":"ST4000NM0023","serial_number":"Z1Z0AAAA","temperature":{"current":34},"power_on_time":{"hours":41000},"smart_status":{"passed":true},"ata_smart_attributes":{"table":[{"name":"Reallocated_Sector_Ct","raw":{"value":0}}]}}`
	storcliFullRead9 = `{"smartctl":{"exit_status":0},"model_name":"ST4000NM0023","serial_number":"Z1Z0BBBB","temperature":{"current":36},"power_on_time":{"hours":41002},"smart_status":{"passed":true},"ata_smart_attributes":{"table":[{"name":"Reallocated_Sector_Ct","raw":{"value":0}}]}}`
)

func TestStorcliParseDrives(t *testing.T) {
	drives, err := parseStorcliDrives([]byte(storcliDrivesFixture), 0)
	if err != nil {
		t.Fatalf("parse drives: %v", err)
	}
	if len(drives) != 3 {
		t.Fatalf("drives = %d, want 3: %+v", len(drives), drives)
	}
	if drives[0].did != 8 || drives[0].slot != "252:0" || drives[0].serial != "Z1Z0AAAA" || drives[0].model != "ST4000NM0023" {
		t.Fatalf("first drive identity fields = %+v", drives[0])
	}
	if drives[0].tempC != 34 || drives[0].mediaErr != 2 || drives[0].otherErr != 1 || drives[0].state != "Onln" {
		t.Fatalf("first drive state fields = %+v", drives[0])
	}
	if drives[1].did != 9 || drives[1].predFail != 1 || !drives[1].smartAlert {
		t.Fatalf("second drive fields = %+v", drives[1])
	}
	if drives[2].did != 10 || drives[2].slot != "s4" || drives[2].serial != "SSD123" || drives[2].tempC != 0 || drives[2].state != "JBOD" {
		t.Fatalf("no-enclosure drive fields = %+v", drives[2])
	}
}

func TestStorcliParseVDs(t *testing.T) {
	vds, err := parseStorcliVDs([]byte(storcliVDsFixture), 0)
	if err != nil {
		t.Fatalf("parse vds: %v", err)
	}
	if len(vds) != 2 {
		t.Fatalf("vds = %d, want 2: %+v", len(vds), vds)
	}
	if vds[0] != (storcliVD{ctl: 0, vd: 0, typ: "RAID5", state: "Optl", node: "/dev/sda"}) {
		t.Fatalf("optimal vd = %+v", vds[0])
	}
	if vds[1] != (storcliVD{ctl: 0, vd: 1, typ: "RAID1", state: "Dgrd", node: "/dev/sdb"}) {
		t.Fatalf("degraded vd = %+v", vds[1])
	}
}

func TestStorcliTempParse(t *testing.T) {
	cases := map[string]int{
		" 34C (93.20 F)": 34,
		"34C":            34,
		"N/A":            0,
		"":               0,
	}
	for input, want := range cases {
		if got := parseStorcliTemp(input); got != want {
			t.Errorf("parseStorcliTemp(%q) = %d, want %d", input, got, want)
		}
	}
}

func storcliProbeFake(identity8, identity9 string) *fakeSmartctl {
	return &fakeSmartctl{responses: map[string]string{
		"show ctrlcount J":                              storcliCtrlCountFixture,
		"/c0/eall/sall show all J":                      storcliProbeDrivesFixture,
		"/c0/vall show all J":                           storcliVDsFixture,
		"-i --json=c /dev/sda":                          fakeVDIdentity,
		"-i --json=c -d megaraid,8 /dev/sda":            identity8,
		"-i --json=c -d megaraid,9 /dev/sda":            identity9,
		"-i --json=c -d megaraid,10 /dev/sda":           `{"smartctl":{"exit_status":2}}`,
		"-a --json=c -l devstat -d megaraid,8 /dev/sda": storcliFullRead8,
		"-a --json=c -l devstat -d megaraid,9 /dev/sda": storcliFullRead9,
	}, fallback: `{"smartctl":{"exit_status":2}}`}
}

func discoverWithStorcli(c *smartCollector) {
	scanned := []smartDevice{{name: "/dev/sda", devType: "scsi"}}
	c.scanned = scanned
	c.devicesAt = time.Now()
	c.raidRunAt = time.Now()
	c.discoverRAID(c.smartctl, "linux", scanned, false, scanned)
}

func TestProbeWithStorcliPromotesDrives(t *testing.T) {
	f := storcliProbeFake(fakeSASDisk8, fakeSASDisk9)
	c := newRAIDTestCollector(f)
	c.storcli = "/fake/storcli64"
	discoverWithStorcli(c)
	if len(c.raidExtra) != 2 {
		t.Fatalf("raidExtra = %+v, want 2 drives", c.raidExtra)
	}
	if len(c.cliDrives) != 2 || c.cliDrives[0].cliOnly || c.cliDrives[1].cliOnly {
		t.Fatalf("cached storcli drives = %+v, want 2 readable drives", c.cliDrives)
	}
	if c.raidExtra[0].slot != "252:0" || c.raidExtra[1].slot != "252:1" {
		t.Fatalf("slot labels = %+v", c.raidExtra)
	}
	if !c.raidHide["/dev/sda"] {
		t.Fatalf("virtual disk was not hidden: %v", c.raidHide)
	}
	if c.raidNote != "" {
		t.Fatalf("raid note = %q, want empty", c.raidNote)
	}
	if f.called("-i --json=c -d megaraid,0 /dev/sda") {
		t.Fatalf("storcli-covered controller must not be blindly walked")
	}
	points, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	byDevice := map[string]map[metrics.ID]float64{}
	mediaCount := map[string]int{}
	for _, point := range points {
		device := point.Labels["device"]
		if device == "" {
			continue
		}
		if byDevice[device] == nil {
			byDevice[device] = map[metrics.ID]float64{}
		}
		byDevice[device][point.Metric] = point.Value
		if point.Metric == metrics.SmartMediaErrors {
			mediaCount[device]++
		}
	}
	if byDevice["sda#8"][metrics.SmartTempC] != 34 || byDevice["sda#9"][metrics.SmartTempC] != 36 {
		t.Fatalf("smartctl temperatures = %v", byDevice)
	}
	media8, hasMedia8 := byDevice["sda#8"][metrics.SmartMediaErrors]
	media9, hasMedia9 := byDevice["sda#9"][metrics.SmartMediaErrors]
	if !hasMedia8 || !hasMedia9 || media8 != 2 || media9 != 0 || mediaCount["sda#8"] != 1 || mediaCount["sda#9"] != 1 {
		t.Fatalf("storcli media errors = %v", byDevice)
	}
	if byDevice["sda#9"][metrics.SmartHealthy] != 1 {
		t.Fatalf("sda#9 health = %v, want smartctl-derived 1", byDevice["sda#9"][metrics.SmartHealthy])
	}
	if c.Status().State != smartStateOK {
		t.Fatalf("state = %q, want ok", c.Status().State)
	}
}

func TestProbeWithStorcliUsesScannedMegaRAIDDrive(t *testing.T) {
	f := storcliProbeFake(fakeSASDisk8, fakeSASDisk9)
	f.responses["-a --json=c -l devstat -d megaraid,8 /dev/bus/0"] = storcliFullRead8
	c := newRAIDTestCollector(f)
	c.storcli = "/fake/storcli64"
	scanned := []smartDevice{
		{name: "/dev/sda", devType: "scsi"},
		{name: "/dev/bus/0", devType: "megaraid,8"},
	}
	c.scanned = scanned
	c.devicesAt = time.Now()
	c.raidRunAt = time.Now()
	c.discoverRAID(c.smartctl, "linux", scanned[:1], false, scanned)
	for _, drive := range c.raidExtra {
		if drive.devType == "megaraid,8" {
			t.Fatalf("scanned DID 8 duplicated in raidExtra: %+v", c.raidExtra)
		}
	}
	if f.called("-i --json=c -d megaraid,8 /dev/sda") {
		t.Fatalf("scanned DID 8 was identity-probed through the virtual disk")
	}
	var cached *storcliDrive
	for i := range c.cliDrives {
		if c.cliDrives[i].did == 8 {
			cached = &c.cliDrives[i]
			break
		}
	}
	if cached == nil || cached.node != "/dev/bus/0" || cached.cliOnly {
		t.Fatalf("cached DID 8 = %+v, want readable /dev/bus/0", cached)
	}
	points, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	mediaCount := 0
	for _, point := range points {
		if point.Metric == metrics.SmartMediaErrors && point.Labels["device"] == "bus/0#8" {
			mediaCount++
		}
	}
	if mediaCount != 1 {
		t.Fatalf("bus/0#8 media-error points = %d, want 1: %+v", mediaCount, points)
	}
}

func TestProbeWithStorcliCliOnlyFallback(t *testing.T) {
	f := storcliProbeFake(`{"smartctl":{"exit_status":2}}`, `{"smartctl":{"exit_status":2}}`)
	c := newRAIDTestCollector(f)
	c.storcli = "/fake/storcli64"
	discoverWithStorcli(c)
	if len(c.cliDrives) != 2 {
		t.Fatalf("cliDrives = %+v, want 2", c.cliDrives)
	}
	if !c.cliDrives[0].cliOnly || !c.cliDrives[1].cliOnly {
		t.Fatalf("cli-only markers = %+v", c.cliDrives)
	}
	pts, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	byDevice := map[string]map[metrics.ID]float64{}
	slots := map[string]string{}
	for _, point := range pts {
		device := point.Labels["device"]
		if device == "" {
			continue
		}
		if byDevice[device] == nil {
			byDevice[device] = map[metrics.ID]float64{}
		}
		byDevice[device][point.Metric] = point.Value
		slots[device] = point.Labels["slot"]
	}
	for _, device := range []string{"sda#8", "sda#9"} {
		if _, ok := byDevice[device][metrics.SmartTempC]; !ok {
			t.Fatalf("%s has no temperature point: %v", device, byDevice[device])
		}
		if _, ok := byDevice[device][metrics.SmartHealthy]; !ok {
			t.Fatalf("%s has no health point: %v", device, byDevice[device])
		}
		if _, ok := byDevice[device][metrics.SmartMediaErrors]; !ok {
			t.Fatalf("%s has no media-error point: %v", device, byDevice[device])
		}
	}
	if slots["sda#8"] != "252:0" || slots["sda#9"] != "252:1" {
		t.Fatalf("slot labels = %v", slots)
	}
	if c.Status().State != smartStateOK {
		t.Fatalf("state = %q, want ok", c.Status().State)
	}
}

func TestProbeWithStorcliNoVDNodeFallback(t *testing.T) {
	f := storcliProbeFake(fakeSASDisk8, fakeSASDisk9)
	f.responses["/c0/vall show all J"] = storcliVDsNoNodeFixture
	c := newRAIDTestCollector(f)
	c.storcli = "/fake/storcli64"
	discoverWithStorcli(c)
	if len(c.raidExtra) != 2 {
		t.Fatalf("raidExtra = %+v, want 2 drives", c.raidExtra)
	}
	if c.raidExtra[0].name != "/dev/sda" || c.raidExtra[1].name != "/dev/sda" {
		t.Fatalf("fallback nodes = %+v", c.raidExtra)
	}
	if len(c.cliDrives) != 2 || c.cliDrives[0].cliOnly || c.cliDrives[1].cliOnly {
		t.Fatalf("cached storcli drives = %+v, want readable fallback drives", c.cliDrives)
	}
	points, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	devices := map[string]bool{}
	for _, point := range points {
		if device := point.Labels["device"]; device != "" {
			devices[device] = true
		}
	}
	if !devices["sda#8"] || !devices["sda#9"] || devices["c0#8"] || devices["c0#9"] {
		t.Fatalf("device labels = %v", devices)
	}
}

func TestStorcliVDDegradedPoint(t *testing.T) {
	f := &fakeSmartctl{}
	c := newRAIDTestCollector(f)
	c.scanned = []smartDevice{}
	c.devicesAt = time.Now()
	c.raidRunAt = time.Now()
	c.cliVDs = []storcliVD{{ctl: 0, vd: 0, typ: "RAID5", state: "Dgrd"}}
	c.cliAt = time.Now()
	points, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	for _, point := range points {
		if point.Metric == metrics.RaidDegraded && point.Value == 1 && point.Labels["array"] == "c0/v0" && point.Labels["type"] == "RAID5" {
			return
		}
	}
	t.Fatalf("degraded VD point missing: %+v", points)
}

func TestStorcliHealthyMapping(t *testing.T) {
	cases := []struct {
		name  string
		drive storcliDrive
		want  float64
	}{
		{"predictive failure", storcliDrive{state: "Onln", predFail: 1}, 0},
		{"smart alert", storcliDrive{state: "Onln", smartAlert: true}, 0},
		{"failed state", storcliDrive{state: "Failed"}, 0},
		{"offline state", storcliDrive{state: "Offln"}, 0},
		{"missing state", storcliDrive{state: "Msng"}, 0},
		{"media errors only", storcliDrive{state: "Onln", mediaErr: 8}, 1},
		{"clean online", storcliDrive{state: "Onln"}, 1},
	}
	for _, tc := range cases {
		points := storcliDrivePoints(time.Now(), tc.drive)
		values := indexPoints(points)
		if values[metrics.SmartHealthy] != tc.want {
			t.Errorf("%s healthy = %v, want %v", tc.name, values[metrics.SmartHealthy], tc.want)
		}
	}
}

func TestProbePermissionClassification(t *testing.T) {
	f := &fakeSmartctl{responses: map[string]string{
		"-i --json=c /dev/sda": fakeVDIdentity,
	}, fallback: `{"smartctl":{"exit_status":2,"messages":[{"string":"Smartctl open device: /dev/sda failed: Permission denied","severity":"error"}]}}`}
	c := newRAIDTestCollector(f)
	_, _, note, _, _ := c.probeRAIDPassthrough(context.Background(), c.smartctl, "linux", []smartDevice{{name: "/dev/sda", devType: "scsi"}}, false, []smartDevice{{name: "/dev/sda", devType: "scsi"}})
	if !strings.Contains(note, "udev") || !strings.Contains(note, "Permission denied") {
		t.Fatalf("permission note = %q", note)
	}
}

func TestProbeNoNodeClassification(t *testing.T) {
	f := &fakeSmartctl{responses: map[string]string{
		"-i --json=c /dev/sda": fakeVDIdentity,
	}, fallback: `{"smartctl":{"exit_status":2,"messages":[{"string":"cannot open /dev/megaraid_sas_ioctl_node: No such file or directory","severity":"error"}]}}`}
	c := newRAIDTestCollector(f)
	_, _, note, _, _ := c.probeRAIDPassthrough(context.Background(), c.smartctl, "linux", []smartDevice{{name: "/dev/sda", devType: "scsi"}}, false, []smartDevice{{name: "/dev/sda", devType: "scsi"}})
	if !strings.Contains(note, "kernel driver may predate passthrough support") || !strings.Contains(note, "No such file or directory") {
		t.Fatalf("missing-node note = %q", note)
	}
}
