package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"servermonitor/internal/server/auth"
)

func TestRequireAdminRejectsNonAdminRole(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	for _, tc := range []struct {
		role string
		want int
	}{{"viewer", http.StatusForbidden}, {"admin", http.StatusNoContent}} {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/ipban/settings", nil)
		req = req.WithContext(context.WithValue(req.Context(), ctxUser, auth.User{Username: "test", Role: tc.role}))
		rec := httptest.NewRecorder()
		requireAdmin(next).ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("role %q: status %d, want %d", tc.role, rec.Code, tc.want)
		}
	}
}
