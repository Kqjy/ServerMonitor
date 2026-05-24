package version

import "testing"

func TestIsNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.2.0", "0.1.0", true},
		{"0.1.0", "0.2.0", false},
		{"0.1.0", "0.1.0", false},
		{"1.0.0", "0.9.9", true},
		{"0.10.0", "0.9.9", true},
		{"0.1.1", "0.1.0", true},
		{"v0.2.0", "v0.1.0", true},
		{"0.2.0", "v0.2.0", false},
		{"0.3.0-rc1", "0.2.0", true},
		{"0.3.0", "0.3.0-rc1", true},
		{"0.3.0-rc1", "0.3.0", false},
		{"0.3.0-rc2", "0.3.0-rc1", true},
		{"0.3.0-rc1", "0.3.0-rc2", false},
		{"0.3.0-rc.10", "0.3.0-rc.2", true},
		{"0.1.0", "", true},
		{"", "0.1.0", false},
		{"", "", false},
		{"0.1.0+meta", "0.1.0", false},
	}
	for _, c := range cases {
		if got := IsNewer(c.a, c.b); got != c.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestIsNewerRejectsDowngrade(t *testing.T) {
	if IsNewer("0.1.0", "0.3.0") {
		t.Fatal("server-reported older version must not be treated as newer (would trigger auto-downgrade)")
	}
}
