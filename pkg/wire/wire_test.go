package wire

import (
	"encoding/json"
	"testing"
)

func TestExternallyManagedPresenceTracking(t *testing.T) {
	managed, unmanaged := true, false

	encTrue, err := json.Marshal(HostInfo{ExternallyManaged: &managed})
	if err != nil {
		t.Fatal(err)
	}
	encFalse, err := json.Marshal(HostInfo{ExternallyManaged: &unmanaged})
	if err != nil {
		t.Fatal(err)
	}

	var gotTrue HostInfo
	if err := json.Unmarshal(encTrue, &gotTrue); err != nil {
		t.Fatal(err)
	}
	if gotTrue.ExternallyManaged == nil || !*gotTrue.ExternallyManaged {
		t.Fatalf("managed=true must round-trip as non-nil true, got %v", gotTrue.ExternallyManaged)
	}

	var gotFalse HostInfo
	if err := json.Unmarshal(encFalse, &gotFalse); err != nil {
		t.Fatal(err)
	}
	if gotFalse.ExternallyManaged == nil || *gotFalse.ExternallyManaged {
		t.Fatalf("managed=false must round-trip as non-nil false so a real unmanage flips the host, got %v", gotFalse.ExternallyManaged)
	}

	var legacy HostInfo
	if err := json.Unmarshal([]byte(`{"hostname":"old","os":"linux","agent_version":"0.1.8"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.ExternallyManaged != nil {
		t.Fatalf("a payload predating the field must decode as nil so Touch leaves the DB value untouched, got %v", *legacy.ExternallyManaged)
	}
}
