package api

import (
	"testing"
	"time"

	"servermonitor/internal/server/storage"
)

func TestToDTOUpgradeStalled(t *testing.T) {
	within := time.Now().Add(-time.Minute)
	past := time.Now().Add(-upgradeStallWindow - time.Minute)
	cases := []struct {
		name  string
		since *time.Time
		want  bool
	}{
		{"nil is never stalled", nil, false},
		{"inside window is not stalled", &within, false},
		{"beyond window is stalled", &past, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := toDTO(storage.Host{ID: 1, Hostname: "h", AgentVersion: "0.0.1", UpgradeStallSince: tc.since})
			if d.UpgradeStalled != tc.want {
				t.Fatalf("UpgradeStalled = %v, want %v", d.UpgradeStalled, tc.want)
			}
		})
	}
}

func TestToDTOUpgrading(t *testing.T) {
	now := time.Now()
	stalled := now.Add(-upgradeStallWindow - time.Minute)
	cases := []struct {
		name       string
		agentVer   string
		dispatched *time.Time
		stallSince *time.Time
		want       bool
	}{
		{"dispatched with update available is upgrading", "0.0.1", &now, nil, true},
		{"not dispatched is not upgrading", "0.0.1", nil, nil, false},
		{"dispatched but stalled is not upgrading", "0.0.1", &now, &stalled, false},
		{"dispatched but already current is not upgrading", "999.0.0", &now, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := toDTO(storage.Host{
				ID:                  1,
				Hostname:            "h",
				AgentVersion:        tc.agentVer,
				UpgradeDispatchedAt: tc.dispatched,
				UpgradeStallSince:   tc.stallSince,
			})
			if d.Upgrading != tc.want {
				t.Fatalf("Upgrading = %v, want %v", d.Upgrading, tc.want)
			}
		})
	}
}
