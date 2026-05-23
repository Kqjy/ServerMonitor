package api

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type limiterEntry struct {
	limiter *rate.Limiter
	seen    time.Time
}

type limiterMap struct {
	mu    sync.Mutex
	items map[string]*limiterEntry
	rps   rate.Limit
	burst int
}

func newLimiterMap(rps rate.Limit, burst int) *limiterMap {
	m := &limiterMap{
		items: make(map[string]*limiterEntry),
		rps:   rps,
		burst: burst,
	}
	go m.sweep()
	return m
}

func (m *limiterMap) get(key string) *rate.Limiter {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.items[key]
	if !ok {
		e = &limiterEntry{limiter: rate.NewLimiter(m.rps, m.burst)}
		m.items[key] = e
	}
	e.seen = time.Now()
	return e.limiter
}

func (m *limiterMap) sweep() {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for range t.C {
		m.mu.Lock()
		cutoff := time.Now().Add(-15 * time.Minute)
		for k, e := range m.items {
			if e.seen.Before(cutoff) {
				delete(m.items, k)
			}
		}
		m.mu.Unlock()
	}
}
