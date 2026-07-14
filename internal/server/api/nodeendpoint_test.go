package api

import "testing"

func TestNormalizeNodeEndpoint(t *testing.T) {
	cases := []struct {
		name         string
		endpoint     string
		udpPort      int
		wantEndpoint string
		wantPort     int
		wantErr      bool
	}{
		{"host only defaults port", "nas-01.lan", 0, "nas-01.lan", 51821, false},
		{"trims and keeps custom port", "  1.2.3.4  ", 51830, "1.2.3.4", 51830, false},
		{"embedded port preserved", "host.example:9000", 0, "host.example:9000", 51821, false},
		{"empty endpoint rejected", "", 0, "", 0, true},
		{"whitespace endpoint rejected", "   ", 0, "", 0, true},
		{"missing host rejected", ":51830", 0, "", 0, true},
		{"port too high rejected", "nas-01.lan", 70000, "", 0, true},
		{"port negative rejected", "nas-01.lan", -1, "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			endpoint, port, msg := normalizeNodeEndpoint(tc.endpoint, tc.udpPort)
			if tc.wantErr {
				if msg == "" {
					t.Fatalf("expected error message, got none (endpoint=%q port=%d)", endpoint, port)
				}
				return
			}
			if msg != "" {
				t.Fatalf("unexpected error: %s", msg)
			}
			if endpoint != tc.wantEndpoint {
				t.Errorf("endpoint = %q, want %q", endpoint, tc.wantEndpoint)
			}
			if port != tc.wantPort {
				t.Errorf("port = %d, want %d", port, tc.wantPort)
			}
		})
	}
}
