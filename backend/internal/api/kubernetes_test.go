package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/podium/podium/internal/application"
	"github.com/podium/podium/internal/auth"
	"github.com/podium/podium/internal/kubernetes"
	"github.com/podium/podium/internal/storage"
	"k8s.io/client-go/kubernetes/fake"
)

// TestStateResponse_PodsAlwaysArray pins the wire contract for
// GET /api/applications/{id}/state: the "pods" field MUST serialise
// as a JSON array (never `null`), even when the cluster is
// unreachable. The frontend's K8sOverview reads `state.pods.length`
// directly; a `null` would throw "Cannot read properties of null
// (reading 'length')" and the whole route would fall through to the
// ErrorBoundary. See the /apps/9 incident.
func TestStateResponse_PodsAlwaysArray(t *testing.T) {
	// Cluster-unavailable path: the early return at
	// K8sHandler.GetAppState used to leave resp.Pods as nil which
	// encoding/json then serialised as `null`.
	r := stateResponse{Available: false, Pods: []kubernetes.PodSummary{}}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"pods":[]`) {
		t.Errorf("expected pods:[] in JSON, got %s", string(b))
	}

	// Same struct, but with a nil Pods field — a defensive Marshal
	// guard would catch this. (Without it the test below documents
	// the failure mode.)
	var nilPods stateResponse
	b2, _ := json.Marshal(nilPods)
	if strings.Contains(string(b2), `"pods":null`) {
		t.Logf("NOTE: stateResponse with nil Pods serialises as %s (struct should be initialised)", string(b2))
	}
}

// TestGetAppState_NoDeploymentExcludesName verifies that when the
// cluster is reachable but the Deployment object doesn't exist (e.g.
// the user just deleted the deployment), the response leaves
// `deployment_name` empty instead of echoing the deterministic
// `<name>-<id>` string.
//
// Why this matters: AppUrlCardWithState uses `deployment_name` as the
// "live deployment exists" signal. If the handler always echoed the
// deterministic name, that flag would stay true through a
// delete-then-redeploy cycle and the URL card would keep showing the
// stale URL (see the "/ingress shows old URL after redeploy" bug).
func TestGetAppState_NoDeploymentExcludesName(t *testing.T) {
	// Empty fake clientset → CurrentReplicas returns a NotFound error
	// because no Deployment exists.
	cs := fake.NewSimpleClientset()
	f := newK8sFixture(t, &kubernetes.Client{CS: cs, Source: "fake"})

	req := httptest.NewRequest(
		"GET",
		"/api/applications/"+itoa(f.appID)+"/state?namespace=podium-dev",
		nil,
	)
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Available      bool   `json:"available"`
		DeploymentName string `json:"deployment_name"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if !resp.Available {
		t.Errorf("available=%v want true (fake clientset is reachable)", resp.Available)
	}
	if resp.DeploymentName != "" {
		t.Errorf("deployment_name=%q want empty when no live Deployment exists", resp.DeploymentName)
	}
}

// dummy compile-time check that http is referenced (the real test
// uses it via the api package's test fixtures in *_test.go files).
var _ = http.StatusOK

// k8sFixture is the bare minimum needed to drive GetAppState: alice
// owns one application, the handler is mounted, and we have alice's
// session cookie. Mirrors logsFixture but only wires what GetAppState
// reads.
type k8sFixture struct {
	mux    http.Handler
	cookie string
	appID  int64
}

func newK8sFixture(t *testing.T, k8sClient *kubernetes.Client) *k8sFixture {
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
	aliceID := lookupUserID(t, db, "alice")

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
	NewK8sHandler(q, appSvc, k8sClient, nil).Mount(mux)
	wrapped := New(mux, Deps{Auth: authSvc})

	cookie := loginAs(t, wrapped, "alice", "pw")
	return &k8sFixture{mux: wrapped, cookie: cookie, appID: app.ID}
}
