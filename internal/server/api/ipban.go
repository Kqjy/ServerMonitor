package api

import (
	"compress/gzip"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	serverconfig "servermonitor/internal/server/config"
	"servermonitor/internal/server/ipban"
)

const maxIPBanBody = 256 << 10

func writeIPBanError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ipban.ErrValidation):
		writeError(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "invalid: "))
	case errors.Is(err, ipban.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func ipbanActor(r *http.Request) string {
	if u, ok := userFromContext(r.Context()); ok && u.Username != "" {
		return u.Username
	}
	return "admin"
}

func agentIPBanConfigHandler(svc *ipban.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hostID, ok := hostIDFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "no host")
			return
		}
		cfg, err := svc.AgentConfig(r.Context(), hostID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "ipban config unavailable")
			return
		}
		writeJSON(w, http.StatusOK, cfg)
	}
}

type ipbanSettingsResp struct {
	ipban.Settings
	ClientIP string `json:"client_ip"`
}

func allowlistClientIP(r *http.Request, trusted []*net.IPNet) string {
	addr, err := netip.ParseAddr(clientIP(r, trusted))
	if err != nil {
		return ""
	}
	addr = addr.Unmap()
	if addr.IsLoopback() || addr.IsUnspecified() || addr.IsLinkLocalUnicast() || addr.IsMulticast() {
		return ""
	}
	return addr.String()
}

func ipbanSettingsHandler(svc *ipban.Service, trusted []*net.IPNet) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, ipbanSettingsResp{Settings: svc.Settings(), ClientIP: allowlistClientIP(r, trusted)})
	}
}

func ipbanUpdateSettingsHandler(svc *ipban.Service, trusted []*net.IPNet) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in ipban.SettingsInput
		if err := decodeStrictJSON(w, r, maxIPBanBody, &in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		st, err := svc.UpdateSettings(r.Context(), in)
		if err != nil {
			writeIPBanError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ipbanSettingsResp{Settings: st, ClientIP: allowlistClientIP(r, trusted)})
	}
}

func ipbanHostsHandler(svc *ipban.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hosts, err := svc.Hosts(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, hosts)
	}
}

func ipbanUpdateHostHandler(svc *ipban.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "invalid host id")
			return
		}
		var patch ipban.HostPolicy
		if err := decodeStrictJSON(w, r, maxIPBanBody, &patch); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		saved, err := svc.UpdateHostPolicy(r.Context(), id, patch)
		if err != nil {
			writeIPBanError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, saved)
	}
}

func ipbanActiveHandler(svc *ipban.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var hostID int64
		if h := r.URL.Query().Get("host"); h != "" {
			id, err := strconv.ParseInt(h, 10, 64)
			if err != nil || id <= 0 {
				writeError(w, http.StatusBadRequest, "invalid host id")
				return
			}
			hostID = id
		}
		limit := 0
		if raw := r.URL.Query().Get("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed <= 0 || parsed > 1000 {
				writeError(w, http.StatusBadRequest, "limit must be an integer between 1 and 1000")
				return
			}
			limit = parsed
		}
		bans, err := svc.Active(r.Context(), hostID, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, bans)
	}
}

func ipbanFleetHandler(svc *ipban.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fleet, err := svc.Fleet(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, fleet)
	}
}

func ipbanManualBanHandler(svc *ipban.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			IP   string `json:"ip"`
			TTLS int    `json:"ttl_s"`
			Note string `json:"note"`
		}
		if err := decodeStrictJSON(w, r, maxIPBanBody, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		ban, err := svc.ManualBan(r.Context(), req.IP, time.Duration(req.TTLS)*time.Second, req.Note, ipbanActor(r))
		if err != nil {
			writeIPBanError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, ban)
	}
}

func ipbanFleetUnbanHandler(svc *ipban.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := svc.FleetUnban(r.Context(), chi.URLParam(r, "ip"), ipbanActor(r)); err != nil {
			writeIPBanError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func ipbanHostUnbanHandler(svc *ipban.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			HostID int64  `json:"host_id"`
			IP     string `json:"ip"`
		}
		if err := decodeStrictJSON(w, r, maxIPBanBody, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		if req.HostID <= 0 {
			writeError(w, http.StatusBadRequest, "host_id is required")
			return
		}
		if err := svc.HostUnban(r.Context(), req.HostID, req.IP, ipbanActor(r)); err != nil {
			writeIPBanError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func ipbanEventsHandler(svc *ipban.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var f ipban.EventFilter
		if h := q.Get("host"); h != "" {
			id, err := strconv.ParseInt(h, 10, 64)
			if err != nil || id <= 0 {
				writeError(w, http.StatusBadRequest, "invalid host id")
				return
			}
			f.HostID = id
		}
		f.IP = q.Get("ip")
		if raw := q.Get("limit"); raw != "" {
			limit, err := strconv.Atoi(raw)
			if err != nil || limit <= 0 || limit > 1000 {
				writeError(w, http.StatusBadRequest, "limit must be an integer between 1 and 1000")
				return
			}
			f.Limit = limit
		}
		if b := q.Get("before"); b != "" {
			t, err := time.Parse(time.RFC3339Nano, b)
			if err != nil {
				writeError(w, http.StatusBadRequest, "before must be RFC 3339")
				return
			}
			f.Before = t
			if id := q.Get("before_id"); id != "" {
				n, err := strconv.ParseInt(id, 10, 64)
				if err != nil || n < 0 {
					writeError(w, http.StatusBadRequest, "invalid before_id")
					return
				}
				f.BeforeID = n
			}
		}
		if q.Get("before") == "" && q.Get("before_id") != "" {
			writeError(w, http.StatusBadRequest, "before_id requires before")
			return
		}
		if strings.EqualFold(q.Get("format"), "csv") {
			if err := writeIPBanEventsCSV(w, r, svc, f); err != nil {
				writeIPBanError(w, err)
			}
			return
		}
		events, err := svc.Events(r.Context(), f)
		if err != nil {
			writeIPBanError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, events)
	}
}

func writeIPBanEventsCSV(w http.ResponseWriter, r *http.Request, svc *ipban.Service, filter ipban.EventFilter) error {
	filename := "ipban-events-" + time.Now().UTC().Format("20060102-150405") + ".csv"
	tmp, err := os.CreateTemp("", "servermonitor-ipban-*.csv.gz")
	if err != nil {
		return err
	}
	path := tmp.Name()
	defer os.Remove(path)
	gz := gzip.NewWriter(tmp)
	cw := csv.NewWriter(gz)
	if err := cw.Write([]string{"time", "action", "ip", "host", "enforced", "failures", "user", "expires_at", "repeat_count", "source", "actor", "note"}); err != nil {
		_ = gz.Close()
		_ = tmp.Close()
		return err
	}
	err = svc.StreamEvents(r.Context(), filter, func(e ipban.Event) error {
		expires := ""
		if e.ExpiresAt != nil {
			expires = e.ExpiresAt.UTC().Format(time.RFC3339)
		}
		return cw.Write([]string{
			e.Time.UTC().Format(time.RFC3339),
			csvSafe(e.Action),
			csvSafe(e.IP),
			csvSafe(e.Hostname),
			strconv.FormatBool(e.Enforced),
			strconv.Itoa(e.Failures),
			csvSafe(e.User),
			expires,
			strconv.Itoa(e.RepeatCount),
			csvSafe(e.Source),
			csvSafe(e.Actor),
			csvSafe(e.Note),
		})
	})
	cw.Flush()
	if err == nil {
		err = cw.Error()
	}
	if closeErr := gz.Close(); err == nil {
		err = closeErr
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.WriteHeader(http.StatusOK)
	_, err = io.Copy(w, file)
	return err
}

func ipbanStatsHandler(svc *ipban.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var f ipban.StatsFilter
		if h := q.Get("host"); h != "" {
			id, err := strconv.ParseInt(h, 10, 64)
			if err != nil || id <= 0 {
				writeError(w, http.StatusBadRequest, "invalid host id")
				return
			}
			f.HostID = id
		}
		if raw := q.Get("window"); raw != "" {
			d, err := time.ParseDuration(raw)
			if err != nil && !serverconfig.IsForever(raw) && serverconfig.ValidateInterval(raw) == nil {
				d = serverconfig.IntervalToDuration(raw)
				err = nil
			}
			if err != nil || d <= 0 {
				writeError(w, http.StatusBadRequest, "window must be a positive duration such as 168h or 7 days")
				return
			}
			f.Window = d
		}
		if raw := q.Get("top"); raw != "" {
			top, err := strconv.Atoi(raw)
			if err != nil || top <= 0 || top > 200 {
				writeError(w, http.StatusBadRequest, "top must be an integer between 1 and 200")
				return
			}
			f.Top = top
		}
		stats, err := svc.Stats(r.Context(), f)
		if err != nil {
			writeIPBanError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, stats)
	}
}

func ipbanSummaryHandler(svc *ipban.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sum, err := svc.Summary(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, sum)
	}
}
