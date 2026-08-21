package api

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

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
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
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
		f.Limit, _ = strconv.Atoi(q.Get("limit"))
		if b := q.Get("before"); b != "" {
			t, err := time.Parse(time.RFC3339Nano, b)
			if err != nil {
				writeError(w, http.StatusBadRequest, "before must be RFC 3339")
				return
			}
			f.Before = t
		}
		events, err := svc.Events(r.Context(), f)
		if err != nil {
			writeIPBanError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, events)
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
