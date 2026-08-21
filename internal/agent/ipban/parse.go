package ipban

import (
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
	{name: "failed_auth", re: regexp.MustCompile(`^Failed (?:password|publickey|keyboard-interactive(?:/pam)?|none|hostbased|gssapi(?:-with-mic|-keyex)?) for (?:invalid user )?(?P<user>.*?) from ` + addrGroup + ` port \d+`)},
	{name: "invalid_user", re: regexp.MustCompile(`^Invalid user (?P<user>.*?) from ` + addrGroup + `(?: port \d+)?$`)},
	{name: "preauth_close", re: regexp.MustCompile(`^(?:Connection closed|Disconnected) (?:by|from) (?:invalid|authenticating) user (?P<user>.*?) ` + addrGroup + ` port \d+`)},
	{name: "too_many_failures", re: regexp.MustCompile(`^Disconnecting (?:invalid|authenticating) user (?P<user>.*?) ` + addrGroup + ` port \d+: Too many authentication failures`)},
	{name: "max_attempts", re: regexp.MustCompile(`^error: maximum authentication attempts exceeded for (?:invalid user )?(?P<user>.*?) from ` + addrGroup + ` port \d+`)},
	{name: "pam_failure", re: regexp.MustCompile(`^(?:error: )?PAM: (?:Authentication failure|User not known to the underlying authentication module) for (?:illegal user )?(?P<user>.*?) from ` + addrGroup + `$`)},
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
		return Match{IP: ip.Unmap(), User: user, Pattern: p.name, Aggressive: p.aggressive}, true
	}
	return Match{}, false
}

var sshdIdentifiers = []string{"sshd-session", "sshd"}

func StripSyslogPrefix(line string) (string, bool) {
	for _, ident := range sshdIdentifiers {
		search := 0
		for {
			idx := strings.Index(line[search:], ident)
			if idx < 0 {
				break
			}
			idx += search
			if idx > 0 && line[idx-1] != ' ' {
				search = idx + len(ident)
				continue
			}
			rest := line[idx+len(ident):]
			if strings.HasPrefix(rest, "[") {
				end := strings.Index(rest, "]")
				if end < 0 {
					search = idx + len(ident)
					continue
				}
				rest = rest[end+1:]
			}
			if strings.HasPrefix(rest, ": ") {
				return rest[2:], true
			}
			search = idx + len(ident)
		}
	}
	return "", false
}
