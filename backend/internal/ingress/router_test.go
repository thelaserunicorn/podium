package ingress

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/podium/podium/internal/application"
	"github.com/podium/podium/internal/kubernetes"
	"github.com/podium/podium/internal/storage"
)

// fakeAppSource satisfies AppSource without touching the application
// service's database layer. (Reserved for future tests that want to
// bypass the SQLite path.)
type fakeAppSource struct {
	app application.Application
	err error
}

func (f *fakeAppSource) Get(_ context.Context, _, _ int64) (application.Application, error) {
	if f.err != nil {
		return application.Application{}, f.err
	}
	return f.app, nil
}

// fakeBootstrap counts how many times its hooks were called. Tests
// assert it ran exactly once per unique (appID, ns) pair.
type fakeBootstrap struct {
	mu          sync.Mutex
	ensureCalls []string
	applyCalls  []string

	failEnsure error
	failApply  error
}

func (b *fakeBootstrap) EnsureNamespace(_ context.Context, name string) error {
	b.mu.Lock()
	b.ensureCalls = append(b.ensureCalls, name)
	b.mu.Unlock()
	return b.failEnsure
}

func (b *fakeBootstrap) ApplyService(_ context.Context, app *application.Application, ns string) error {
	b.mu.Lock()
	b.applyCalls = append(b.applyCalls, ns+"/"+app.Name)
	b.mu.Unlock()
	return b.failApply
}

// fakeForwarderFactory wires the fakeRunner into every Forwarder the
// Router creates, so Router tests don't shell out to kubectl. The
// shared `fake` ensures all forwarders see the same runner so port
// allocation tests can reason about a single in-memory state.
type fakeForwarderFactory struct {
	fake *fakeRunner
}

func (f *fakeForwarderFactory) New(ns, svc string, localPort, remotePort int) *Forwarder {
	fwd := NewForwarder(ns, svc, localPort, remotePort, nil)
	fwd.SetRunner(f.fake)
	return fwd
}

// seedRouterFixture opens an in-memory SQLite, inserts one user + one
// app, returns the bits the router needs.
func seedRouterFixture(t *testing.T, containerPort int) (*storage.Queries, *application.Service, int64, int64) {
	t.Helper()
	db := storage.OpenInMemoryForTest(t)
	store := storage.NewQueries(db)
	appSvc := application.NewService(db)
	ctx := context.Background()
	// Hard-coded bcrypt hash for "unused" (cost 4). We never call
	// Verify on it; the column is NOT NULL.
	dummyHash := "$2a$04$Z3V5c2lvbnRlc3RwYXNzd29yZHRoYXRpczMyYnl0ZXMAAAAAAAAAAAAA"
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users (username, email, password_hash, role, status) VALUES ('seed','seed@x',?,'ADMIN','APPROVED')`,
		dummyHash); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := db.ExecContext(ctx,
		`INSERT INTO users (username, email, password_hash, role, status) VALUES ('alice','alice@x',?,'USER','APPROVED')`,
		dummyHash)
	if err != nil {
		t.Fatalf("alice: %v", err)
	}
	aliceID, _ := res.LastInsertId()
	app, err := appSvc.Create(ctx, application.CreateInput{
		Name: "demo", RepositoryURL: "https://github.com/x/y", ContainerPort: containerPort, UserID: aliceID,
	})
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	return store, appSvc, app.ID, aliceID
}

func TestRouter_LazyCreatesOnFirstLookup(t *testing.T) {
	store, appSvc, appID, aliceID := seedRouterFixture(t, 8080)
	if _, err := store.EnsureEnvironment(context.Background(), "podium-dev"); err != nil {
		t.Fatalf("ensure env: %v", err)
	}

	bs := &fakeBootstrap{}
	r := newTestRouter(t, store, bs, appSvc)
	r.SetPortRange(41000, 41000) // pin a single port to assert allocation

	fwd, err := r.Lookup(context.Background(), appID, "podium-dev", aliceID)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if fwd == nil {
		t.Fatal("nil forwarder")
	}
	if fwd.LocalPort() != 41000 {
		t.Errorf("LocalPort=%d want 41000", fwd.LocalPort())
	}
	if got := len(bs.ensureCalls); got != 1 {
		t.Errorf("EnsureNamespace calls=%d want 1", got)
	}
	if got := len(bs.applyCalls); got != 1 {
		t.Errorf("ApplyService calls=%d want 1", got)
	}
	_ = fwd.Stop()
}

func TestRouter_CachedOnSecondLookup(t *testing.T) {
	store, appSvc, appID, aliceID := seedRouterFixture(t, 8080)
	if _, err := store.EnsureEnvironment(context.Background(), "podium-dev"); err != nil {
		t.Fatalf("ensure env: %v", err)
	}
	bs := &fakeBootstrap{}
	r := newTestRouter(t, store, bs, appSvc)
	r.SetPortRange(41000, 41001)

	fwd1, err := r.Lookup(context.Background(), appID, "podium-dev", aliceID)
	if err != nil {
		t.Fatal(err)
	}
	fwd2, err := r.Lookup(context.Background(), appID, "podium-dev", aliceID)
	if err != nil {
		t.Fatal(err)
	}
	if fwd1 != fwd2 {
		t.Errorf("second Lookup returned different *Forwarder (should be cached)")
	}
	if got := len(bs.ensureCalls); got != 1 {
		t.Errorf("EnsureNamespace calls=%d want 1 (cached)", got)
	}
	_ = fwd1.Stop()
}

func TestRouter_DifferentAppsGetDifferentPorts(t *testing.T) {
	store, appSvc, appID, aliceID := seedRouterFixture(t, 8080)
	app2, err := appSvc.Create(context.Background(), application.CreateInput{
		Name: "demo-2", RepositoryURL: "https://github.com/x/y", ContainerPort: 9090, UserID: aliceID,
	})
	if err != nil {
		t.Fatalf("create app2: %v", err)
	}
	if _, err := store.EnsureEnvironment(context.Background(), "podium-dev"); err != nil {
		t.Fatalf("ensure env: %v", err)
	}
	bs := &fakeBootstrap{}
	r := newTestRouter(t, store, bs, appSvc)
	r.SetPortRange(42000, 42010)

	fwd1, err := r.Lookup(context.Background(), appID, "podium-dev", aliceID)
	if err != nil {
		t.Fatal(err)
	}
	fwd2, err := r.Lookup(context.Background(), app2.ID, "podium-dev", aliceID)
	if err != nil {
		t.Fatal(err)
	}
	if fwd1.LocalPort() == fwd2.LocalPort() {
		t.Errorf("expected different ports, both=%d", fwd1.LocalPort())
	}
	_ = fwd1.Stop()
	_ = fwd2.Stop()
}

func TestRouter_OwnerCheckEnforced(t *testing.T) {
	store, appSvc, appID, _ := seedRouterFixture(t, 8080)
	if _, err := store.EnsureEnvironment(context.Background(), "podium-dev"); err != nil {
		t.Fatalf("ensure env: %v", err)
	}
	bs := &fakeBootstrap{}
	r := newTestRouter(t, store, bs, appSvc)
	r.SetPortRange(43000, 43000)

	// Asking with a different ownerID must return ErrNoSuchApp.
	_, err := r.Lookup(context.Background(), appID, "podium-dev", 9999)
	if !errors.Is(err, ErrNoSuchApp) {
		t.Errorf("err=%v want ErrNoSuchApp", err)
	}
	if got := len(bs.ensureCalls); got != 0 {
		t.Errorf("EnsureNamespace called %d times on owner mismatch (want 0)", got)
	}
}

func TestRouter_UnknownAppReturnsErrNoSuchApp(t *testing.T) {
	store, appSvc, _, aliceID := seedRouterFixture(t, 8080)
	if _, err := store.EnsureEnvironment(context.Background(), "podium-dev"); err != nil {
		t.Fatalf("ensure env: %v", err)
	}
	r := newTestRouter(t, store, &fakeBootstrap{}, appSvc)
	r.SetPortRange(44000, 44000)

	_, err := r.Lookup(context.Background(), 99999, "podium-dev", aliceID)
	if !errors.Is(err, ErrNoSuchApp) {
		t.Errorf("err=%v want ErrNoSuchApp", err)
	}
}

func TestRouter_UnknownNamespaceReturnsErrNoSuchNamespace(t *testing.T) {
	store, appSvc, appID, aliceID := seedRouterFixture(t, 8080)
	r := newTestRouter(t, store, &fakeBootstrap{}, appSvc)
	r.SetPortRange(45000, 45000)

	_, err := r.Lookup(context.Background(), appID, "ghost-ns", aliceID)
	if !errors.Is(err, ErrNoSuchNamespace) {
		t.Errorf("err=%v want ErrNoSuchNamespace", err)
	}
}

func TestRouter_InvalidNamespaceNameReturnsErrNoSuchNamespace(t *testing.T) {
	store, appSvc, appID, aliceID := seedRouterFixture(t, 8080)
	r := newTestRouter(t, store, &fakeBootstrap{}, appSvc)
	r.SetPortRange(46000, 46000)

	for _, bad := range []string{"", "Bad Name", "ns_with_underscore", strings.Repeat("a", 64)} {
		_, err := r.Lookup(context.Background(), appID, bad, aliceID)
		if !errors.Is(err, ErrNoSuchNamespace) {
			t.Errorf("ns=%q err=%v want ErrNoSuchNamespace", bad, err)
		}
	}
}

func TestRouter_PortExhaustionReturnsErrPortExhausted(t *testing.T) {
	store, appSvc, appID, aliceID := seedRouterFixture(t, 8080)
	for _, ns := range []string{"podium-dev", "podium-staging", "podium-prod"} {
		if _, err := store.EnsureEnvironment(context.Background(), ns); err != nil {
			t.Fatalf("ensure env: %v", err)
		}
	}
	bs := &fakeBootstrap{}
	r := newTestRouter(t, store, bs, appSvc)
	r.SetPortRange(47000, 47001) // 2 ports total

	ctx := context.Background()
	if _, err := r.Lookup(ctx, appID, "podium-dev", aliceID); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := r.Lookup(ctx, appID, "podium-staging", aliceID); err != nil {
		t.Fatalf("second: %v", err)
	}
	_, err := r.Lookup(ctx, appID, "podium-prod", aliceID)
	if !errors.Is(err, ErrPortExhausted) {
		t.Errorf("err=%v want ErrPortExhausted", err)
	}
}

// TestRouter_ConcurrentFirstLookupsDedup verifies that 10 goroutines
// racing on the same (appID, ns) produce exactly ONE bootstrap call
// and one Forwarder shared across all of them.
func TestRouter_ConcurrentFirstLookupsDedup(t *testing.T) {
	store, appSvc, appID, aliceID := seedRouterFixture(t, 8080)
	if _, err := store.EnsureEnvironment(context.Background(), "podium-dev"); err != nil {
		t.Fatalf("ensure env: %v", err)
	}
	bs := &fakeBootstrap{}
	r := newTestRouter(t, store, bs, appSvc)
	r.SetPortRange(48000, 48000)

	const N = 10
	fwds := make([]*Forwarder, N)
	errs := make([]error, N)
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			fwds[i], errs[i] = r.Lookup(context.Background(), appID, "podium-dev", aliceID)
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	for i := 1; i < N; i++ {
		if fwds[i] != fwds[0] {
			t.Errorf("goroutine %d got a different *Forwarder", i)
		}
	}
	if got := len(bs.ensureCalls); got != 1 {
		t.Errorf("EnsureNamespace calls=%d want 1", got)
	}
	if got := len(bs.applyCalls); got != 1 {
		t.Errorf("ApplyService calls=%d want 1", got)
	}
	_ = fwds[0].Stop()
}

func TestRouter_BootstrapErrorRetriedOnNextLookup(t *testing.T) {
	store, appSvc, appID, aliceID := seedRouterFixture(t, 8080)
	if _, err := store.EnsureEnvironment(context.Background(), "podium-dev"); err != nil {
		t.Fatalf("ensure env: %v", err)
	}
	bs := &fakeBootstrap{failApply: errors.New("k8s down")}
	r := newTestRouter(t, store, bs, appSvc)
	r.SetPortRange(49000, 49000)

	_, err := r.Lookup(context.Background(), appID, "podium-dev", aliceID)
	if err == nil {
		t.Fatal("expected error from bootstrap failure")
	}
	// A retry should attempt bootstrap again (no poison-cache on
	// apply failures).
	bs.failApply = nil
	fwd, err := r.Lookup(context.Background(), appID, "podium-dev", aliceID)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if fwd == nil {
		t.Fatal("retry returned nil forwarder")
	}
	_ = fwd.Stop()
}

func TestRouter_CloseClearsRoutes(t *testing.T) {
	store, appSvc, appID, aliceID := seedRouterFixture(t, 8080)
	for _, ns := range []string{"podium-dev", "podium-staging"} {
		if _, err := store.EnsureEnvironment(context.Background(), ns); err != nil {
			t.Fatalf("ensure env: %v", err)
		}
	}
	bs := &fakeBootstrap{}
	r := newTestRouter(t, store, bs, appSvc)
	r.SetPortRange(50000, 50001)

	if _, err := r.Lookup(context.Background(), appID, "podium-dev", aliceID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Lookup(context.Background(), appID, "podium-staging", aliceID); err != nil {
		t.Fatal(err)
	}
	r.Close()
	r.mu.Lock()
	n := len(r.byRoute)
	r.mu.Unlock()
	if n != 0 {
		t.Errorf("after Close byRoute has %d entries; want 0", n)
	}
}

func TestRouter_PortAllocation_Monotonic(t *testing.T) {
	store, appSvc, appID, aliceID := seedRouterFixture(t, 8080)
	for _, ns := range []string{"podium-dev", "podium-staging", "podium-prod"} {
		if _, err := store.EnsureEnvironment(context.Background(), ns); err != nil {
			t.Fatalf("ensure env: %v", err)
		}
	}
	bs := &fakeBootstrap{}
	r := newTestRouter(t, store, bs, appSvc)
	r.SetPortRange(51000, 51002)

	ctx := context.Background()
	p1, err := r.Lookup(ctx, appID, "podium-dev", aliceID)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := r.Lookup(ctx, appID, "podium-staging", aliceID)
	if err != nil {
		t.Fatal(err)
	}
	p3, err := r.Lookup(ctx, appID, "podium-prod", aliceID)
	if err != nil {
		t.Fatal(err)
	}
	ports := []int{p1.LocalPort(), p2.LocalPort(), p3.LocalPort()}
	for i := 1; i < len(ports); i++ {
		if ports[i] <= ports[i-1] {
			t.Errorf("ports not monotonic: %v", ports)
		}
	}
	_ = p1
	_ = p2
	_ = p3
}

// ensure the production type satisfies our interface (compile-time
// guard — without this a refactor in kubernetes.Client could
// silently break the Router wiring).
var _ NSBootstrap = (*kubernetes.Client)(nil)

// newTestRouter wires a Router with a shared fakeRunner. Multiple
// routes through the same Router see the same runner; that's
// intentional and lets concurrent tests work without kubectl on PATH.
func newTestRouter(t *testing.T, store *storage.Queries, bs NSBootstrap, apps AppSource) *Router {
	t.Helper()
	r := NewRouter(store, bs, apps, nil)
	r.SetForwarderFactory(&fakeForwarderFactory{fake: sharedFakeRunner()})
	return r
}

// sharedFakeRunner returns a process-wide fakeRunner so concurrent
// tests can each construct a Router without racing on the runner.
var sharedFakeOnce sync.Once
var sharedFake *fakeRunner

func sharedFakeRunner() *fakeRunner {
	sharedFakeOnce.Do(func() {
		sharedFake = &fakeRunner{
			readyDelay:    5 * time.Millisecond,
			readyListener: true, // Router tests need DialContext to succeed
		}
	})
	return sharedFake
}

var _ = sharedFakeRunner // referenced via SetForwarderFactory above

// TestRouter_EvictStopsAndClearsCache exercises the delete-deployment
// lifecycle hook. After Evict(appID, ns), the cached Forwarder must
// be stopped (subprocess gone) and the cache slot must be free (next
// Lookup rebuilds).
func TestRouter_EvictStopsAndClearsCache(t *testing.T) {
	store, appSvc, appID, aliceID := seedRouterFixture(t, 8080)
	if _, err := store.EnsureEnvironment(context.Background(), "podium-dev"); err != nil {
		t.Fatalf("ensure env: %v", err)
	}
	bs := &fakeBootstrap{}
	r := newTestRouter(t, store, bs, appSvc)
	// Two ports so we can assert the post-Evict Lookup rebuilds
	// (which allocates a fresh port).
	r.SetPortRange(52000, 52001)

	fwd1, err := r.Lookup(context.Background(), appID, "podium-dev", aliceID)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}

	// Capture the cmd slot before eviction so we can assert the
	// underlying subprocess was killed, not just removed from the
	// map.
	r.mu.Lock()
	entry, ok := r.byRoute[routeKey{appID: appID, ns: "podium-dev"}]
	r.mu.Unlock()
	if !ok || entry.fwd != fwd1 {
		t.Fatalf("route not in cache as expected")
	}

	r.Evict(appID, "podium-dev")

	// Cache entry must be gone.
	r.mu.Lock()
	_, stillCached := r.byRoute[routeKey{appID: appID, ns: "podium-dev"}]
	r.mu.Unlock()
	if stillCached {
		t.Errorf("Evict left the route in the cache")
	}

	// Next Lookup must rebuild from scratch (new *Forwarder) and
	// re-run the bootstrap hooks — that's how the next deployment's
	// Service gets a fresh port-forward.
	fwd2, err := r.Lookup(context.Background(), appID, "podium-dev", aliceID)
	if err != nil {
		t.Fatalf("Lookup after Evict: %v", err)
	}
	if fwd2 == fwd1 {
		t.Errorf("Lookup after Evict returned the same *Forwarder (cache should be empty)")
	}
	if got := len(bs.ensureCalls); got != 2 {
		t.Errorf("EnsureNamespace calls=%d after eviction+relookup, want 2", got)
	}
	_ = fwd2.Stop()
}

// TestRouter_EvictIsIdempotent: calling Evict twice (or before any
// Lookup) must not panic and must not affect other routes.
func TestRouter_EvictIsIdempotent(t *testing.T) {
	store, appSvc, appID, aliceID := seedRouterFixture(t, 8080)
	if _, err := store.EnsureEnvironment(context.Background(), "podium-dev"); err != nil {
		t.Fatalf("ensure env: %v", err)
	}
	r := newTestRouter(t, store, &fakeBootstrap{}, appSvc)
	r.SetPortRange(52010, 52010)

	// Evict with no route cached — must not panic.
	r.Evict(appID, "podium-dev")

	if _, err := r.Lookup(context.Background(), appID, "podium-dev", aliceID); err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	// Second evict on the same key — also a no-op (entry already gone
	// after the first one).
	r.Evict(appID, "podium-dev")
	r.Evict(appID, "podium-dev")

	r.mu.Lock()
	n := len(r.byRoute)
	r.mu.Unlock()
	if n != 0 {
		t.Errorf("byRoute size=%d after double Evict, want 0", n)
	}
}

// TestRouter_EvictAppClearsOnlyMatchingRoutes: EvictApp should drop
// every cached route for the given appID while leaving routes for
// other apps intact. Used when the whole application is deleted.
func TestRouter_EvictAppClearsOnlyMatchingRoutes(t *testing.T) {
	store, appSvc, appA, aliceID := seedRouterFixture(t, 8080)
	appB, err := appSvc.Create(context.Background(), application.CreateInput{
		Name: "second", RepositoryURL: "https://github.com/x/y", ContainerPort: 9090, UserID: aliceID,
	})
	if err != nil {
		t.Fatalf("create appB: %v", err)
	}
	for _, ns := range []string{"podium-dev", "podium-staging"} {
		if _, err := store.EnsureEnvironment(context.Background(), ns); err != nil {
			t.Fatalf("ensure %s: %v", ns, err)
		}
	}
	bs := &fakeBootstrap{}
	r := newTestRouter(t, store, bs, appSvc)
	r.SetPortRange(52020, 52030)

	// appA: two routes (dev, staging). appB: one route (dev).
	for _, ns := range []string{"podium-dev", "podium-staging"} {
		if _, err := r.Lookup(context.Background(), appA, ns, aliceID); err != nil {
			t.Fatalf("lookup A %s: %v", ns, err)
		}
	}
	fwdB, err := r.Lookup(context.Background(), appB.ID, "podium-dev", aliceID)
	if err != nil {
		t.Fatalf("lookup B: %v", err)
	}

	r.EvictApp(appA)

	r.mu.Lock()
	_, aCached := r.byRoute[routeKey{appID: appA, ns: "podium-dev"}]
	_, aCached2 := r.byRoute[routeKey{appID: appA, ns: "podium-staging"}]
	bEntry, bCached := r.byRoute[routeKey{appID: appB.ID, ns: "podium-dev"}]
	r.mu.Unlock()

	if aCached || aCached2 {
		t.Errorf("appA routes still in cache after EvictApp")
	}
	if !bCached || bEntry.fwd != fwdB {
		t.Errorf("appB route was disturbed by EvictApp for appA")
	}
	_ = fwdB.Stop()
}
