package kubernetes

import (
	"context"
	"errors"
	"testing"

	"github.com/podium/podium/internal/application"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

// newTestClient returns a *Client backed by a fake clientset. Tests
// can pre-seed objects by passing them as objects.
func newTestClient(t *testing.T, objects ...runtime.Object) *Client {
	t.Helper()
	cs := fake.NewSimpleClientset(objects...)
	return &Client{CS: cs, Source: "fake"}
}

func TestEnsureNamespace_CreatesAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	if err := c.EnsureNamespace(ctx, "podium-dev"); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	ns, err := c.CS.CoreV1().Namespaces().Get(ctx, "podium-dev", metav1.GetOptions{})
	if err != nil || ns.Name != "podium-dev" {
		t.Fatalf("missing namespace: err=%v ns=%+v", err, ns)
	}
	if err := c.EnsureNamespace(ctx, "podium-dev"); err != nil {
		t.Errorf("second ensure should be no-op, got %v", err)
	}
}

func TestEnsureNamespace_RejectsBadName(t *testing.T) {
	c := newTestClient(t)
	err := c.EnsureNamespace(context.Background(), "BadName")
	if err == nil {
		t.Fatal("expected error for invalid DNS-1123 name")
	}
	if !errors.Is(err, application.ErrInvalidNamespace) {
		t.Errorf("err=%v, want application.ErrInvalidNamespace", err)
	}
}

func TestApplyDeployment_CreatesAndUpdates(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	app := &application.Application{ID: 1, Name: "my-api", ContainerPort: 8080}

	name, err := c.ApplyDeploymentNoEnv(ctx, app, "podium-dev", "my-api:v1", 3)
	if err != nil {
		t.Fatalf("ApplyDeployment: %v", err)
	}
	wantName := DeploymentName("my-api", 1)
	if name != wantName {
		t.Errorf("name=%q want %q", name, wantName)
	}

	got, err := c.CS.AppsV1().Deployments("podium-dev").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if got.Spec.Replicas == nil || *got.Spec.Replicas != 3 {
		t.Errorf("replicas=%v want 3", got.Spec.Replicas)
	}
	if len(got.Spec.Template.Spec.Containers) != 1 ||
		got.Spec.Template.Spec.Containers[0].Image != "my-api:v1" {
		t.Errorf("container mismatch: %+v", got.Spec.Template.Spec.Containers)
	}
	if got.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort != 8080 {
		t.Errorf("port mismatch")
	}

	// Second call must take the update branch and not error.
	if _, err := c.ApplyDeploymentNoEnv(ctx, app, "podium-dev", "my-api:v2", 5); err != nil {
		t.Fatalf("ApplyDeployment update: %v", err)
	}
	updated, _ := c.CS.AppsV1().Deployments("podium-dev").Get(ctx, name, metav1.GetOptions{})
	if updated.Spec.Template.Spec.Containers[0].Image != "my-api:v2" {
		t.Errorf("update did not take: %+v", updated.Spec.Template.Spec.Containers)
	}
	if updated.Spec.Replicas == nil || *updated.Spec.Replicas != 5 {
		t.Errorf("replicas after update=%v want 5", updated.Spec.Replicas)
	}
}

func TestCurrentReplicas(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	app := &application.Application{ID: 7, Name: "demo", ContainerPort: 8080}
	name, _ := c.ApplyDeploymentNoEnv(ctx, app, "podium-dev", "demo:v1", 2)

	// Before the fake has noticed any ReadyReplicas, current should be 0.
	cur, des, err := c.CurrentReplicas(ctx, "podium-dev", name)
	if err != nil {
		t.Fatal(err)
	}
	if cur != 0 || des != 2 {
		t.Errorf("got current=%d desired=%d, want 0/2", cur, des)
	}
}

func TestApplyService_MatchPort(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	app := &application.Application{ID: 42, Name: "svc-app", ContainerPort: 9090}

	if err := c.ApplyService(ctx, app, "podium-dev"); err != nil {
		t.Fatalf("ApplyService: %v", err)
	}
	got, err := c.CS.CoreV1().Services("podium-dev").Get(ctx, DeploymentName("svc-app", 42), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.Type != "ClusterIP" {
		t.Errorf("type=%q want ClusterIP", got.Spec.Type)
	}
	if len(got.Spec.Ports) != 1 || got.Spec.Ports[0].Port != 9090 {
		t.Errorf("ports=%+v", got.Spec.Ports)
	}
	if got.Spec.Ports[0].TargetPort.IntVal != 9090 {
		t.Errorf("targetPort=%v want 9090", got.Spec.Ports[0].TargetPort)
	}
}

func TestListPods_EmptyAndPopulated(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	out, err := c.ListPods(ctx, "podium-dev", "app=demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty, got %d", len(out))
	}
}

func TestDeploymentName_Stable(t *testing.T) {
	got := DeploymentName("my-app", 1)
	want := "my-app-000001"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// TestDeriveKindClusterName: the kubelogin convention is
// "kind-<cluster>" for the kubeconfig context. Stripping the prefix
// recovers the kind cluster name so we can pass `--name` to `kind
// load` and avoid the silent wrong-cluster bug. Contexts that don't
// follow the convention pass through unchanged — `kind load` will
// then either succeed (if it matches a real kind cluster) or fail
// loudly with "cluster not found".
func TestDeriveKindClusterName(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"kind-podium":  "podium",
		"kind":         "kind",
		"kind-my-team": "my-team",
		"orbstack":     "orbstack", // not a kind context — pass through
		"":             "",
	}
	for in, want := range cases {
		if got := deriveKindClusterName(in); got != want {
			t.Errorf("deriveKindClusterName(%q)=%q want %q", in, got, want)
		}
	}
}

// TestDeleteNamespace_RemovesResource: happy path. The namespace
// disappears from the fake clientset after Delete.
func TestDeleteNamespace_RemovesResource(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	if err := c.EnsureNamespace(ctx, "smoke-test"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := c.CS.CoreV1().Namespaces().Get(ctx, "smoke-test", metav1.GetOptions{}); err != nil {
		t.Fatalf("namespace not present after seed: %v", err)
	}

	if err := c.DeleteNamespace(ctx, "smoke-test"); err != nil {
		t.Fatalf("DeleteNamespace: %v", err)
	}
	if _, err := c.CS.CoreV1().Namespaces().Get(ctx, "smoke-test", metav1.GetOptions{}); err == nil {
		t.Fatal("namespace still present after delete")
	}
}

// TestDeleteNamespace_NotFoundIsOk: deleting a name that was never
// created must NOT surface an error — the admin endpoint calls this
// after wiping SQLite, and a SQLite row can exist without a
// corresponding K8s namespace if the cluster was down at create-time.
func TestDeleteNamespace_NotFoundIsOk(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	if err := c.DeleteNamespace(ctx, "ghost"); err != nil {
		t.Fatalf("DeleteNamespace on missing: %v", err)
	}
}

// TestDeleteNamespace_RejectsBadName: same DNS-1123 validation as
// EnsureNamespace — callers must not be able to slip invalid names
// through this side of the pair.
func TestDeleteNamespace_RejectsBadName(t *testing.T) {
	c := newTestClient(t)
	err := c.DeleteNamespace(context.Background(), "BadName")
	if err == nil {
		t.Fatal("expected error for invalid DNS-1123 name")
	}
	if !errors.Is(err, application.ErrInvalidNamespace) {
		t.Errorf("err=%v, want application.ErrInvalidNamespace", err)
	}
}
