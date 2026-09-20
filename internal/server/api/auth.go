package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"servermonitor/internal/server/auth"
	"servermonitor/pkg/version"
)

const sessionCookie = auth.SessionCookie

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type meResponse struct {
	Username string `json:"username"`
	Role     string `json:"role"`
}

type statusResponse struct {
	Initialized bool   `json:"initialized"`
	Version     string `json:"version"`
}

func authStatusHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		has, err := svc.HasAnyUser(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, statusResponse{Initialized: has, Version: version.Version})
	}
}

func setupHandler(svc *auth.Service, secure bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req loginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		u, err := svc.CreateAdmin(r.Context(), req.Username, req.Password)
		if err != nil {
			if errors.Is(err, auth.ErrAlreadyInitialized) {
				writeError(w, http.StatusConflict, "admin already exists")
				return
			}
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		_, token, err := svc.Login(r.Context(), u.Username, req.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		recordActor(r.Context(), u)
		setSessionCookie(w, r, token, secure)
		writeJSON(w, http.StatusCreated, meResponse{Username: u.Username, Role: u.Role})
	}
}

func loginHandler(svc *auth.Service, secure bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req loginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		u, token, err := svc.Login(r.Context(), req.Username, req.Password)
		if err != nil {
			if errors.Is(err, auth.ErrInvalidCredentials) {
				writeError(w, http.StatusUnauthorized, "invalid credentials")
				return
			}
			if errors.Is(err, auth.ErrAccountLocked) {
				w.Header().Set("Retry-After", "900")
				writeError(w, http.StatusLocked, "account temporarily locked due to repeated failed logins")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		recordActor(r.Context(), u)
		setSessionCookie(w, r, token, secure)
		writeJSON(w, http.StatusOK, meResponse{Username: u.Username, Role: u.Role})
	}
}

func logoutHandler(svc *auth.Service, secure bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err == nil {
			_ = svc.Logout(r.Context(), c.Value)
		}
		clearSessionCookie(w, r, secure)
		w.WriteHeader(http.StatusNoContent)
	}
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func changePasswordHandler(svc *auth.Service, secure bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := userFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		if u.ID == 0 {
			writeError(w, http.StatusBadRequest, "admin token bypass cannot change a stored password; use the sm-server reset-password subcommand")
			return
		}
		var req changePasswordRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		token, err := svc.ChangePassword(r.Context(), u.ID, req.CurrentPassword, req.NewPassword)
		if err != nil {
			if errors.Is(err, auth.ErrInvalidCredentials) {
				writeError(w, http.StatusUnauthorized, "current password is incorrect")
				return
			}
			if errors.Is(err, auth.ErrNoUser) {
				writeError(w, http.StatusNotFound, "user not found")
				return
			}
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		setSessionCookie(w, r, token, secure)
		w.WriteHeader(http.StatusNoContent)
	}
}

func meHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := userFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		writeJSON(w, http.StatusOK, meResponse{Username: u.Username, Role: u.Role})
	}
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  time.Now().Add(auth.SessionTTL),
		MaxAge:   int(auth.SessionTTL / time.Second),
		HttpOnly: true,
		Secure:   secure || r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure || r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
}

func requireUser(svc *auth.Service, adminToken string, secure bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if t := r.Header.Get("X-Admin-Token"); t != "" && adminToken != "" {
				if subtle.ConstantTimeCompare([]byte(t), []byte(adminToken)) == 1 {
					u := auth.User{Username: "admin", Role: "admin"}
					recordActor(r.Context(), u)
					ctx := context.WithValue(r.Context(), ctxUser, u)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}
			c, err := r.Cookie(sessionCookie)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "not authenticated")
				return
			}
			u, err := svc.ResolveSession(r.Context(), c.Value)
			if err != nil {
				clearSessionCookie(w, r, secure)
				writeError(w, http.StatusUnauthorized, "session invalid")
				return
			}
			recordActor(r.Context(), u)
			ctx := context.WithValue(r.Context(), ctxUser, u)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func recordActor(ctx context.Context, u auth.User) {
	if slot, ok := ctx.Value(ctxActorSlot).(*actorSlot); ok && slot != nil {
		slot.user.Store(&u)
	}
}

func userFromContext(ctx context.Context) (auth.User, bool) {
	u, ok := ctx.Value(ctxUser).(auth.User)
	return u, ok
}

func requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := userFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		if u.Role != "admin" {
			writeError(w, http.StatusForbidden, "admin role required")
			return
		}
		next.ServeHTTP(w, r)
	})
}
