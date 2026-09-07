package kubernetes

import (
	"context"
	"testing"

	"github.com/podium/podium/internal/application"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func TestScaleDeployment_UpdatesReplicaCount(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	app := &application.Application{ID: 1, Name: "demo", ContainerPort: 8080}
	name, err := c.ApplyDeployment(ctx, app, "podium-dev", "demo:v1", 2)
	if err != nil {
		t.Fatalf("ApplyDeployment: %v", err)
	}

	if err := c.ScaleDeployment(ctx, "podium-dev", name, 5); err != nil {
		t.Fatalf("ScaleDeployment: %v", err)
	}
	got, err := c.CS.AppsV1().Deployments("podium-dev").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.Replicas == nil || *got.Spec.Replicas != 5 {
		t.Errorf("replicas=%v want 5", got.Spec.Replicas)
	}

	// Scale back down — confirms the Update path is idempotent.
	if err := c.ScaleDeployment(ctx, "podium-dev", name, 1); err != nil {
		t.Fatalf("ScaleDeployment down: %v", err)
	}
	got, _ = c.CS.AppsV1().Deployments("podium-dev").Get(ctx, name, metav1.GetOptions{})
	if *got.Spec.Replicas != 1 {
		t.Errorf("replicas=%v want 1", got.Spec.Replicas)
	}
}

func TestScaleDeployment_RejectsBadReplicas(t *testing.T) {
	c := newTestClient(t)
	if err := c.ScaleDeployment(context.Background(), "podium-dev", "anything", 0); err == nil {
		t.Error("expected error for replicas=0")
	}
}

func TestScaleDeployment_UnknownDeploymentErrors(t *testing.T) {
	c := newTestClient(t)
	err := c.ScaleDeployment(context.Background(), "podium-dev", "missing", 2)
	if err == nil {
		t.Fatal("expected error for unknown deployment")
	}
}

func TestDeletePodsBySelector_DeletesMatchingPods(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewSimpleClientset()
	c := &Client{CS: cs, Source: "fake"}

	// Two pods matching the selector, one unrelated pod that must
	// survive.
	pod := func(name string, labels map[string]string) runtime.Object {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "podium-dev", Labels: labels},
		}
	}
	for _, o := range []runtime.Object{
		pod("p1", map[string]string{"app": "demo"}),
		pod("p2", map[string]string{"app": "demo"}),
		pod("other", map[string]string{"app": "unrelated"}),
	} {
		if _, err := cs.CoreV1().Pods("podium-dev").Create(ctx, o.(runtime.Object).(*corev1.Pod), metav1.CreateOptions{}); err != nil {
			t.Fatalf("seed pod: %v", err)
		}
	}

	n, err := c.DeletePodsBySelector(ctx, "podium-dev", "app=demo")
	if err != nil {
		t.Fatalf("DeletePodsBySelector: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted=%d want 2", n)
	}

	// The unrelated pod survives.
	list, _ := cs.CoreV1().Pods("podium-dev").List(ctx, metav1.ListOptions{})
	if len(list.Items) != 1 || list.Items[0].Name != "other" {
		t.Errorf("unexpected pods after delete: %+v", list.Items)
	}
}

func TestDeletePodsBySelector_NoMatchesReturnsZero(t *testing.T) {
	c := newTestClient(t)
	n, err := c.DeletePodsBySelector(context.Background(), "podium-dev", "app=missing")
	if err != nil {
		t.Fatalf("DeletePodsBySelector: %v", err)
	}
	if n != 0 {
		t.Errorf("deleted=%d want 0", n)
	}
}
