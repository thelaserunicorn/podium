package kubernetes

import (
	"context"
	"encoding/base64"
	"testing"

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

func TestApplySecret_Base64EncodesValues(t *testing.T) {
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
	decoded, err := base64.StdEncoding.DecodeString(string(sec.Data["DB_PASSWORD"]))
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	if string(decoded) != "hunter2" {
		t.Errorf("decoded=%q want hunter2", string(decoded))
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
