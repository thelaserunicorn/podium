package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/podium/podium/internal/application"
	"github.com/podium/podium/internal/auth"
	"github.com/podium/podium/internal/kubernetes"
	"github.com/podium/podium/internal/storage"

	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

// logsFixture mirrors envFixture: it spins up alice + bob + a shared
// application, mounts the LogsHandler on a fresh mux, and returns a
// session cookie for alice plus the bob cookie for cross-user tests.
type logsFixture struct {
	mux     http.Handler
	store   *storage.Queries
	cookie  string
	bobCook string
	appID   int64
	aliceID int64
	bobID   int64
}

func newLogsFixture(t *testing.T, k8sClient *kubernetes.Client) *logsFixture {
	t.Helper()
	db := storage.OpenInMemoryForTest(t)
	q := storage.NewQueries(db)
	authSvc := auth.NewService(db)
	appSvc := application.NewService(db)
	ctx := context.Background()

	hash, _ := auth.HashPassword("pw")
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users (username, email, password_hash, role, status) VALUES ('alice','alice@e.x',?, 'USER','APPROVED')`, hash); err != nil {
		t.Fatalf("insert alice: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users (username, email, password_hash, role, status) VALUES ('bob','bob@e.x',?, 'USER','APPROVED')`, hash); err != nil {
		t.Fatalf("insert bob: %v", err)
	}
	aliceID := lookupUserID(t, db, "alice")
	bobID := lookupUserID(t, db, "bob")

	app, err := appSvc.Create(ctx, application.CreateInput{
		Name: "demo", RepositoryURL: "https://github.com/x/y", ContainerPort: 8080,
		UserID: aliceID,
	})
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	mux := http.NewServeMux()
	MountAuth(mux, auth.NewHandler(authSvc))
	MountApplications(mux, application.NewHandler(appSvc))
	NewLogsHandler(q, appSvc, k8sClient, nil).Mount(mux)
	wrapped := New(mux, Deps{Auth: authSvc})

	cookie := loginAs(t, wrapped, "alice", "pw")
	bobCook := loginAs(t, wrapped, "bob", "pw")

	t.Cleanup(func() { _ = appSvc.Delete(ctx, app.ID, aliceID) })
	return &logsFixture{
		mux: wrapped, store: q, cookie: cookie, bobCook: bobCook,
		appID: app.ID, aliceID: aliceID, bobID: bobID,
	}
}

// lookupUserID returns the row id for a username — used to drive
// cross-user ownership tests without depending on the auth package's
// internals.
func lookupUserID(t *testing.T, db *sql.DB, username string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(`SELECT id FROM users WHERE username = ?`, username).Scan(&id); err != nil {
		t.Fatalf("lookup user %s: %v", username, err)
	}
	return id
}

// loginAs posts to /api/auth/login and returns the session cookie
// value (sans attributes).
func loginAs(t *testing.T, mux http.Handler, username, password string) string {
	t.Helper()
	body := strings.NewReader(fmt.Sprintf(`{"username":%q,"password":%q}`, username, password))
	req := httptest.NewRequest("POST", "/api/auth/login", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login %s: %d %s", username, rec.Code, rec.Body.String())
	}
	cookie := rec.Header().Get("Set-Cookie")
	if i := strings.IndexByte(cookie, ';'); i > 0 {
		cookie = cookie[:i]
	}
	if cookie == "" {
		t.Fatalf("login %s: empty cookie", username)
	}
	return cookie
}

// insertDeployment writes a deployment row directly via the store and
// returns its id. Used by /events tests to skip the orchestrator +
// docker path.
func insertDeployment(t *testing.T, q *storage.Queries, appID int64) int64 {
	t.Helper()
	ctx := context.Background()
	env, err := q.GetEnvironmentByNamespace(ctx, "podium-dev")
	if err != nil {
		t.Fatalf("GetEnvironmentByNamespace: %v", err)
	}
	id, err := q.CreateDeployment(ctx, appID, env.ID, 1, 1, "demo:v1")
	if err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}
	return id
}

// --- /api/applications/{id}/logs -----------------------------------------

func TestLogs_RequiresAuth(t *testing.T) {
	f := newLogsFixture(t, nil)
	req := httptest.NewRequest("GET", fmt.Sprintf("/api/applications/%d/logs?pod=p", f.appID), nil)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d want 401", rec.Code)
	}
}

func TestLogs_CrossUserReturns404(t *testing.T) {
	f := newLogsFixture(t, nil)
	req := httptest.NewRequest("GET", fmt.Sprintf("/api/applications/%d/logs?pod=p", f.appID), nil)
	req.Header.Set("Cookie", f.bobCook)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d want 404", rec.Code)
	}
}

func TestLogs_RequiresPodParam(t *testing.T) {
	f := newLogsFixture(t, nil)
	req := httptest.NewRequest("GET", fmt.Sprintf("/api/applications/%d/logs", f.appID), nil)
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d want 400", rec.Code)
	}
}

func TestLogs_RejectsInvalidNamespace(t *testing.T) {
	f := newLogsFixture(t, nil)
	req := httptest.NewRequest("GET", fmt.Sprintf("/api/applications/%d/logs?pod=p&namespace=BadName", f.appID), nil)
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d want 400", rec.Code)
	}
}

func TestLogs_NilClientReturnsAvailableFalse(t *testing.T) {
	f := newLogsFixture(t, nil)
	req := httptest.NewRequest("GET", fmt.Sprintf("/api/applications/%d/logs?pod=p", f.appID), nil)
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Available bool   `json:"available"`
		Namespace string `json:"namespace"`
		Pod       string `json:"pod"`
		Lines     []any  `json:"lines"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Available {
		t.Error("expected available=false")
	}
	if resp.Namespace != "podium-dev" || resp.Pod != "p" {
		t.Errorf("echoed fields: %+v", resp)
	}
}

func TestLogs_HappyPath(t *testing.T) {
	// fake clientset returns empty logs for any pod (it doesn't
	// enforce existence); this proves the wire-through maps
	// available=true when the client is reachable and the line list
	// echoes back the requested pod.
	cs := fake.NewSimpleClientset()
	f := newLogsFixture(t, &kubernetes.Client{CS: cs, Source: "fake"})
	req := httptest.NewRequest("GET", fmt.Sprintf("/api/applications/%d/logs?pod=any", f.appID), nil)
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Available bool   `json:"available"`
		Namespace string `json:"namespace"`
		Pod       string `json:"pod"`
		Lines     []any  `json:"lines"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if !resp.Available {
		t.Error("expected available=true")
	}
	if resp.Pod != "any" || resp.Namespace != "podium-dev" {
		t.Errorf("echoed fields: pod=%q ns=%q", resp.Pod, resp.Namespace)
	}
}

// --- /api/deployments/{id}/events ----------------------------------------

func TestEvents_RequiresAuth(t *testing.T) {
	f := newLogsFixture(t, nil)
	req := httptest.NewRequest("GET", "/api/deployments/1/events", nil)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d want 401", rec.Code)
	}
}

func TestEvents_HappyPath(t *testing.T) {
	now := time.Now()
	cs := fake.NewSimpleClientset(&eventsv1.Event{
		ObjectMeta: metav1.ObjectMeta{Namespace: "podium-dev", Name: "e1"},
		EventTime:  metav1.NewMicroTime(now),
		Reason:     "Pulled",
		Note:       "image pulled",
		Type:       "Normal",
		Regarding: corev1.ObjectReference{
			Kind: "Pod", Name: "demo-1", UID: types.UID("uid-1"),
		},
	})
	f := newLogsFixture(t, &kubernetes.Client{CS: cs, Source: "fake"})
	depID := insertDeployment(t, f.store, f.appID)

	req := httptest.NewRequest("GET", fmt.Sprintf("/api/deployments/%d/events?namespace=podium-dev", depID), nil)
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Available bool   `json:"available"`
		Namespace string `json:"namespace"`
		Events    []struct {
			Reason string `json:"reason"`
		} `json:"events"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if !resp.Available {
		t.Error("available=false")
	}
	if len(resp.Events) != 1 || resp.Events[0].Reason != "Pulled" {
		t.Errorf("events=%+v", resp.Events)
	}
}

func TestEvents_UnknownIDReturns404(t *testing.T) {
	f := newLogsFixture(t, nil)
	req := httptest.NewRequest("GET", "/api/deployments/9999/events", nil)
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d want 404", rec.Code)
	}
}

func TestEvents_CrossUserReturns404(t *testing.T) {
	f := newLogsFixture(t, nil)
	depID := insertDeployment(t, f.store, f.appID)
	req := httptest.NewRequest("GET", fmt.Sprintf("/api/deployments/%d/events", depID), nil)
	req.Header.Set("Cookie", f.bobCook)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d want 404", rec.Code)
	}
}

func TestEvents_NilClientReturnsAvailableFalse(t *testing.T) {
	f := newLogsFixture(t, nil)
	depID := insertDeployment(t, f.store, f.appID)
	req := httptest.NewRequest("GET", fmt.Sprintf("/api/deployments/%d/events", depID), nil)
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Available bool   `json:"available"`
		Namespace string `json:"namespace"`
		Events    []any  `json:"events"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Available {
		t.Error("expected available=false")
	}
	if resp.Namespace != "podium-dev" {
		t.Errorf("namespace=%q", resp.Namespace)
	}
}

func TestEvents_RejectsInvalidNamespace(t *testing.T) {
	f := newLogsFixture(t, nil)
	depID := insertDeployment(t, f.store, f.appID)
	req := httptest.NewRequest("GET", fmt.Sprintf("/api/deployments/%d/events?namespace=BadName", depID), nil)
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d want 400", rec.Code)
	}
}

func TestEvents_RejectsNonIntegerID(t *testing.T) {
	f := newLogsFixture(t, nil)
	req := httptest.NewRequest("GET", "/api/deployments/abc/events", nil)
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d want 400", rec.Code)
	}
}
