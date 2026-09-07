package api

import (
	"log/slog"
	"net/http"

	"github.com/podium/podium/internal/auth"
	"github.com/podium/podium/internal/ingress"
)

// IngressHandler exposes the reverse-proxy entry point at
//
//	/-/apps/{id}/{namespace}/ and /-/apps/{id}/{namespace}/{rest...}
//
// The route lives OUTSIDE the standard /api/ prefix and OUTSIDE the
// RequireAuth wrapper — proxied apps do not carry Podium session
// cookies, and the trust model is "localhost-only tunnel" (the same
// model `kubectl port-forward` uses).
//
// Authentication: the middleware chain sets the user in the request
// context (auth.WithUser) for any request that arrived with a valid
// session cookie, even on /-/ paths. The proxy reads its own
// AuthUser-shaped value via ingress.WithAuthUser; see the shim
// inside Mount for the bridge.
//
// On graceful shutdown the caller is expected to invoke Close
// (Stop() on every kubectl port-forward the proxy kept warm).
type IngressHandler struct {
	proxy  *ingress.Proxy
	router *ingress.Router
	logger *slog.Logger
}

// NewIngressHandler wires the handler around a configured
// Router + Proxy. The router is also retained so main.go can call
// Close() on graceful shutdown.
func NewIngressHandler(router *ingress.Router, proxy *ingress.Proxy, logger *slog.Logger) *IngressHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &IngressHandler{proxy: proxy, router: router, logger: logger}
}

// authUserAdapter satisfies ingress.AuthUser using the auth.User
// already on the context. Defined in api (not auth) so the ingress
// package stays decoupled from the auth package — the plan file
// covers the cycle in proxy.go.
type authUserAdapter struct{ u *auth.User }

func (a *authUserAdapter) UserID() int64 { return a.u.ID }

// Mount registers the ingress proxy on the given mux. The shim wraps
// p.ServeHTTP so it can pull the *auth.User off the context (placed
// there by withSession) and re-publish it as an ingress.AuthUser.
//
// Wrapping order: shim -> proxy.ServeHTTP. The shim never blocks —
// it just rewrites the context value and calls through.
func (h *IngressHandler) Mount(mux *http.ServeMux) {
	shim := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := auth.UserFromContext(r.Context())
		if u != nil {
			ctx := ingress.WithAuthUser(r.Context(), &authUserAdapter{u: u})
			r = r.WithContext(ctx)
		}
		h.proxy.ServeHTTP(w, r)
	})
	// Path-based routing under /-/apps/. The proxy handles every
	// /-/apps/{id}/{namespace}/... shape; the prefix match is done by
	// Go's stdlib ServeMux.
	mux.Handle("/-/apps/", shim)
}

// Router exposes the underlying router so main.go can release the
// port-forwards on shutdown.
func (h *IngressHandler) Router() *ingress.Router { return h.router }
