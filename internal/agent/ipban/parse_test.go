package ipban

import "testing"

func TestParseSSHDNormal(t *testing.T) {
	cases := []struct {
		line    string
		ip      string
		user    string
		pattern string
	}{
		{"Failed password for root from 203.0.113.5 port 4242 ssh2", "203.0.113.5", "root", "failed_auth"},
		{"Failed password for invalid user admin from 203.0.113.5 port 4242 ssh2", "203.0.113.5", "admin", "failed_auth"},
		{"Failed publickey for deploy from 2001:db8::5 port 50000 ssh2: RSA SHA256:abc", "2001:db8::5", "deploy", "failed_auth"},
		{"Failed keyboard-interactive/pam for invalid user test user from 198.51.100.7 port 3333 ssh2", "198.51.100.7", "test user", "failed_auth"},
		{"Failed none for invalid user  from 198.51.100.9 port 1 ssh2", "198.51.100.9", "", "failed_auth"},
		{"Failed password for git from ::ffff:203.0.113.9 port 22 ssh2", "203.0.113.9", "git", "failed_auth"},
		{"Invalid user oracle from 203.0.113.10 port 51234", "203.0.113.10", "oracle", "invalid_user"},
		{"Invalid user  from 203.0.113.11 port 51234", "203.0.113.11", "", "invalid_user"},
		{"Connection closed by invalid user admin 203.0.113.12 port 40000 [preauth]", "203.0.113.12", "admin", "preauth_close"},
		{"Connection closed by authenticating user root 203.0.113.13 port 40001 [preauth]", "203.0.113.13", "root", "preauth_close"},
		{"Disconnected from invalid user ubuntu 203.0.113.14 port 40002 [preauth]", "203.0.113.14", "ubuntu", "preauth_close"},
		{"Disconnected from authenticating user root 203.0.113.15 port 40003 [preauth]", "203.0.113.15", "root", "preauth_close"},
		{"Disconnecting invalid user pi 203.0.113.16 port 40004: Too many authentication failures [preauth]", "203.0.113.16", "pi", "too_many_failures"},
		{"error: maximum authentication attempts exceeded for root from 203.0.113.17 port 40005 ssh2 [preauth]", "203.0.113.17", "root", "max_attempts"},
		{"error: maximum authentication attempts exceeded for invalid user admin from 203.0.113.18 port 40006 ssh2 [preauth]", "203.0.113.18", "admin", "max_attempts"},
		{"error: PAM: Authentication failure for root from 203.0.113.19", "203.0.113.19", "root", "pam_failure"},
		{"error: PAM: User not known to the underlying authentication module for illegal user test from 203.0.113.20", "203.0.113.20", "test", "pam_failure"},
		{"User backup from 203.0.113.21 not allowed because not listed in AllowUsers", "203.0.113.21", "backup", "not_allowed"},
		{"Received disconnect from 203.0.113.22 port 40007:3: com.jcraft.jsch.JSchException: Auth fail [preauth]", "203.0.113.22", "", "auth_fail_disconnect"},
	}
	for _, c := range cases {
		m, ok := ParseSSHD(c.line, false)
		if !ok {
			t.Errorf("%q: expected a match", c.line)
			continue
		}
		if m.IP.String() != c.ip {
			t.Errorf("%q: ip %s, want %s", c.line, m.IP, c.ip)
		}
		if m.User != c.user {
			t.Errorf("%q: user %q, want %q", c.line, m.User, c.user)
		}
		if m.Pattern != c.pattern {
			t.Errorf("%q: pattern %s, want %s", c.line, m.Pattern, c.pattern)
		}
		if m.Aggressive {
			t.Errorf("%q: should not be aggressive", c.line)
		}
	}
}

func TestParseSSHDAggressiveOnlyWhenEnabled(t *testing.T) {
	lines := []string{
		"Received disconnect from 203.0.113.30 port 40010:11: Bye Bye [preauth]",
		"Connection reset by 203.0.113.31 port 40011 [preauth]",
		"Connection reset by invalid user admin 203.0.113.32 port 40012 [preauth]",
		"Connection closed by 203.0.113.33 port 40013 [preauth]",
		"Unable to negotiate with 203.0.113.34 port 40014: no matching key exchange method found. Their offer: diffie-hellman-group1-sha1 [preauth]",
		"banner exchange: Connection from 203.0.113.35 port 40015: invalid format",
		"Bad protocol version identification 'GET / HTTP/1.1' from 203.0.113.36 port 40016",
		"Did not receive identification string from 203.0.113.37 port 40017",
		"Timeout before authentication for 203.0.113.38 port 40018",
	}
	for _, line := range lines {
		if _, ok := ParseSSHD(line, false); ok {
			t.Errorf("%q: matched in normal mode", line)
		}
		m, ok := ParseSSHD(line, true)
		if !ok {
			t.Errorf("%q: expected an aggressive match", line)
			continue
		}
		if !m.Aggressive {
			t.Errorf("%q: expected aggressive flag", line)
		}
		if !m.IP.IsValid() {
			t.Errorf("%q: invalid ip", line)
		}
	}
}

func TestParseSSHDIgnoresNoise(t *testing.T) {
	lines := []string{
		"Accepted publickey for deploy from 203.0.113.40 port 40020 ssh2: ED25519 SHA256:xyz",
		"pam_unix(sshd:session): session opened for user deploy(uid=1000) by (uid=0)",
		"Server listening on 0.0.0.0 port 22.",
		"Received disconnect from 203.0.113.41 port 40021:11: disconnected by user",
		"Disconnected from user deploy 203.0.113.42 port 40022",
		"Failed password for root from not-an-ip port 1 ssh2",
		"",
	}
	for _, line := range lines {
		if m, ok := ParseSSHD(line, true); ok {
			t.Errorf("%q: unexpected match %+v", line, m)
		}
	}
}

func TestStripSyslogPrefix(t *testing.T) {
	cases := []struct {
		line string
		want string
		ok   bool
	}{
		{"Aug 21 13:35:01 web01 sshd[1234]: Failed password for root from 203.0.113.5 port 1 ssh2", "Failed password for root from 203.0.113.5 port 1 ssh2", true},
		{"2026-08-21T13:35:01.123456+00:00 web01 sshd-session[99]: Invalid user x from 203.0.113.6 port 2", "Invalid user x from 203.0.113.6 port 2", true},
		{"Aug 21 13:35:01 web01 sshd: Invalid user y from 203.0.113.7 port 3", "Invalid user y from 203.0.113.7 port 3", true},
		{"Aug 21 13:35:01 web01 systemd[1]: Started OpenBSD Secure Shell server.", "", false},
		{"Aug 21 13:35:01 web01 CRON[5]: pam_unix(cron:session): session opened", "", false},
		{"Aug 21 13:35:01 web01 mysshd[5]: Failed password for root from 203.0.113.8 port 1 ssh2", "", false},
	}
	for _, c := range cases {
		got, ok := StripSyslogPrefix(c.line)
		if ok != c.ok || got != c.want {
			t.Errorf("%q: got (%q, %v), want (%q, %v)", c.line, got, ok, c.want, c.ok)
		}
	}
}
