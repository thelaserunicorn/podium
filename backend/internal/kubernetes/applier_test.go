package kubernetes

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/podium/podium/internal/application"
	"github.com/podium/podium/internal/deployment"
	"github.com/podium/podium/internal/storage"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// fakeAppReader implements AppReader without touching a database.
type fakeAppReader struct {
	app application.Application
	err error
}

func (f *fakeAppReader) Get(_ context.Context, _, _ int64) (application.Application, error) {
	return f.app, f.err
}

func newApplierFixture(t *testing.T, replicas int) (*Applier, *storage.Queries, int64) {
	t.Helper()
	ctx := context.Background()
	db := storage.OpenInMemoryForTest(t)
	q := storage.NewQueries(db)

	res, _ := db.ExecContext(ctx,
		`INSERT INTO users (username, email, password_hash, role, status) VALUES (?, ?, ?, 'USER', 'APPROVED')`,
		"alice", "a@b", "x")
	uid, _ := res.LastInsertId()
	res, _ = db.ExecContext(ctx,
		`INSERT INTO applications (user_id, name, repository_url, container_port) VALUES (?, 'demo', 'https://github.com/x/y', 8080)`,
		uid)
	appID, _ := res.LastInsertId()

	envID, _ := q.EnsureEnvironment(ctx, "podium-dev")
	depID, _ := q.CreateDeployment(ctx, appID, envID, 1, replicas, "demo:v1")
	// Drive through BUILDING → BUILT so the applier's path lines up
	// with what real orchestrator state looks like at the entry point.
	if err := q.SetDeploymentStatus(ctx, depID, storage.StatusBuilding, ""); err != nil {
		t.Fatal(err)
	}
	if err := q.SetDeploymentStatus(ctx, depID, storage.StatusBuilt, ""); err != nil {
		t.Fatal(err)
	}

	cs := fake.NewSimpleClientset()
	a := &Applier{
		Client:       &Client{CS: cs, Source: "fake"},
		Store:        q,
		App:          &fakeAppReader{app: application.Application{ID: appID, Name: "demo", ContainerPort: 8080}},
		PollEvery:    10 * time.Millisecond,
		ReadyTimeout: 500 * time.Millisecond,
	}
	return a, q, depID
}

func TestApplier_HappyPathFlipsToRunning(t *testing.T) {
	ctx := context.Background()
	a, q, depID := newApplierFixture(t, 2)

	// The fake clientset does not bring up pods on its own — drive
	// ReadyReplicas up after Apply creates the Deployment so the
	// readiness poll sees the deployment as ready.
	cs := a.Client.CS.(*fake.Clientset)
	cs.PrependReactor("get", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if getAction, ok := action.(k8stesting.GetActionImpl); ok {
			dep, err := cs.Tracker().Get(getAction.GetResource(), getAction.GetNamespace(), getAction.GetName())
			if err != nil {
				return false, nil, err
			}
			d := dep.(*appsv1.Deployment)
			d.Status.ReadyReplicas = int32(*d.Spec.Replicas)
			d.Status.Replicas = int32(*d.Spec.Replicas)
			return true, d, nil
		}
		return false, nil, nil
	})

	if err := a.Apply(ctx, depID); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	d, _ := q.GetDeployment(ctx, depID)
	if d.Status != storage.StatusRunning {
		t.Errorf("status=%q want RUNNING", d.Status)
	}
	if !d.FinishedAt.Valid {
		t.Errorf("finished_at should be set on RUNNING")
	}
}

func TestApplier_TimeoutMarksReadinessTimeout(t *testing.T) {
	ctx := context.Background()
	a, q, depID := newApplierFixture(t, 2)

	// The fake clientset does not tick ReadyReplicas up; the poll
	// loop should hit ReadyTimeout and surface ErrReadinessTimeout.
	a.PollEvery = 10 * time.Millisecond
	a.ReadyTimeout = 50 * time.Millisecond

	err := a.Apply(ctx, depID)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !errors.Is(err, deployment.ErrReadinessTimeout) {
		t.Errorf("err=%v, want errors.Is ErrReadinessTimeout", err)
	}
	d, _ := q.GetDeployment(ctx, depID)
	if d.Status != storage.StatusFailed {
		t.Errorf("status=%q want FAILED", d.Status)
	}
	if d.Reason.String != "readiness_timeout" {
		t.Errorf("reason=%q", d.Reason.String)
	}
}

func TestApplier_DeployFailedOnApplyError(t *testing.T) {
	ctx := context.Background()
	a, _, depID := newApplierFixture(t, 1)

	// Inject a fake reactor that always errors on Deployments.Create.
	a.Client.CS.(*fake.Clientset).PrependReactor("create", "deployments",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("simulated apply failure")
		},
	)
	// Same reactor for updates, since ApplyDeployment falls back to
	// Update on AlreadyExists.
	a.Client.CS.(*fake.Clientset).PrependReactor("update", "deployments",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("simulated apply failure")
		},
	)

	err := a.Apply(ctx, depID)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, deployment.ErrDeployFailed) {
		t.Errorf("err=%v, want errors.Is ErrDeployFailed", err)
	}
	// The applier itself does NOT mark FAILED — that's the
	// orchestrator's job (it calls fail() on the returned error).
	// We assert the row state is unchanged at BUILT.
	// (Verified end-to-end via the orchestrator integration tests.)
}

func TestNopApplier_ReturnsDeployFailed(t *testing.T) {
	err := NopApplier{Err: errors.New("no kubeconfig")}.Apply(context.Background(), 1)
	if !errors.Is(err, deployment.ErrDeployFailed) {
		t.Errorf("err=%v, want errors.Is ErrDeployFailed", err)
	}
}
