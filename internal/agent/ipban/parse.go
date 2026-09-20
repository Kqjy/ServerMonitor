package ipban

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"time"
)

type Failure struct {
	Time   time.Time
	IP     netip.Addr
	User   string
	Source string
}

type Match struct {
	IP         netip.Addr
	User       string
	Pattern    string
	Aggressive bool
}

type sshdPattern struct {
	name       string
	re         *regexp.Regexp
	aggressive bool
}

const addrGroup = `(?P<ip>[0-9A-Fa-f:.]+)`

var sshdPatterns = []sshdPattern{
	{name: "failed_auth", re: regexp.MustCompile(`^Failed (?:password|publickey|keyboard-interactive(?:/pam)?|none|hostbased|gssapi(?:-with-mic|-keyex)?) for (?:invalid user )?(?P<user>.*) from ` + addrGroup + ` port \d+(?: ssh2.*)?$`)},
	{name: "invalid_user", re: regexp.MustCompile(`^Invalid user (?P<user>.*?) from ` + addrGroup + `(?: port \d+)?$`)},
	{name: "preauth_close", re: regexp.MustCompile(`^(?:Connection closed|Disconnected) (?:by|from) (?:invalid|authenticating) user (?P<user>.*?) ` + addrGroup + ` port \d+`)},
	{name: "too_many_failures", re: regexp.MustCompile(`^Disconnecting (?:invalid|authenticating) user (?P<user>.*?) ` + addrGroup + ` port \d+: Too many authentication failures`)},
	{name: "max_attempts", re: regexp.MustCompile(`^error: maximum authentication attempts exceeded for (?:invalid user )?(?P<user>.*?) from ` + addrGroup + ` port \d+`)},
	{name: "pam_failure", re: regexp.MustCompile(`^(?:error: )?PAM: (?:Authentication failure|User not known to the underlying authentication module) for (?:illegal user )?(?P<user>.*?) from ` + addrGroup + `$`)},
	{name: "pam_unix_failure", re: regexp.MustCompile(`^pam_unix\(sshd:auth\): authentication failure;(?: [^;\r\n]*)* rhost=` + addrGroup + `(?: user=(?P<user>[^\s;]*))?(?: [^;\r\n]*)*$`)},
	{name: "not_allowed", re: regexp.MustCompile(`^User (?P<user>\S+) from ` + addrGroup + ` not allowed because`)},
	{name: "auth_fail_disconnect", re: regexp.MustCompile(`^(?:error: )?Received disconnect from ` + addrGroup + ` port \d+:3: .*: Auth fail \[preauth\]`)},
	{name: "preauth_disconnect", re: regexp.MustCompile(`^(?:error: )?Received disconnect from ` + addrGroup + ` port \d+:\d+: .*\[preauth\]`), aggressive: true},
	{name: "preauth_reset", re: regexp.MustCompile(`^Connection reset by (?:(?:invalid|authenticating) user (?P<user>.*?) )?` + addrGroup + ` port \d+ \[preauth\]`), aggressive: true},
	{name: "preauth_close_anon", re: regexp.MustCompile(`^Connection closed by ` + addrGroup + ` port \d+ \[preauth\]`), aggressive: true},
	{name: "no_negotiation", re: regexp.MustCompile(`^Unable to negotiate with ` + addrGroup + ` port \d+: no matching (?:key exchange method|host key type|cipher|MAC) found`), aggressive: true},
	{name: "bad_banner", re: regexp.MustCompile(`^banner exchange: Connection from ` + addrGroup + ` port \d+: invalid format`), aggressive: true},
	{name: "bad_protocol", re: regexp.MustCompile(`^Bad protocol version identification '.*' from ` + addrGroup + ` port \d+`), aggressive: true},
	{name: "no_ident", re: regexp.MustCompile(`^Did not receive identification string from ` + addrGroup + `(?: port \d+)?`), aggressive: true},
	{name: "auth_timeout", re: regexp.MustCompile(`^Timeout before authentication for ` + addrGroup + `(?: port \d+)?`), aggressive: true},
}

func ParseSSHD(message string, aggressive bool) (Match, bool) {
	message = strings.TrimSpace(message)
	for _, p := range sshdPatterns {
		if p.aggressive && !aggressive {
			continue
		}
		sub := p.re.FindStringSubmatch(message)
		if sub == nil {
			continue
		}
		var ipText, user string
		for i, name := range p.re.SubexpNames() {
			switch name {
			case "ip":
				ipText = sub[i]
			case "user":
				user = sub[i]
			}
		}
		ip, err := netip.ParseAddr(ipText)
		if err != nil {
			continue
		}
		if len(user) > 256 || strings.IndexFunc(user, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
			continue
		}
		return Match{IP: ip.Unmap(), User: user, Pattern: p.name, Aggressive: p.aggressive}, true
	}
	return Match{}, false
}

var syslogSSHDLine = regexp.MustCompile(`^(?P<time>(?:[A-Z][a-z]{2}\s+[ 0-9][0-9]\s+[0-9]{2}:[0-9]{2}:[0-9]{2}|[0-9]{4}-[0-9]{2}-[0-9]{2}T\S+))\s+\S+\s+(?:sshd|sshd-session)(?:\[[0-9]+\])?:\s(?P<message>.*)$`)

func StripSyslogPrefix(line string) (string, bool) {
	m := syslogSSHDLine.FindStringSubmatch(strings.TrimRight(line, "\r\n"))
	if m == nil {
		return "", false
	}
	for i, name := range syslogSSHDLine.SubexpNames() {
		if name == "message" {
			return m[i], true
		}
	}
	return "", false
}

func ParseSyslogLine(line string, now time.Time) (string, time.Time, bool) {
	m := syslogSSHDLine.FindStringSubmatch(strings.TrimRight(line, "\r\n"))
	if m == nil {
		return "", time.Time{}, false
	}
	var rawTime, message string
	for i, name := range syslogSSHDLine.SubexpNames() {
		switch name {
		case "time":
			rawTime = m[i]
		case "message":
			message = m[i]
		}
	}
	at, err := time.Parse(time.RFC3339Nano, rawTime)
	if err != nil {
		rawTime = strings.Join(strings.Fields(rawTime), " ")
		at, err = time.ParseInLocation("Jan 2 15:04:05 2006", rawTime+fmt.Sprintf(" %d", now.Year()), now.Location())
		if err == nil && at.After(now.Add(24*time.Hour)) {
			at = at.AddDate(-1, 0, 0)
		}
	}
	if err != nil {
		return "", time.Time{}, false
	}
	return message, at, true
}
