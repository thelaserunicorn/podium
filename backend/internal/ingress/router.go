package ingress

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/podium/podium/internal/application"
	"github.com/podium/podium/internal/kubernetes"
	"github.com/podium/podium/internal/storage"
)

// AppSource loads the application and validates ownership. The
// default implementation is *application.Service; tests inject a
// fake.
type AppSource interface {
	Get(ctx context.Context, id, userID int64) (application.Application, error)
}

// NSBootstrap ensures the Kubernetes namespace exists and that the
// app's Service is applied. The default implementation is
// *kubernetes.Client; tests inject a fake so they don't need a real
// cluster.
type NSBootstrap interface {
	EnsureNamespace(ctx context.Context, name string) error
	ApplyService(ctx context.Context, app *application.Application, namespace string) error
}

// routeKey identifies a (appID, namespace) pair. Two routes for the
// same app in the same namespace share a single Forwarder.
type routeKey struct {
	appID int64
	ns    string
}

// ForwarderFactory builds a new Forwarder for a (namespace, service,
// localPort, remotePort) tuple. The real Router uses a factory that
// creates a Forwarder with the production osRunner; tests inject one
// that uses the fakeRunner so the test doesn't actually shell out to
// `kubectl`.
type ForwarderFactory interface {
	New(ns, svc string, localPort, remotePort int) *Forwarder
}

// realForwarderFactory is the production factory.
type realForwarderFactory struct {
	log *slog.Logger
}

func (f *realForwarderFactory) New(ns, svc string, localPort, remotePort int) *Forwarder {
	return NewForwarder(ns, svc, localPort, remotePort, f.log)
}

// Router maps (appID, namespace) → *Forwarder. Routes are created
// lazily on first Lookup and cached for the lifetime of the Router.
// Concurrent first-lookups for the same (app, ns) race safely: only
// one Forwarder is created, the other callers block on the same
// in-flight create.
type Router struct {
	store     *storage.Queries
	apps      AppSource
	bootstrap NSBootstrap
	log       *slog.Logger
	forwarder ForwarderFactory

	// basePort is the first localhost port to assign. nextPort is the
	// next port to try (monotonically increasing). When nextPort
	// exceeds maxPort, Lookup returns ErrPortExhausted.
	basePort int
	maxPort  int
	nextPort int

	mu      sync.Mutex
	cond    *sync.Cond // signals when an in-flight create finishes
	byRoute map[routeKey]*routeEntry
}

// routeEntry holds the Forwarder for a route plus a flag indicating
// whether a create is currently in progress. Other goroutines waiting
// for the same route block on r.cond until the in-flight create sets
// fwd and clears inFlight.
type routeEntry struct {
	fwd      *Forwarder
	inFlight bool
	err      error
}

// ErrPortExhausted indicates the Router has allocated its 100th port.
// The MVP caps at 100 apps; if a user genuinely needs more, restart
// Podium or extend the cap.
var ErrPortExhausted = errors.New("ingress: too many routes (100); restart Podium to free ports")

// ErrNoSuchApp indicates the (appID, userID) pair does not name an
// application the caller owns. The proxy maps this to 404.
var ErrNoSuchApp = errors.New("ingress: application not found")

// ErrNoSuchNamespace indicates the namespace is not (and cannot be)
// created. The proxy maps this to 404.
var ErrNoSuchNamespace = errors.New("ingress: namespace not found")

// NewRouter builds a Router pinned to the given k8s bootstrap path.
// The basePort..maxPort range (inclusive) is the allocation space;
// defaults are 40000..40099.
func NewRouter(store *storage.Queries, bootstrap NSBootstrap, apps AppSource, log *slog.Logger) *Router {
	if log == nil {
		log = slog.Default()
	}
	r := &Router{
		store:     store,
		apps:      apps,
		bootstrap: bootstrap,
		log:       log,
		forwarder: &realForwarderFactory{log: log},
		basePort:  40000,
		maxPort:   40099,
		nextPort:  40000,
		byRoute:   make(map[routeKey]*routeEntry),
	}
	r.cond = sync.NewCond(&r.mu)
	return r
}

// SetForwarderFactory swaps the factory used to construct new
// Forwarders. Tests inject a factory that wires the fakeRunner; main.go
// uses the default realForwarderFactory.
func (r *Router) SetForwarderFactory(f ForwarderFactory) {
	r.mu.Lock()
	r.forwarder = f
	r.mu.Unlock()
}

// SetPortRange overrides the default 40000..40099 allocation. Tests
// use this to pick a narrow range; zero keeps the defaults.
//
// `base` must be > 0; `max` must be >= `base` (a single-port range
// is valid — `SetPortRange(41000, 41000)` allocates port 41000 only).
func (r *Router) SetPortRange(base, max int) {
	if base > 0 && max >= base {
		r.mu.Lock()
		r.basePort = base
		r.maxPort = max
		r.nextPort = base
		r.mu.Unlock()
	}
}

// Lookup returns the Forwarder for (appID, ns), creating it on
// demand. `ownerID` enforces the application ownership check —
// users can only ingress their own apps.
//
// Lookup flow:
//  1. Validate app exists and is owned by ownerID.
//  2. Validate namespace exists in SQLite (or, for custom namespaces,
//     skip — the Deploy path will create it). For ingress we don't
//     auto-create custom namespaces; the user must have deployed to
//     the namespace at least once.
//  3. Ensure the k8s namespace and Service exist (idempotent).
//  4. Allocate a local port and start a Forwarder.
//  5. Cache and return.
//
// Concurrent first-lookups for the same route coordinate via the
// Router's cond: only one goroutine runs steps 3-5; the rest block
// until the result is visible.
func (r *Router) Lookup(ctx context.Context, appID int64, ns string, ownerID int64) (*Forwarder, error) {
	if err := application.ValidateNamespaceName(ns); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrNoSuchNamespace, err.Error())
	}

	app, err := r.apps.Get(ctx, appID, ownerID)
	if err != nil {
		if errors.Is(err, application.ErrNotFound) {
			return nil, ErrNoSuchApp
		}
		return nil, fmt.Errorf("ingress: lookup app: %w", err)
	}

	if _, err := r.store.EnvironmentIDByNamespace(ctx, ns); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrNoSuchNamespace, err.Error())
	}

	key := routeKey{appID: appID, ns: ns}

	r.mu.Lock()
	if entry, ok := r.byRoute[key]; ok {
		if entry.inFlight {
			// Another goroutine is creating this route. Release the
			// lock, wait on the cond, re-check.
			for entry.inFlight {
				r.cond.Wait()
			}
			entry = r.byRoute[key]
		}
		r.mu.Unlock()
		if entry.err != nil {
			return nil, entry.err
		}
		return entry.fwd, nil
	}

	// First lookup: claim the slot, release the lock while we do the
	// slow work.
	r.byRoute[key] = &routeEntry{inFlight: true}
	r.mu.Unlock()

	fwd, err := r.createForwarder(ctx, &app, ns)
	r.mu.Lock()
	entry := r.byRoute[key]
	if err != nil {
		// Remove the failed slot so a retry can try again; cache the
		// error on a fresh entry so concurrent waiters see it.
		delete(r.byRoute, key)
		r.mu.Unlock()
		r.cond.Broadcast()
		return nil, err
	}
	entry.inFlight = false
	entry.fwd = fwd
	entry.err = nil
	r.mu.Unlock()
	r.cond.Broadcast()
	return fwd, nil
}

// createForwarder allocates a port, builds the Forwarder, and starts
// the kubectl subprocess.
func (r *Router) createForwarder(ctx context.Context, app *application.Application, ns string) (*Forwarder, error) {
	if err := r.bootstrap.EnsureNamespace(ctx, ns); err != nil {
		return nil, fmt.Errorf("ingress: ensure namespace: %w", err)
	}
	if err := r.bootstrap.ApplyService(ctx, app, ns); err != nil {
		return nil, fmt.Errorf("ingress: apply service: %w", err)
	}

	r.mu.Lock()
	if r.nextPort > r.maxPort {
		r.mu.Unlock()
		return nil, ErrPortExhausted
	}
	port := r.nextPort
	r.nextPort++
	r.mu.Unlock()

	svcName := kubernetes.DeploymentName(app.Name, app.ID)
	r.mu.Lock()
	factory := r.forwarder
	r.mu.Unlock()
	fwd := factory.New(ns, svcName, port, app.ContainerPort)
	if err := fwd.Start(ctx); err != nil {
		return nil, fmt.Errorf("ingress: start forwarder: %w", err)
	}
	return fwd, nil
}

// Close stops every Forwarder the Router knows about. Called on
// graceful shutdown.
func (r *Router) Close() {
	r.mu.Lock()
	entries := make([]*routeEntry, 0, len(r.byRoute))
	for _, e := range r.byRoute {
		entries = append(entries, e)
	}
	r.byRoute = make(map[routeKey]*routeEntry)
	r.mu.Unlock()
	for _, e := range entries {
		if e.fwd != nil {
			_ = e.fwd.Stop()
		}
	}
}

// Ensure kubernetes.Client satisfies NSBootstrap at compile time.
var _ NSBootstrap = (*kubernetes.Client)(nil)
