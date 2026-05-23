package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
)

const (
	csrfCookie = "sm_csrf"
	csrfHeader = "X-CSRF-Token"
)

func csrfIssuer(secure bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				if c, err := r.Cookie(csrfCookie); err != nil || c.Value == "" {
					setCSRFCookie(w, secure)
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func csrfVerifier() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if csrfMethodSafe(r.Method) {
				next.ServeHTTP(w, r)
				return
			}
			if r.Header.Get("X-Admin-Token") != "" || r.Header.Get("X-Agent-Token") != "" {
				next.ServeHTTP(w, r)
				return
			}
			c, err := r.Cookie(csrfCookie)
			if err != nil || c.Value == "" {
				writeError(w, http.StatusForbidden, "missing csrf cookie")
				return
			}
			header := r.Header.Get(csrfHeader)
			if header == "" {
				writeError(w, http.StatusForbidden, "missing csrf header")
				return
			}
			if subtle.ConstantTimeCompare([]byte(header), []byte(c.Value)) != 1 {
				writeError(w, http.StatusForbidden, "csrf token mismatch")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func csrfMethodSafe(m string) bool {
	switch strings.ToUpper(m) {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

func setCSRFCookie(w http.ResponseWriter, secure bool) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookie,
		Value:    hex.EncodeToString(raw),
		Path:     "/",
		HttpOnly: false,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}
