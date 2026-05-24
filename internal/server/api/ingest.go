package api

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"servermonitor/internal/server/ingest"
	"servermonitor/internal/server/sse"
	"servermonitor/internal/server/storage"
	"servermonitor/pkg/version"
	"servermonitor/pkg/wire"
)

const (
	maxIngestBody         = 32 << 20
	maxIngestDecompressed = 256 << 20
)

func ingestHandler(b *ingest.Batcher, hub *sse.Hub, hosts *storage.Hosts, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hostID, ok := hostIDFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "no host")
			return
		}

		body := http.MaxBytesReader(w, r.Body, maxIngestBody)
		defer body.Close()

		var reader io.Reader = body
		if strings.Contains(r.Header.Get("Content-Encoding"), "gzip") {
			gz, err := gzip.NewReader(body)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid gzip")
				return
			}
			defer gz.Close()
			reader = io.LimitReader(gz, maxIngestDecompressed+1)
		}

		var batch wire.Batch
		dec := json.NewDecoder(reader)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&batch); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		if _, err := dec.Token(); err != io.EOF {
			if lr, ok := reader.(*io.LimitedReader); ok && lr.N <= 0 {
				writeError(w, http.StatusRequestEntityTooLarge, "decompressed body too large")
				return
			}
			writeError(w, http.StatusBadRequest, "trailing data after json")
			return
		}

		if len(batch.Points) == 0 && len(batch.Processes) == 0 && len(batch.Containers) == 0 {
			writeJSON(w, http.StatusOK, wire.IngestAck{Accepted: 0, HostID: hostID})
			return
		}

		now := time.Now()
		minT := now.Add(-2 * time.Hour)
		maxT := now.Add(2 * time.Minute)
		for i := range batch.Points {
			t := batch.Points[i].Time
			if t.IsZero() {
				batch.Points[i].Time = now
				continue
			}
			if t.Before(minT) || t.After(maxT) {
				writeError(w, http.StatusBadRequest, "timestamp out of range")
				return
			}
		}
		for i := range batch.Processes {
			t := batch.Processes[i].Time
			if t.IsZero() {
				batch.Processes[i].Time = now
				continue
			}
			if t.Before(minT) || t.After(maxT) {
				writeError(w, http.StatusBadRequest, "timestamp out of range")
				return
			}
		}
		for i := range batch.Containers {
			t := batch.Containers[i].Time
			if t.IsZero() {
				batch.Containers[i].Time = now
				continue
			}
			if t.Before(minT) || t.After(maxT) {
				writeError(w, http.StatusBadRequest, "timestamp out of range")
				return
			}
		}

		points, procs, conts := ingest.ConvertBatch(hostID, &batch)
		if err := b.Reserve(len(points)); err != nil {
			if errors.Is(err, ingest.ErrBackpressure) {
				w.Header().Set("Retry-After", "2")
				writeError(w, http.StatusTooManyRequests, "ingest backpressure")
				return
			}
			writeError(w, http.StatusInternalServerError, "reserve failed")
			return
		}
		if err := ingest.InsertSnapshots(r.Context(), b.Pool(), procs, conts); err != nil {
			b.ReleaseReserved(len(points))
			logger.Warn("insert snapshots", "err", err, "host", hostID, "procs", len(procs), "conts", len(conts))
			w.Header().Set("Retry-After", "2")
			writeError(w, http.StatusBadGateway, "snapshot insert failed")
			return
		}
		b.CommitReserved(points)

		_ = hosts.Touch(r.Context(), hostID, storage.HostInfoUpdate{
			OS:           batch.Host.OS,
			Arch:         batch.Host.Arch,
			Kernel:       batch.Host.Kernel,
			AgentVersion: batch.Host.AgentVersion,
			Collectors:   batch.Host.Collectors,
			Tags:         batch.Host.Tags,
		})

		hub.Broadcast(hostID, batch.Points)

		ack := wire.IngestAck{
			Accepted:           len(batch.Points),
			HostID:             hostID,
			LatestAgentVersion: version.Version,
		}
		if host, hErr := hosts.Get(r.Context(), hostID); hErr == nil {
			if host.SampleIntervalS > 0 {
				ack.IntervalS = host.SampleIntervalS
			}
			auto := host.AutoUpgrade
			ack.AutoUpgrade = &auto
			if host.UpgradeRequestedAt != nil && version.IsNewer(version.Version, batch.Host.AgentVersion) {
				ack.UpgradeNow = true
				if clrErr := hosts.ClearUpgradeRequest(r.Context(), hostID); clrErr != nil {
					logger.Warn("clear upgrade request", "host", hostID, "err", clrErr)
				}
			}
		}
		writeJSON(w, http.StatusOK, ack)
	}
}
