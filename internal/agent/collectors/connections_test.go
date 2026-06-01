package collectors

import "testing"

func TestPortOwnerStateLatch(t *testing.T) {
	c := &connCollector{}

	if c.updateOwnerStateLocked(3, 5) {
		t.Fatalf("majority-resolved tick should not warn")
	}
	if c.state != connStateOK {
		t.Fatalf("state = %q, want %q", c.state, connStateOK)
	}

	if !c.updateOwnerStateLocked(0, 5) {
		t.Fatalf("first zero-owner tick should warn")
	}
	if c.state != connStateNoOwners {
		t.Fatalf("state = %q, want %q", c.state, connStateNoOwners)
	}
	if c.stateMsg == "" {
		t.Fatalf("zero-owner state must carry a diagnostic message")
	}

	if c.updateOwnerStateLocked(0, 7) {
		t.Fatalf("repeated bad tick must not warn again")
	}

	if c.updateOwnerStateLocked(6, 7) {
		t.Fatalf("recovery tick should not warn")
	}
	if c.state != connStateOK || c.warned {
		t.Fatalf("recovery must reset state to ok and clear the warned latch")
	}

	if !c.updateOwnerStateLocked(1, 100) {
		t.Fatalf("first mostly-unresolved tick should warn")
	}
	if c.state != connStatePartialOwners {
		t.Fatalf("state = %q, want %q", c.state, connStatePartialOwners)
	}
	if c.stateMsg == "" {
		t.Fatalf("partial-owner state must carry a diagnostic message")
	}

	if c.updateOwnerStateLocked(50, 100) {
		t.Fatalf("exactly half resolved should recover to ok without warning")
	}
	if c.state != connStateOK || c.warned {
		t.Fatalf("half-resolved must be ok and clear the warned latch")
	}

	if !c.updateOwnerStateLocked(49, 100) {
		t.Fatalf("just-under-half should warn on the partial transition")
	}
	if c.state != connStatePartialOwners {
		t.Fatalf("state = %q, want %q", c.state, connStatePartialOwners)
	}

	if c.updateOwnerStateLocked(0, 0) {
		t.Fatalf("no listening sockets should not warn")
	}
	if c.state != connStateOK {
		t.Fatalf("zero-socket state = %q, want %q", c.state, connStateOK)
	}
}
