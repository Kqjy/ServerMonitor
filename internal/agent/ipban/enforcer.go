package ipban

import (
	"errors"
	"net/netip"
	"os"
	"time"
)

var ErrUnsupported = errors.New("ip banning is not supported on this platform")

const (
	TableName       = "sm_agent"
	ChainName       = "ipban"
	maxLocalEntries = 10000
	maxFleetEntries = 50000
	minElementTTL   = time.Second
)

var setNames = []string{"local4", "local6", "fleet4", "fleet6"}

type FleetEntry struct {
	IP      netip.Addr
	Timeout time.Duration
}

type ActiveEntry struct {
	IP      netip.Addr
	Expires time.Duration
}

type SetSnapshot struct {
	Name    string
	Entries []ActiveEntry
}

type Enforcer interface {
	Setup() error
	AddLocal(ip netip.Addr, timeout time.Duration) error
	RemoveLocal(ip netip.Addr) error
	FlushLocal() error
	ReplaceFleet(entries []FleetEntry) error
	Active() (local []ActiveEntry, fleet []ActiveEntry, err error)
	Close() error
}

const (
	enforceOK           = "ok"
	enforceNoPermission = "no_permission"
	enforceUnsupported  = "unsupported"
	enforceError        = "error"
)

func classifyEnforceError(err error) string {
	switch {
	case err == nil:
		return enforceOK
	case errors.Is(err, ErrUnsupported):
		return enforceUnsupported
	case errors.Is(err, os.ErrPermission):
		return enforceNoPermission
	case isUnsupportedErrno(err):
		return enforceUnsupported
	default:
		return enforceError
	}
}
