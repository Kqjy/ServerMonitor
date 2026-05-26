package collectors

import (
	"context"
	"os"
	"runtime"
	"sort"
	"sync"

	"servermonitor/pkg/wire"
)

func resolveTrustedBin(candidates []string) string {
	for _, p := range candidates {
		fi, err := os.Lstat(p)
		if err != nil {
			continue
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if !fi.Mode().IsRegular() {
			continue
		}
		return p
	}
	return ""
}

type Collector interface {
	Name() string
	Platforms() []string
	Collect(ctx context.Context) ([]wire.Point, error)
}

type ProcessCollector interface {
	CollectProcesses(ctx context.Context, topN int) ([]wire.Process, error)
}

type ContainerCollector interface {
	CollectContainers(ctx context.Context) ([]wire.Container, error)
}

type PortCollector interface {
	CollectPorts(ctx context.Context) ([]wire.Port, error)
}

type StatusReporter interface {
	Status() wire.CollectorStatus
}

var (
	regMu      sync.Mutex
	registered []Collector
)

func Register(c Collector) {
	regMu.Lock()
	defer regMu.Unlock()
	registered = append(registered, c)
}

func All() []Collector {
	regMu.Lock()
	defer regMu.Unlock()
	cp := make([]Collector, len(registered))
	copy(cp, registered)
	sort.Slice(cp, func(i, j int) bool { return cp[i].Name() < cp[j].Name() })
	return cp
}

func Filtered(enabled, disabled []string) []Collector {
	all := All()
	allowed := make(map[string]bool, len(enabled))
	for _, n := range enabled {
		allowed[n] = true
	}
	denied := make(map[string]bool, len(disabled))
	for _, n := range disabled {
		denied[n] = true
	}
	out := make([]Collector, 0, len(all))
	for _, c := range all {
		if !supports(c, runtime.GOOS) {
			continue
		}
		if denied[c.Name()] {
			continue
		}
		if len(allowed) > 0 && !allowed[c.Name()] {
			continue
		}
		out = append(out, c)
	}
	return out
}

func supports(c Collector, goos string) bool {
	ps := c.Platforms()
	if len(ps) == 0 {
		return true
	}
	for _, p := range ps {
		if p == "all" || p == goos {
			return true
		}
	}
	return false
}
