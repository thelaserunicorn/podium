package ingress

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/podium/podium/internal/application"
	"github.com/podium/podium/internal/storage"
)

// fakeAuthUser implements AuthUser for proxy tests. We use a struct
// rather than *auth.User to avoid pulling the auth package into the
// ingress test binary.
type fakeAuthUser struct{ id int64 }

func (u *fakeAuthUser) UserID() int64 { return u.id }

// upstreamServer returns an httptest.Server that records requests
// and serves canned responses. The URL form is used so the reverse
// proxy can dial it; the test wires the proxy to point at the
// server's address via a fakeForwarderFactory (see helper below).
func upstreamServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	return s
}

// dialReplacingForwarder is a no-op stub kept for symmetry with the
// earlier design — the new fakeRunner uses dialUpstream instead.
func dialReplacingForwarder(f *Forwarder, _ string) { _ = f }

// upstreamProxySetup is the smallest fixture that lets us exercise
// the proxy end-to-end: a real upstream HTTP server, a Router whose
// ForwarderFactory dials that server, and a Proxy wrapping the
// Router. Tests use the returned Proxy as an http.Handler.
type upstreamProxySetup struct {
	proxy    *Proxy
	upstream *httptest.Server
	appID    int64
	aliceID  int64
	router   *Router
}

func newUpstreamProxySetup(t *testing.T, upstream http.HandlerFunc) *upstreamProxySetup {
	t.Helper()
	store, appSvc, appID, aliceID := seedRouterFixture(t, 8080)
	if _, err := store.EnsureEnvironment(context.Background(), "podium-dev"); err != nil {
		t.Fatalf("ensure env: %v", err)
	}

	upstreamSrv := upstreamServer(t, upstream)

	bs := &fakeBootstrap{}
	r := NewRouter(store, bs, appSvc, nil)
	listenerAddr := upstreamSrv.Listener.Addr().String()
	r.SetForwarderFactory(&fakeForwarderFactory{fake: &fakeRunner{
		readyDelay:    5 * time.Millisecond,
		dialUpstream:  listenerAddr,
		readyListener: true,
	}})
	r.SetPortRange(52000, 52100)

	proxy := NewProxy(r, nil)

	setup := &upstreamProxySetup{
		proxy:    proxy,
		upstream: upstreamSrv,
		appID:    appID,
		aliceID:  aliceID,
		router:   r,
	}
	// Stop every forwarder the router created at end-of-test so the
	// shared port range (52000..52100) is freed for the next test.
	t.Cleanup(func() { r.Close() })
	return setup
}

func (s *upstreamProxySetup) request(t *testing.T, method, path string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	ctx := WithAuthUser(req.Context(), &fakeAuthUser{id: s.aliceID})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	s.proxy.ServeHTTP(rec, req)
	return rec
}

func TestProxy_StripsIngressPrefix(t *testing.T) {
	setup := newUpstreamProxySetup(t, func(w http.ResponseWriter, r *http.Request) {
		// Echo back the path the upstream saw.
		w.Header().Set("X-Upstream-Path", r.URL.Path)
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
	})
	path := "/-/apps/" + strconv.FormatInt(setup.appID, 10) + "/podium-dev/foo/bar"
	rec := setup.request(t, "GET", path, nil)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Upstream-Path"); got != "/foo/bar" {
		t.Errorf("upstream path=%q want /foo/bar", got)
	}
}

func TestProxy_StreamsBody(t *testing.T) {
	setup := newUpstreamProxySetup(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		flusher, _ := w.(http.Flusher)
		for i := 0; i < 5; i++ {
			_, _ = io.WriteString(w, "chunk-"+strconv.Itoa(i)+"\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	})
	rec := setup.request(t, "GET", "/-/apps/"+strconv.FormatInt(setup.appID, 10) + "/podium-dev/", nil)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	for i := 0; i < 5; i++ {
		want := "chunk-" + strconv.Itoa(i) + "\n"
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("body missing %q; got %q", want, rec.Body.String())
		}
	}
}

func TestProxy_AnonymousReturns401(t *testing.T) {
	setup := newUpstreamProxySetup(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream should not be called for anonymous request")
	})
	req := httptest.NewRequest("GET", "/-/apps/"+strconv.FormatInt(setup.appID, 10) + "/podium-dev/", nil)
	// No auth in context.
	rec := httptest.NewRecorder()
	setup.proxy.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Errorf("status=%d want 401", rec.Code)
	}
}

func TestProxy_InvalidPathReturns404(t *testing.T) {
	store, appSvc, _, aliceID := seedRouterFixture(t, 8080)
	if _, err := store.EnsureEnvironment(context.Background(), "podium-dev"); err != nil {
		t.Fatalf("ensure env: %v", err)
	}
	r := NewRouter(store, &fakeBootstrap{}, appSvc, nil)
	r.SetForwarderFactory(&fakeForwarderFactory{fake: &fakeRunner{readyDelay: 5 * time.Millisecond}})
	proxy := NewProxy(r, nil)

	for _, bad := range []string{
		"/",
		"/-/",
		"/-/apps",
		"/-/apps/",
		"/-/apps/abc",
		"/-/apps/abc/podium-dev",
		"/-/apps/0/podium-dev/",
		"/-/apps/-1/podium-dev/",
	} {
		req := httptest.NewRequest("GET", bad, nil)
		ctx := WithAuthUser(req.Context(), &fakeAuthUser{id: aliceID})
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		proxy.ServeHTTP(rec, req)
		if rec.Code != 404 {
			t.Errorf("path=%q status=%d want 404", bad, rec.Code)
		}
	}
}

func TestProxy_UnknownAppReturns404(t *testing.T) {
	store, appSvc, _, aliceID := seedRouterFixture(t, 8080)
	if _, err := store.EnsureEnvironment(context.Background(), "podium-dev"); err != nil {
		t.Fatalf("ensure env: %v", err)
	}
	r := NewRouter(store, &fakeBootstrap{}, appSvc, nil)
	r.SetForwarderFactory(&fakeForwarderFactory{fake: &fakeRunner{readyDelay: 5 * time.Millisecond}})
	proxy := NewProxy(r, nil)

	req := httptest.NewRequest("GET", "/-/apps/99999/podium-dev/", nil)
	ctx := WithAuthUser(req.Context(), &fakeAuthUser{id: aliceID})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Errorf("status=%d want 404", rec.Code)
	}
}

func TestProxy_UnknownNamespaceReturns404(t *testing.T) {
	store, appSvc, appID, aliceID := seedRouterFixture(t, 8080)
	if _, err := store.EnsureEnvironment(context.Background(), "podium-dev"); err != nil {
		t.Fatalf("ensure env: %v", err)
	}
	r := NewRouter(store, &fakeBootstrap{}, appSvc, nil)
	r.SetForwarderFactory(&fakeForwarderFactory{fake: &fakeRunner{readyDelay: 5 * time.Millisecond}})
	proxy := NewProxy(r, nil)

	req := httptest.NewRequest("GET", "/-/apps/"+strconv.FormatInt(appID, 10)+"/ghost-ns/", nil)
	ctx := WithAuthUser(req.Context(), &fakeAuthUser{id: aliceID})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Errorf("status=%d want 404", rec.Code)
	}
}

func TestProxy_OwnerCheckEnforced(t *testing.T) {
	setup := newUpstreamProxySetup(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream should not be called for cross-owner request")
	})
	req := httptest.NewRequest("GET", "/-/apps/"+strconv.FormatInt(setup.appID, 10)+"/podium-dev/", nil)
	// Use a different ownerID so the ownership check fails.
	ctx := WithAuthUser(req.Context(), &fakeAuthUser{id: 9999})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	setup.proxy.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Errorf("status=%d want 404", rec.Code)
	}
}

// TestProxy_Upstream502WhenForwarderDead simulates a forwarder whose
// subprocess died; the proxy should respond with 502 + app_unavailable
// rather than hanging.
func TestProxy_Upstream502WhenForwarderDead(t *testing.T) {
	store, appSvc, appID, aliceID := seedRouterFixture(t, 8080)
	if _, err := store.EnsureEnvironment(context.Background(), "podium-dev"); err != nil {
		t.Fatalf("ensure env: %v", err)
	}
	r := NewRouter(store, &fakeBootstrap{}, appSvc, nil)
	// Use a runner that dials nothing — Dial returns an error.
	r.SetForwarderFactory(&fakeForwarderFactory{fake: &fakeRunner{
		readyDelay: 5 * time.Millisecond,
		dialUpstream: "127.0.0.1:1", // closed port → dial fails
	}})
	r.SetPortRange(53000, 53000)
	proxy := NewProxy(r, nil)

	req := httptest.NewRequest("GET", "/-/apps/"+strconv.FormatInt(appID, 10)+"/podium-dev/", nil)
	ctx := WithAuthUser(req.Context(), &fakeAuthUser{id: aliceID})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if rec.Code != 502 {
		t.Errorf("status=%d want 502 (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "app_unavailable") {
		t.Errorf("body=%q want app_unavailable", rec.Body.String())
	}
}

func TestParseIngressPath(t *testing.T) {
	cases := []struct {
		path      string
		wantID    int64
		wantNS    string
		wantRest  string
		wantError bool
	}{
		{"/-/apps/1/podium-dev/", 1, "podium-dev", "/", false},
		{"/-/apps/1/podium-dev", 1, "podium-dev", "/", false},
		{"/-/apps/42/podium-staging/foo/bar", 42, "podium-staging", "/foo/bar", false},
		{"/-/apps/7/custom-ns/api", 7, "custom-ns", "/api", false},
		{"/", 0, "", "", true},
		{"/-/", 0, "", "", true},
		{"/-/apps", 0, "", "", true},
		{"/-/apps/", 0, "", "", true},
		{"/-/apps/abc/podium-dev", 0, "", "", true},
		{"/-/apps/0/podium-dev/", 0, "", "", true},
		{"/-/apps/-1/podium-dev/", 0, "", "", true},
		{"/-/apps/1", 0, "", "", true},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			id, ns, rest, err := parseIngressPath(c.path)
			if c.wantError {
				if err == nil {
					t.Errorf("path=%q expected error", c.path)
				}
				return
			}
			if err != nil {
				t.Fatalf("path=%q: %v", c.path, err)
			}
			if id != c.wantID || ns != c.wantNS || rest != c.wantRest {
				t.Errorf("path=%q got=(%d,%q,%q) want=(%d,%q,%q)", c.path, id, ns, rest, c.wantID, c.wantNS, c.wantRest)
			}
		})
	}
}

// ensure the test helpers compile against the same imports we use.
var _ = bufio.NewScanner
var _ = url.URL{}
var _ = errors.New
var _ = application.ErrNotFound
var _ storage.Queries
