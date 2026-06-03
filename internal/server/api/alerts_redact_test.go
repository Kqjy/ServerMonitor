package api

import (
	"encoding/json"
	"testing"
)

func TestRedactChannelConfig(t *testing.T) {
	cases := []struct {
		name       string
		kind       string
		in         string
		wantPass   string
		passExists bool
	}{
		{"smtp blanks password", "smtp", `{"host":"mail","password":"s3cret","from":"a@b.c"}`, "", true},
		{"smtp without password is unchanged", "smtp", `{"host":"mail","from":"a@b.c"}`, "", false},
		{"webhook is untouched", "webhook", `{"url":"https://x/y","format":"discord"}`, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := redactChannelConfig(tc.kind, json.RawMessage(tc.in))
			var m map[string]json.RawMessage
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("output is not valid json: %v", err)
			}
			if tc.kind == "webhook" {
				if string(out) != tc.in {
					t.Fatalf("webhook config must be returned verbatim: got %s", out)
				}
				return
			}
			pw, ok := m["password"]
			if ok != tc.passExists {
				t.Fatalf("password key presence = %v, want %v", ok, tc.passExists)
			}
			if ok && string(pw) != `""` {
				t.Fatalf("password must be blanked, got %s", pw)
			}
			if _, ok := m["host"]; !ok {
				t.Fatal("redaction must preserve non-secret fields (host missing)")
			}
		})
	}
}

func TestRedactChannelConfigInvalidJSON(t *testing.T) {
	in := json.RawMessage(`not json`)
	if got := redactChannelConfig("smtp", in); string(got) != string(in) {
		t.Fatalf("invalid json must be returned as-is, got %s", got)
	}
}
