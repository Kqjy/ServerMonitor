package sse

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"servermonitor/pkg/wire"
)

type Hub struct {
	mu   sync.RWMutex
	subs map[*sub]struct{}
}

type subKind int

const (
	subPoints subKind = iota
	subAlerts
)

type subMsg struct {
	event string
	data  []byte
}

type sub struct {
	hostID  int64
	kind    subKind
	metrics map[string]struct{}
	ch      chan subMsg
}

func NewHub() *Hub {
	return &Hub{subs: map[*sub]struct{}{}}
}

type envelope struct {
	HostID int64        `json:"host_id"`
	Points []wire.Point `json:"points"`
}

func (h *Hub) Broadcast(hostID int64, points []wire.Point) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if len(h.subs) == 0 {
		return
	}

	for s := range h.subs {
		if s.kind != subPoints {
			continue
		}
		if s.hostID != 0 && s.hostID != hostID {
			continue
		}
		filtered := points
		if len(s.metrics) > 0 {
			filtered = make([]wire.Point, 0, len(points))
			for _, p := range points {
				if _, ok := s.metrics[p.Metric.Meta().Name]; ok {
					filtered = append(filtered, p)
				}
			}
			if len(filtered) == 0 {
				continue
			}
		}
		data, err := json.Marshal(envelope{HostID: hostID, Points: filtered})
		if err != nil {
			continue
		}
		select {
		case s.ch <- subMsg{event: "points", data: data}:
		default:
		}
	}
}

func (h *Hub) BroadcastAlert(ev wire.AlertEvent) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for s := range h.subs {
		if s.kind != subAlerts {
			continue
		}
		if s.hostID != 0 && s.hostID != ev.HostID {
			continue
		}
		select {
		case s.ch <- subMsg{event: "alert", data: data}:
		default:
		}
	}
}

func (h *Hub) HandleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	q := r.URL.Query()
	hostID, _ := strconv.ParseInt(q.Get("host_id"), 10, 64)
	metricSet := map[string]struct{}{}
	if mq := q.Get("metrics"); mq != "" {
		for _, m := range splitCSV(mq) {
			metricSet[m] = struct{}{}
		}
	}
	kind := subPoints
	if q.Get("kind") == "alerts" {
		kind = subAlerts
	}

	s := &sub{
		hostID:  hostID,
		kind:    kind,
		metrics: metricSet,
		ch:      make(chan subMsg, 64),
	}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.subs, s)
		h.mu.Unlock()
	}()

	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case msg := <-s.ch:
			if _, err := w.Write([]byte("event: " + msg.event + "\ndata: ")); err != nil {
				return
			}
			if _, err := w.Write(msg.data); err != nil {
				return
			}
			if _, err := w.Write([]byte("\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	out := []string{}
	cur := ""
	for _, r := range s {
		if r == ',' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
