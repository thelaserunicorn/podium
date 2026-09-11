package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/podium/podium/internal/auth"
	"github.com/podium/podium/internal/ingress"
)

// ingressURLResponse is the JSON shape returned by
// GET /api/applications/{id}/ingress. The browser opens `url` in a
// new tab; the user sees the proxied app at its own localhost port.
type ingressURLResponse struct {
	URL  string `json:"url"`            // http://127.0.0.1:<port>
	Port int    `json:"port"`           // bare port number, for display
	NS   string `json:"namespace"`      // echoed for client convenience
	App  int64  `json:"application_id"` // echoed for client convenience
}

// IngressURLHandler serves GET /api/applications/{id}/ingress?ns=...
//
// The endpoint:
//  1. Requires an authenticated session.
//  2. Resolves the application by (id, caller-user) — users can only
//     ingress their own apps (404 otherwise).
//  3. Asks the ingress.Router to allocate (or reuse) a Forwarder for
//     (id, ns). The Router eagerly starts the kubectl port-forward,
//     so by the time this handler returns, the URL is reachable.
//
// The Forwarder is owned by the Router — once started it stays
// alive until Podium shuts down (Router.Close) or the process dies.
// We deliberately do not stop it after each request; re-dialing
// across requests would re-create the subprocess each time.
type IngressURLHandler struct {
	router *ingress.Router
	logger *slog.Logger
}

func NewIngressURLHandler(router *ingress.Router, logger *slog.Logger) *IngressURLHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &IngressURLHandler{router: router, logger: logger}
}

// ServeHTTP implements http.Handler.
//
// Query parameters:
//
//	ns — required. Kubernetes namespace the app is deployed to.
//
// Responses:
//
//	200 — {url, port, namespace, application_id}
//	400 — missing/invalid ns
//	404 — unknown app / namespace
//	502 — kubectl failed to start the port-forward (caller sees the
//	      underlying error in the JSON body)
func (h *IngressURLHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user == nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	appID, err := pathInt64(r, "id")
	if err != nil || appID <= 0 {
		http.Error(w, `{"error":"invalid application id"}`, http.StatusBadRequest)
		return
	}

	ns := r.URL.Query().Get("ns")
	if ns == "" {
		http.Error(w, `{"error":"missing ns query parameter"}`, http.StatusBadRequest)
		return
	}

	fwd, err := h.router.Lookup(r.Context(), appID, ns, user.ID)
	if err != nil {
		switch {
		case errors.Is(err, ingress.ErrNoSuchApp):
			http.Error(w, `{"error":"application not found"}`, http.StatusNotFound)
		case errors.Is(err, ingress.ErrNoSuchNamespace):
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusNotFound)
		default:
			h.logger.Error("ingress lookup",
				slog.Int64("app_id", appID),
				slog.String("ns", ns),
				slog.String("err", err.Error()),
			)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error":  "app_unavailable",
				"detail": err.Error(),
			})
		}
		return
	}

	resp := ingressURLResponse{
		URL:  fwd.LocalURL(),
		Port: fwd.LocalPort(),
		NS:   ns,
		App:  appID,
	}
	w.Header().Set("Content-Type", "application/json")
	// The URL is volatile — the backend's Router evicts the cached
	// Forwarder on every delete+redeploy and allocates a new port.
	// Forbid caching on the client AND any intermediate proxy so a
	// subsequent poll cannot reuse the previous (now-dead) URL.
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// pathInt64 is a small helper for extracting a {id} path segment as
// int64. The standard library's PathValue returns a string; this is
// the only place we need int64 conversion, so a tiny helper beats
// adding a strconv import to multiple files.
func pathInt64(r *http.Request, name string) (int64, error) {
	return strconv.ParseInt(r.PathValue(name), 10, 64)
}

// MountIngressURL registers the /api/applications/{id}/ingress
// endpoint on the mux, wrapped in RequireAuth. The endpoint returns
// the localhost URL of the kubectl port-forward for the app in the
// requested namespace. main.go calls this once at startup.
func MountIngressURL(mux *http.ServeMux, h *IngressURLHandler) {
	mux.Handle("GET /api/applications/{id}/ingress", RequireAuth(h))
}
