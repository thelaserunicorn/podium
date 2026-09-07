package kubernetes

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/podium/podium/internal/application"
)

// seedAppResources creates a Deployment, Service, ConfigMap, and Secret
// in the supplied namespace for the given app — enough to assert that
// DeleteAppResources sweeps all of them.
func seedAppResources(t *testing.T, c *Client, app *application.Application, ns string) {
	t.Helper()
	ctx := context.Background()
	name := DeploymentName(app.Name, app.ID)

	if _, err := c.ApplyDeploymentNoEnv(ctx, app, ns, "demo:v1", 1); err != nil {
		t.Fatalf("seed deployment: %v", err)
	}
	if err := c.ApplyService(ctx, app, ns); err != nil {
		t.Fatalf("seed service: %v", err)
	}
	if err := c.ApplyConfigMap(ctx, app.Name, app.ID, ns, map[string]string{"FOO": "bar"}); err != nil {
		t.Fatalf("seed configmap: %v", err)
	}
	if err := c.ApplySecret(ctx, app.Name, app.ID, ns, map[string]string{"DB": "x"}); err != nil {
		t.Fatalf("seed secret: %v", err)
	}

	// Sanity-check: all four should be present after seeding.
	if _, err := c.CS.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{}); err != nil {
		t.Fatalf("seeded deployment missing: %v", err)
	}
	if _, err := c.CS.CoreV1().Services(ns).Get(ctx, name, metav1.GetOptions{}); err != nil {
		t.Fatalf("seeded service missing: %v", err)
	}
	if _, err := c.CS.CoreV1().ConfigMaps(ns).Get(ctx, name, metav1.GetOptions{}); err != nil {
		t.Fatalf("seeded configmap missing: %v", err)
	}
	if _, err := c.CS.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{}); err != nil {
		t.Fatalf("seeded secret missing: %v", err)
	}
}

func assertAllGone(t *testing.T, c *Client, app *application.Application, ns string) {
	t.Helper()
	ctx := context.Background()
	name := DeploymentName(app.Name, app.ID)
	for _, check := range []struct {
		kind string
		do   func() error
	}{
		{"deployment", func() error { _, err := c.CS.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{}); return err }},
		{"service", func() error { _, err := c.CS.CoreV1().Services(ns).Get(ctx, name, metav1.GetOptions{}); return err }},
		{"configmap", func() error { _, err := c.CS.CoreV1().ConfigMaps(ns).Get(ctx, name, metav1.GetOptions{}); return err }},
		{"secret", func() error { _, err := c.CS.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{}); return err }},
	} {
		err := check.do()
		if err == nil {
			t.Errorf("%s should be gone", check.kind)
			continue
		}
		if !apierrors.IsNotFound(err) {
			t.Errorf("%s: unexpected error: %v", check.kind, err)
		}
	}
}

func TestDeleteAppResources_RemovesEverything(t *testing.T) {
	c := newTestClient(t)
	app := &application.Application{ID: 1, Name: "demo", ContainerPort: 8080}
	seedAppResources(t, c, app, "podium-dev")

	if err := c.DeleteAppResources(context.Background(), app, "podium-dev"); err != nil {
		t.Fatalf("DeleteAppResources: %v", err)
	}
	assertAllGone(t, c, app, "podium-dev")
}

func TestDeleteAppResources_IdempotentOnMissing(t *testing.T) {
	// Calling delete on a namespace where nothing was ever deployed
	// must not error — every NotFound is treated as success.
	c := newTestClient(t)
	app := &application.Application{ID: 42, Name: "ghost", ContainerPort: 8080}
	if err := c.DeleteAppResources(context.Background(), app, "podium-dev"); err != nil {
		t.Fatalf("DeleteAppResources on empty namespace: %v", err)
	}
}

func TestDeleteAppResources_LeavesOtherAppsAlone(t *testing.T) {
	c := newTestClient(t)
	a := &application.Application{ID: 1, Name: "keep", ContainerPort: 8080}
	b := &application.Application{ID: 2, Name: "drop", ContainerPort: 8080}
	seedAppResources(t, c, a, "podium-dev")
	seedAppResources(t, c, b, "podium-dev")

	if err := c.DeleteAppResources(context.Background(), b, "podium-dev"); err != nil {
		t.Fatalf("DeleteAppResources: %v", err)
	}

	// `keep` should still have all four resources; `drop` should have none.
	ctx := context.Background()
	keepName := DeploymentName(a.Name, a.ID)
	for _, check := range []struct {
		kind string
		do   func() error
	}{
		{"deployment", func() error {
			_, err := c.CS.AppsV1().Deployments("podium-dev").Get(ctx, keepName, metav1.GetOptions{})
			return err
		}},
		{"service", func() error {
			_, err := c.CS.CoreV1().Services("podium-dev").Get(ctx, keepName, metav1.GetOptions{})
			return err
		}},
		{"configmap", func() error {
			_, err := c.CS.CoreV1().ConfigMaps("podium-dev").Get(ctx, keepName, metav1.GetOptions{})
			return err
		}},
		{"secret", func() error {
			_, err := c.CS.CoreV1().Secrets("podium-dev").Get(ctx, keepName, metav1.GetOptions{})
			return err
		}},
	} {
		if err := check.do(); err != nil {
			t.Errorf("keep-app %s should still exist: %v", check.kind, err)
		}
	}
	assertAllGone(t, c, b, "podium-dev")
}

func TestDeleteDeployment_RemovesOnlyDeployment(t *testing.T) {
	c := newTestClient(t)
	app := &application.Application{ID: 5, Name: "single", ContainerPort: 8080}
	seedAppResources(t, c, app, "podium-dev")

	if err := c.DeleteDeployment(context.Background(), "podium-dev", DeploymentName(app.Name, app.ID)); err != nil {
		t.Fatalf("DeleteDeployment: %v", err)
	}

	// Deployment gone, Service + ConfigMap + Secret still present
	// (so a future deploy can reuse them).
	ctx := context.Background()
	name := DeploymentName(app.Name, app.ID)
	if _, err := c.CS.AppsV1().Deployments("podium-dev").Get(ctx, name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("deployment not deleted: %v", err)
	}
	for _, check := range []struct {
		kind string
		do   func() error
	}{
		{"service", func() error {
			_, err := c.CS.CoreV1().Services("podium-dev").Get(ctx, name, metav1.GetOptions{})
			return err
		}},
		{"configmap", func() error {
			_, err := c.CS.CoreV1().ConfigMaps("podium-dev").Get(ctx, name, metav1.GetOptions{})
			return err
		}},
		{"secret", func() error {
			_, err := c.CS.CoreV1().Secrets("podium-dev").Get(ctx, name, metav1.GetOptions{})
			return err
		}},
	} {
		if err := check.do(); err != nil {
			t.Errorf("%s should still exist: %v", check.kind, err)
		}
	}
}

// ensure the imports for the linter
var _ = types.UID("")
var _ = corev1.ServiceTypeClusterIP
