package storage

import "testing"

func TestArchiveOwnsAggregateRetention(t *testing.T) {
	p := RetentionPolicies{Aggregate5m: "6 months", ArchiveAggregate5m: true}
	if shouldInstallPolicy(p, "metric_points_5m", "retention", p.Aggregate5m) {
		t.Fatal("archive ownership installed an independent Timescale retention policy")
	}
	if !shouldInstallPolicy(p, "metric_points", "retention", "30 days") {
		t.Fatal("archive ownership must not disable unrelated retention policies")
	}
	p.ArchiveAggregate5m = false
	if !shouldInstallPolicy(p, "metric_points_5m", "retention", p.Aggregate5m) {
		t.Fatal("Timescale must own aggregate retention when the archive is disabled")
	}
}
