package ingress

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
)

// User is the subset of the auth user the proxy needs. The api
// middleware satisfies this implicitly: *auth.User has an ID() method
// we can call without importing the auth package here (avoids the
// cycle auth → ingress → auth). We assert via the AuthUser
// interface below.
type User interface {
	UserID() int64
}

// AuthUser lets api code pass an *auth.User into the proxy's context
// without the ingress package importing auth. The api package's
// middleware does the cast and calls WithAuthUser.
type AuthUser interface {
	User
}

type userKey struct{}

// WithAuthUser returns a copy of ctx that carries u. The api
// middleware populates this before invoking the proxy.
func WithAuthUser(ctx context.Context, u AuthUser) context.Context {
	if u == nil {
		return ctx
	}
	return context.WithValue(ctx, userKey{}, u)
}

// userFromContext extracts the AuthUser set by the middleware, or
// nil for anonymous requests.
func userFromContext(r *http.Request) AuthUser {
	v := r.Context().Value(userKey{})
	if v == nil {
		return nil
	}
	u, _ := v.(AuthUser)
	return u
}

// Proxy is an http.Handler that reverse-proxies
// /-/apps/{id}/{namespace}/ and /-/apps/{id}/{namespace}/{path...}
// to the (app, namespace)'s ClusterIP Service via a per-route
// kubectl port-forward managed by the Router.
//
// The proxy is mounted OUTSIDE api.RequireAuth — proxied apps do not
// carry Podium session cookies, and the route is effectively a
// localhost-only tunnel (same trust model as `kubectl port-forward`).
// Authentication is enforced by reading the user from the request
// context (set by the api.withSession middleware); anonymous
// requests are rejected with 401.
//
// Path-stripping: the Director function rewrites
// `/-/apps/{id}/{namespace}/foo/bar` to `/foo/bar` before the
// upstream sees it, so apps see the original URL structure.
//
// Streaming: FlushInterval=-1 keeps the upstream response streaming
// through to the client. Plain HTTP chunked responses work; WebSocket
// is not supported in MVP.
type Proxy struct {
	router *Router
	log    *slog.Logger
}

// NewProxy constructs a Proxy. log may be nil (defaults to slog.Default).
func NewProxy(router *Router, log *slog.Logger) *Proxy {
	if log == nil {
		log = slog.Default()
	}
	return &Proxy{router: router, log: log}
}

// ServeHTTP parses the (id, namespace) pair from the URL path, looks
// up the Forwarder, and reverse-proxies the request. Returns:
//   - 401 for anonymous requests
//   - 404 for unknown app / namespace / invalid path
//   - 502 when the upstream cannot be reached
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	appID, namespace, rest, err := parseIngressPath(r.URL.Path)
	if err != nil {
		http.Error(w, "ingress: "+err.Error(), http.StatusNotFound)
		return
	}

	user := userFromContext(r)
	if user == nil {
		http.Error(w, "ingress: login required", http.StatusUnauthorized)
		return
	}

	fwd, err := p.router.Lookup(r.Context(), appID, namespace, user.UserID())
	if err != nil {
		switch {
		case errors.Is(err, ErrNoSuchApp), errors.Is(err, ErrNoSuchNamespace):
			http.Error(w, "ingress: "+err.Error(), http.StatusNotFound)
		default:
			p.log.Error("ingress lookup",
				slog.Int64("app_id", appID),
				slog.String("ns", namespace),
				slog.String("err", err.Error()),
			)
			http.Error(w, "ingress: app_unavailable", http.StatusBadGateway)
		}
		return
	}

	host := "127.0.0.1:" + strconv.Itoa(fwd.LocalPort())
	rp := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "http"
			req.URL.Host = host
			req.URL.Path = rest
			req.URL.RawQuery = r.URL.RawQuery
			req.Host = host
		},
		Transport: &http.Transport{
			DialContext: fwd.DialContext,
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			p.log.Warn("ingress upstream",
				slog.Int64("app_id", appID),
				slog.String("ns", namespace),
				slog.String("path", r.URL.Path),
				slog.String("err", err.Error()),
			)
			http.Error(w, "ingress: app_unavailable", http.StatusBadGateway)
		},
		FlushInterval: -1,
	}
	rp.ServeHTTP(w, r)
}

// parseIngressPath splits /-/apps/{id}/{namespace}/{rest...} into its
// components. Returns an error for malformed paths.
func parseIngressPath(p string) (appID int64, namespace, rest string, err error) {
	const prefix = "/-/apps/"
	if !strings.HasPrefix(p, prefix) {
		return 0, "", "", errors.New("not an ingress path")
	}
	remainder := strings.TrimPrefix(p, prefix)
	// Strip a trailing slash so /-/apps/1/ns/ and /-/apps/1/ns both work.
	remainder = strings.TrimSuffix(remainder, "/")

	parts := strings.SplitN(remainder, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return 0, "", "", errors.New("expected /-/apps/{id}/{namespace}/...")
	}

	id, perr := strconv.ParseInt(parts[0], 10, 64)
	if perr != nil || id <= 0 {
		return 0, "", "", errors.New("invalid application id")
	}

	if len(parts) == 2 {
		return id, parts[1], "/", nil
	}
	return id, parts[1], "/" + parts[2], nil
}
