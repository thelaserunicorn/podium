package api_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/podium/podium/internal/api"
	"github.com/podium/podium/internal/auth"
	"github.com/podium/podium/internal/ingress"
)

// TestIngressURL_AnonymousIs401 confirms the endpoint requires auth.
// We don't need a working router for this — auth gating happens
// before any Router call.
func TestIngressURL_AnonymousIs401(t *testing.T) {
	router := ingress.NewRouter(nil, nil, nil, slog.Default())
	t.Cleanup(router.Close)
	mux := http.NewServeMux()
	api.MountIngressURL(mux, api.NewIngressURLHandler(router, slog.Default()))

	req := httptest.NewRequest("GET", "/api/applications/1/ingress?ns=podium-dev", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401", rec.Code)
	}
}

// TestIngressURL_MissingNsIs400 confirms `ns` is required. With no
// router configured (only auth gating passes), the handler reaches
// its ns check and 400s before touching the router.
func TestIngressURL_MissingNsIs400(t *testing.T) {
	router := ingress.NewRouter(nil, nil, nil, slog.Default())
	t.Cleanup(router.Close)
	mux := http.NewServeMux()
	api.MountIngressURL(mux, api.NewIngressURLHandler(router, slog.Default()))

	req := httptest.NewRequest("GET", "/api/applications/1/ingress", nil)
	u := auth.User{ID: 1, Username: "t", Email: "t@x", Role: "USER", Status: auth.StatusApproved}
	req = req.WithContext(auth.WithUser(req.Context(), u))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ns") {
		t.Errorf("body=%q want ns-related error", rec.Body.String())
	}
}

// TestIngressURL_InvalidAppIDIs400 confirms {id} must parse as a
// positive int64. ServeMux itself rejects non-int patterns, so we
// use one that doesn't match {id}.
func TestIngressURL_InvalidAppIDIs404(t *testing.T) {
	router := ingress.NewRouter(nil, nil, nil, slog.Default())
	t.Cleanup(router.Close)
	mux := http.NewServeMux()
	api.MountIngressURL(mux, api.NewIngressURLHandler(router, slog.Default()))

	// "abc" doesn't match the {id} path segment, so ServeMux returns 404.
	req := httptest.NewRequest("GET", "/api/applications/abc/ingress?ns=podium-dev", nil)
	u := auth.User{ID: 1, Username: "t", Email: "t@x", Role: "USER", Status: auth.StatusApproved}
	req = req.WithContext(auth.WithUser(req.Context(), u))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 or 404", rec.Code)
	}
}
