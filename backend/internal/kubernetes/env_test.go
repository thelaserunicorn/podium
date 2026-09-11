package kubernetes

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestApplyConfigMap_CreatesAndUpdates(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	if err := c.ApplyConfigMap(ctx, "demo", 1, "podium-dev", map[string]string{"FOO": "bar"}); err != nil {
		t.Fatalf("ApplyConfigMap: %v", err)
	}
	cm, err := c.CS.CoreV1().ConfigMaps("podium-dev").Get(ctx, EnvConfigMapName("demo", 1), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get configmap: %v", err)
	}
	if cm.Data["FOO"] != "bar" {
		t.Errorf("data[FOO]=%q want bar", cm.Data["FOO"])
	}

	// Update path.
	if err := c.ApplyConfigMap(ctx, "demo", 1, "podium-dev", map[string]string{"FOO": "baz", "NEW": "v"}); err != nil {
		t.Fatalf("ApplyConfigMap update: %v", err)
	}
	cm, _ = c.CS.CoreV1().ConfigMaps("podium-dev").Get(ctx, EnvConfigMapName("demo", 1), metav1.GetOptions{})
	if cm.Data["FOO"] != "baz" || cm.Data["NEW"] != "v" {
		t.Errorf("after update: %+v", cm.Data)
	}
}

func TestApplyConfigMap_EmptyMapIsNoOp(t *testing.T) {
	c := newTestClient(t)
	if err := c.ApplyConfigMap(context.Background(), "demo", 1, "podium-dev", nil); err != nil {
		t.Errorf("expected nil error for empty map, got %v", err)
	}
	// Confirm nothing was created.
	_, err := c.CS.CoreV1().ConfigMaps("podium-dev").Get(context.Background(),
		EnvConfigMapName("demo", 1), metav1.GetOptions{})
	if err == nil {
		t.Error("expected no configmap to be created")
	}
}

func TestApplySecret_StoresValuesVerbatim(t *testing.T) {
	// Per DECISIONS.md E, secret env-var values go into a Kubernetes
	// Secret. The apiserver handles on-the-wire base64 encoding of
	// Secret.Data; Podium does NOT additionally encode. The pod
	// receives the plaintext as the env var via envFrom.
	ctx := context.Background()
	c := newTestClient(t)

	plain := map[string]string{"DB_PASSWORD": "hunter2"}
	if err := c.ApplySecret(ctx, "demo", 1, "podium-dev", plain); err != nil {
		t.Fatalf("ApplySecret: %v", err)
	}
	sec, err := c.CS.CoreV1().Secrets("podium-dev").Get(ctx, EnvSecretName("demo", 1), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if string(sec.Data["DB_PASSWORD"]) != "hunter2" {
		t.Errorf("Secret.Data[DB_PASSWORD]=%q want hunter2 (no double-encoding)",
			string(sec.Data["DB_PASSWORD"]))
	}
	if sec.Type != corev1.SecretTypeOpaque {
		t.Errorf("Secret.Type=%v want Opaque", sec.Type)
	}
}

func TestApplySecret_EmptyMapIsNoOp(t *testing.T) {
	c := newTestClient(t)
	if err := c.ApplySecret(context.Background(), "demo", 1, "podium-dev", nil); err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	_, err := c.CS.CoreV1().Secrets("podium-dev").Get(context.Background(),
		EnvSecretName("demo", 1), metav1.GetOptions{})
	if err == nil {
		t.Error("expected no secret to be created")
	}
}

func TestDeleteConfigMap_Idempotent(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	if err := c.ApplyConfigMap(ctx, "demo", 1, "podium-dev", map[string]string{"A": "1"}); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteConfigMap(ctx, "demo", 1, "podium-dev"); err != nil {
		t.Errorf("first delete: %v", err)
	}
	// Second delete must be a no-op (NotFound -> success).
	if err := c.DeleteConfigMap(ctx, "demo", 1, "podium-dev"); err != nil {
		t.Errorf("second delete (missing): %v", err)
	}
}

func TestDeleteSecret_Idempotent(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	if err := c.ApplySecret(ctx, "demo", 1, "podium-dev", map[string]string{"K": "v"}); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteSecret(ctx, "demo", 1, "podium-dev"); err != nil {
		t.Errorf("first delete: %v", err)
	}
	if err := c.DeleteSecret(ctx, "demo", 1, "podium-dev"); err != nil {
		t.Errorf("second delete (missing): %v", err)
	}
}
