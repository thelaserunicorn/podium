package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/podium/podium/internal/api"
	"github.com/podium/podium/internal/ingress"
)

// TestIngressHandler_AnonymousIsRejected confirms the api-layer shim
// propagates the request without authentication when there's no
// session cookie. The proxy itself enforces the 401 — the test
// simply asserts the wiring doesn't strip or transform the request
// in a way that would mask the failure.
func TestIngressHandler_AnonymousIsRejected(t *testing.T) {
	// Router + proxy with a router that always returns ErrNoSuchApp.
	// We never reach the router in this test — the proxy short-circuits
	// on missing user. But constructing the chain keeps the test honest.
	router := ingress.NewRouter(nil, nil, nil, nil)
	t.Cleanup(router.Close)
	proxy := ingress.NewProxy(router, nil)
	handler := api.NewIngressHandler(router, proxy, nil)

	mux := http.NewServeMux()
	handler.Mount(mux)

	req := httptest.NewRequest("GET", "/-/apps/1/podium-dev/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401", rec.Code)
	}
}

// TestIngressHandler_BadPathIs404 confirms the shim does NOT swallow
// path errors. parseIngressPath returns errors for malformed paths
// and the proxy surfaces them as 404 — the shim must not intercept.
//
// Note: Go's stdlib ServeMux auto-redirects /-/apps → /-/apps/ when
// only the trailing-slash form is registered, so we don't include
// that exact pair here. The behavior is correct (a 307 to the
// canonical prefix), but it's mux behavior, not proxy behavior.
func TestIngressHandler_BadPathIs404(t *testing.T) {
	router := ingress.NewRouter(nil, nil, nil, nil)
	t.Cleanup(router.Close)
	proxy := ingress.NewProxy(router, nil)
	handler := api.NewIngressHandler(router, proxy, nil)

	mux := http.NewServeMux()
	handler.Mount(mux)

	for _, bad := range []string{
		"/",
		"/-/",
		"/-/apps/",
		"/-/apps/abc/podium-dev",
	} {
		req := httptest.NewRequest("GET", bad, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("path=%q status=%d want 404", bad, rec.Code)
		}
	}
}

// TestIngressHandler_MountsUnderAppsPrefix asserts Mount() handles
// the /-/apps/ subtree rather than any single path. We do this by
// issuing a request with a path that, if Mount used HandleFunc, would
// 404 — instead the prefix match routes it through the proxy (and
// the proxy then 401s because no user is in context).
func TestIngressHandler_MountsUnderAppsPrefix(t *testing.T) {
	router := ingress.NewRouter(nil, nil, nil, nil)
	t.Cleanup(router.Close)
	proxy := ingress.NewProxy(router, nil)
	handler := api.NewIngressHandler(router, proxy, nil)

	mux := http.NewServeMux()
	handler.Mount(mux)

	for _, p := range []string{
		"/-/apps/1/podium-dev/",
		"/-/apps/1/podium-dev/foo/bar",
		"/-/apps/42/custom-ns/api",
	} {
		req := httptest.NewRequest("GET", p, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		// The proxy responds 401 (anonymous). What matters is that the
		// prefix got through to the proxy — a misconfigured mount
		// (e.g. /-/apps exactly) would return 404 from the stdlib mux.
		if rec.Code == http.StatusNotFound {
			t.Errorf("path=%q got 404 — Mount may not have used prefix match", p)
		}
	}
}

// TestIngressHandler_RouterAccessor returns the same router that was
// passed in. main.go uses this to call Close on shutdown.
func TestIngressHandler_RouterAccessor(t *testing.T) {
	router := ingress.NewRouter(nil, nil, nil, nil)
	t.Cleanup(router.Close)
	proxy := ingress.NewProxy(router, nil)
	handler := api.NewIngressHandler(router, proxy, nil)
	if handler.Router() != router {
		t.Errorf("Router() returned a different *ingress.Router")
	}
}

// keep imports tidy + silence unused linters
var (
	_ = context.Background
	_ = strconv.Itoa
)
