package api

import (
	"testing"
	"time"

	"servermonitor/internal/server/archive"
)

func TestRollupBoundary(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()

	step := 30
	if b := rollupBoundary(now, now.Add(-2*time.Hour), now, &step); !b.IsZero() || step != 30 {
		t.Fatalf("short range should stay on raw: zero=%v step=%d", b.IsZero(), step)
	}

	step = 120
	b := rollupBoundary(now, now.Add(-24*time.Hour), now, &step)
	if b.IsZero() {
		t.Fatal("24h range should route to the rollup")
	}
	if step != caStepMin {
		t.Fatalf("sub-rollup step should be raised to %d, got %d", caStepMin, step)
	}
	if b.Unix()%int64(caStepMin) != 0 {
		t.Fatalf("boundary %d not aligned to step grid %d", b.Unix(), caStepMin)
	}
	if !b.After(now.Add(-24*time.Hour)) || !b.Before(now) {
		t.Fatalf("boundary %v outside the query window", b)
	}
	if now.Sub(b) < rollupLiveTail {
		t.Fatalf("live tail %v shorter than %v", now.Sub(b), rollupLiveTail)
	}

	step = 600
	b2 := rollupBoundary(now, now.Add(-72*time.Hour), now, &step)
	if b2.IsZero() || step != 600 {
		t.Fatalf("coarse step should be preserved: zero=%v step=%d", b2.IsZero(), step)
	}
	if b2.Unix()%600 != 0 {
		t.Fatalf("boundary %d not aligned to step grid 600", b2.Unix())
	}
	if now.Sub(b2) < rollupLiveTail {
		t.Fatalf("live tail %v shorter than %v", now.Sub(b2), rollupLiveTail)
	}
}

func TestBatchBucketMerge(t *testing.T) {
	seamAvg := &batchBucket{}
	seamAvg.addAvg(30, 3)
	seamAvg.addAvg(20, 1)
	if got := seamAvg.value(false); got != 12.5 {
		t.Fatalf("cross-tier avg merge = %v, want 12.5", got)
	}

	seamMax := &batchBucket{}
	seamMax.addMax(7)
	seamMax.addMax(12)
	seamMax.addMax(9)
	if got := seamMax.value(true); got != 12 {
		t.Fatalf("cross-tier max merge = %v, want 12", got)
	}

	oneAvg := &batchBucket{}
	oneAvg.addAvg(50, 5)
	if got := oneAvg.value(false); got != 10 {
		t.Fatalf("single-tier avg = %v, want 10 (must equal avg(value))", got)
	}

	oneMax := &batchBucket{}
	oneMax.addMax(42)
	if got := oneMax.value(true); got != 42 {
		t.Fatalf("single-tier max = %v, want 42 (must equal max(value))", got)
	}

	if got := (&batchBucket{}).value(false); got != 0 {
		t.Fatalf("empty avg bucket = %v, want 0", got)
	}
	if got := (&batchBucket{}).value(true); got != 0 {
		t.Fatalf("empty max bucket = %v, want 0", got)
	}
}

func TestParseSeriesGroupSpecs(t *testing.T) {
	specs, err := parseSeriesGroupSpecs([]string{"cpu_total_pct", "cpu_core_pct:core"})
	if err != nil {
		t.Fatalf("parse group specs: %v", err)
	}
	if len(specs) != 2 {
		t.Fatalf("spec count = %d, want 2", len(specs))
	}
	if specs[0].name != "cpu_total_pct" || specs[0].splitBy != "" {
		t.Fatalf("scalar spec = %#v", specs[0])
	}
	if specs[1].name != "cpu_core_pct" || specs[1].splitBy != "core" {
		t.Fatalf("split spec = %#v", specs[1])
	}

	for _, values := range [][]string{
		nil,
		{"unknown_metric"},
		{"cpu_total_pct", "cpu_total_pct"},
	} {
		if _, err := parseSeriesGroupSpecs(values); err == nil {
			t.Fatalf("parseSeriesGroupSpecs(%v) succeeded, want error", values)
		}
	}
}

func TestSeriesGroupAccumulator(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	scalar := &seriesGroupAcc{
		spec:   seriesGroupSpec{name: "cpu_total_pct"},
		groups: map[string]*labelGroup{},
	}
	scalar.add(map[string]string{"source": "a"}, at, 30, 3)
	scalar.add(map[string]string{"source": "b"}, at, 20, 1)
	if len(scalar.groups) != 1 {
		t.Fatalf("scalar groups = %d, want 1", len(scalar.groups))
	}
	bucket := scalar.groups[""].buckets[at.Unix()]
	if got := bucket.sum / float64(bucket.n); got != 12.5 {
		t.Fatalf("scalar weighted average = %v, want 12.5", got)
	}

	split := &seriesGroupAcc{
		spec:   seriesGroupSpec{name: "cpu_core_pct", splitBy: "core"},
		groups: map[string]*labelGroup{},
	}
	split.add(map[string]string{"core": "0"}, at, 10, 1)
	split.add(map[string]string{"core": "1"}, at, 20, 1)
	if len(split.groups) != 2 || split.groups["0"] == nil || split.groups["1"] == nil {
		t.Fatalf("split groups = %#v", split.groups)
	}
}

func TestColdRawClamp(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	day := 24 * time.Hour
	coldCutoff := now.Add(-180 * day)
	liveTail := now.Add(-rollupLiveTail)
	forever := now.Add(-100 * 365 * day)
	from := now.Add(-365 * day)

	lo, hi, ok := coldRawClamp(from, forever, coldCutoff, liveTail)
	if !ok {
		t.Fatal("raw=forever long query must read raw for the pre-aggregate cold range")
	}
	if !lo.Equal(from) || !hi.Equal(coldCutoff) {
		t.Fatalf("cold raw window = [%v,%v], want [%v,%v]", lo, hi, from, coldCutoff)
	}

	if _, _, ok := coldRawClamp(from, now.Add(-30*day), coldCutoff, liveTail); ok {
		t.Fatal("raw shorter than aggregate retention must not read cold raw")
	}

	if _, _, ok := coldRawClamp(from, forever, coldCutoff, forever); ok {
		t.Fatal("without rollup promotion the tail raw read already covers the range")
	}

	if _, _, ok := coldRawClamp(now.Add(-3*day), forever, coldCutoff, liveTail); ok {
		t.Fatal("a query newer than coldCutoff has no cold range")
	}

	rawAvail := now.Add(-300 * day)
	lo2, hi2, ok2 := coldRawClamp(from, rawAvail, coldCutoff, liveTail)
	if !ok2 {
		t.Fatal("raw=300d exceeds the 180d aggregate; cold raw read expected")
	}
	if !lo2.Equal(rawAvail) || !hi2.Equal(coldCutoff) {
		t.Fatalf("cold raw window = [%v,%v], want [%v,%v]", lo2, hi2, rawAvail, coldCutoff)
	}
}

func TestColdRawGaps(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	day := 24 * time.Hour
	lo := now.Add(-365 * day)
	hi := now.Add(-180 * day)
	step := time.Duration(caStepMin) * time.Second

	eq := func(got, want [][2]time.Time) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("gaps = %v, want %v", got, want)
		}
		for i := range got {
			if !got[i][0].Equal(want[i][0]) || !got[i][1].Equal(want[i][1]) {
				t.Fatalf("gap[%d] = [%v,%v], want [%v,%v]", i, got[i][0], got[i][1], want[i][0], want[i][1])
			}
		}
	}

	eq(coldRawGaps(lo, hi, nil), [][2]time.Time{{lo, hi}})

	eq(coldRawGaps(lo, hi, []archive.Interval{{From: lo, To: hi}}), nil)

	eq(coldRawGaps(lo, hi, []archive.Interval{{From: now.Add(-300 * day), To: hi}}),
		[][2]time.Time{{lo, now.Add(-300 * day)}})

	eq(coldRawGaps(lo, hi, []archive.Interval{
		{From: lo, To: now.Add(-330 * day)},
		{From: now.Add(-250 * day), To: hi},
	}), [][2]time.Time{{now.Add(-330 * day).Add(step), now.Add(-250 * day)}})

	eq(coldRawGaps(lo, hi, []archive.Interval{{From: now.Add(-400 * day), To: hi}}), nil)

	eq(coldRawGaps(lo, hi, []archive.Interval{
		{From: lo, To: now.Add(-330 * day)},
		{From: now.Add(-330 * day).Add(step), To: hi},
	}), nil)

	eq(coldRawGaps(lo, hi, []archive.Interval{{From: lo, To: now.Add(-250 * day)}}),
		[][2]time.Time{{now.Add(-250 * day).Add(step), hi}})
}
