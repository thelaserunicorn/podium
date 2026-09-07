package ingress

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"strings"
	"testing"
)

// fakeForwarder satisfies the DialContext signature the Proxy needs
// without spawning kubectl. It dials whatever address dialAddr is
// set to, so the test's httptest server becomes the upstream.
type fakeForwarder struct {
	dialAddr string
}

func (f *fakeForwarder) DialContext(ctx context.Context, network, _ string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, f.dialAddr)
}

func (f *fakeForwarder) LocalPort() int { return 0 }

func TestInjectBaseHref_InsertsIntoHead(t *testing.T) {
	body := []byte(`<!doctype html><html><head><title>x</title></head><body></body></html>`)
	out := string(injectBaseHref(body, "/-/apps/11/podium-dev/"))

	if !strings.Contains(out, `<base href="/-/apps/11/podium-dev/">`) {
		t.Errorf("missing <base>: %s", out)
	}
	// The base must come BEFORE the <title> (browsers expect <base>
	// early in <head> for relative URL resolution to work).
	baseIdx := strings.Index(out, "<base")
	titleIdx := strings.Index(out, "<title")
	if baseIdx < 0 || titleIdx < 0 || baseIdx > titleIdx {
		t.Errorf("<base> must precede <title>: %s", out)
	}
}

func TestInjectBaseHref_ReplacesExistingBase(t *testing.T) {
	body := []byte(`<html><head><base href="/old/"><title>x</title></head><body></body></html>`)
	out := string(injectBaseHref(body, "/-/apps/11/podium-dev/"))

	if !strings.Contains(out, `<base href="/-/apps/11/podium-dev/">`) {
		t.Errorf("did not replace <base>: %s", out)
	}
	if strings.Contains(out, `href="/old/"`) {
		t.Errorf("old <base> still present: %s", out)
	}
	// Should have exactly one <base> tag.
	if got := strings.Count(out, "<base"); got != 1 {
		t.Errorf("expected 1 <base>, got %d: %s", got, out)
	}
}

func TestInjectBaseHref_HeadMissingPrependsBase(t *testing.T) {
	// SVG / fragmented HTML: no <head>. The browser still respects a
	// leading <base> tag.
	body := []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)
	out := string(injectBaseHref(body, "/-/apps/11/podium-dev/"))

	if !strings.HasPrefix(out, `<base href="/-/apps/11/podium-dev/">`) {
		t.Errorf("expected leading <base>: %s", out)
	}
}

func TestIsHTML(t *testing.T) {
	cases := []struct {
		ct   string
		want bool
	}{
		{"text/html", true},
		{"text/html; charset=utf-8", true},
		{"text/html;charset=utf-8", true},
		{"  TEXT/HTML  ", true},
		{"application/json", false},
		{"text/css", false},
		{"", false},
		{"text/plain", false},
	}
	for _, c := range cases {
		if got := isHTML(c.ct); got != c.want {
			t.Errorf("isHTML(%q) = %v, want %v", c.ct, got, c.want)
		}
	}
}

func TestIngressBasePath(t *testing.T) {
	if got := ingressBasePath(11, "podium-dev"); got != "/-/apps/11/podium-dev/" {
		t.Errorf("ingressBasePath = %q", got)
	}
	if got := ingressBasePath(123, "my-ns"); got != "/-/apps/123/my-ns/" {
		t.Errorf("ingressBasePath = %q", got)
	}
}

// TestProxy_ModifyResponse_InjectsBase exercises the end-to-end
// ModifyResponse path: a fake upstream returns HTML; the proxy must
// serve the same HTML with a <base> tag injected. This is the
// regression test for the "browser loads but CSS is missing" bug —
// the upstream's `<link href="/static/css/app.css">` resolves
// against the page's base, which we set to the proxied app path so
// the request goes back through the proxy.
func TestProxy_ModifyResponse_InjectsBase(t *testing.T) {
	// Fake upstream: serves a single HTML page with a root-relative
	// CSS link.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>sentinel</title><link rel="stylesheet" href="/static/css/app.css"></head><body>ok</body></html>`))
	}))
	defer upstream.Close()

	// Build a Router whose Forwarder points at our fake upstream
	// instead of running kubectl. We construct the ReverseProxy
	// directly via the same logic ServeHTTP uses, but skip the
	// Router.Lookup dance (which would try to talk to a real k8s).
	fwd := &fakeForwarder{dialAddr: upstream.Listener.Addr().String()}
	rp := &httputil.ReverseProxy{
		Director: func(r *http.Request) {
			r.URL.Scheme = "http"
			r.URL.Host = "127.0.0.1:1"
			r.URL.Path = "/"
		},
		Transport: &http.Transport{DialContext: fwd.DialContext},
		ModifyResponse: func(resp *http.Response) error {
			if !isHTML(resp.Header.Get("Content-Type")) {
				return nil
			}
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				return err
			}
			_ = resp.Body.Close()
			body = injectBaseHref(body, ingressBasePath(11, "podium-dev"))
			resp.Body = io.NopCloser(bytes.NewReader(body))
			resp.ContentLength = int64(len(body))
			resp.Header.Set("Content-Length", itoa(len(body)))
			return nil
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/-/apps/11/podium-dev/", nil)
	rp.ServeHTTP(rec, req)

	got := rec.Body.String()
	if !strings.Contains(got, `<base href="/-/apps/11/podium-dev/">`) {
		t.Errorf("response missing injected <base>: %s", got)
	}
	if !strings.Contains(got, `<link rel="stylesheet" href="/static/css/app.css">`) {
		t.Errorf("response missing original CSS link: %s", got)
	}
	// The CSS link must appear AFTER the <base> so the browser
	// resolves `/static/css/app.css` against the base path.
	baseIdx := strings.Index(got, "<base")
	linkIdx := strings.Index(got, `<link rel="stylesheet"`)
	if baseIdx < 0 || linkIdx < 0 || baseIdx > linkIdx {
		t.Errorf("<base> must precede <link>: base=%d link=%d", baseIdx, linkIdx)
	}
}

// itoa is a tiny helper to keep the test self-contained.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
