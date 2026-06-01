package collectors

import "testing"

func TestPortOwnerStateLatch(t *testing.T) {
	c := &connCollector{}

	if c.updateOwnerStateLocked(3, 5) {
		t.Fatalf("resolved owners should not warn")
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
		t.Fatalf("repeated zero-owner tick must not warn again")
	}

	if c.updateOwnerStateLocked(2, 7) {
		t.Fatalf("recovery tick should not warn")
	}
	if c.state != connStateOK || c.warned {
		t.Fatalf("recovery must reset state to ok and clear the warned latch")
	}

	if !c.updateOwnerStateLocked(0, 4) {
		t.Fatalf("regression after recovery should warn again")
	}

	if c.updateOwnerStateLocked(0, 0) {
		t.Fatalf("no listening sockets should not warn")
	}
	if c.state != connStateOK {
		t.Fatalf("zero-socket state = %q, want %q", c.state, connStateOK)
	}
}
