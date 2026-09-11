package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/podium/podium/internal/application"
	"github.com/podium/podium/internal/auth"
)

// fakeEnvStore implements application.EnvStore for tests that don't
// need a real kubernetes.Client. It records calls so handlers can be
// asserted on.
type fakeEnvStore struct {
	mu         sync.Mutex
	configMaps []map[string]string
	secrets    []map[string]string
	returnErr  error
}

func (f *fakeEnvStore) ApplyConfigMap(_ context.Context, _ string, _ int64, _ string, data map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.returnErr != nil {
		return f.returnErr
	}
	clone := make(map[string]string, len(data))
	for k, v := range data {
		clone[k] = v
	}
	f.configMaps = append(f.configMaps, clone)
	return nil
}

func (f *fakeEnvStore) ApplySecret(_ context.Context, _ string, _ int64, _ string, data map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.returnErr != nil {
		return f.returnErr
	}
	clone := make(map[string]string, len(data))
	for k, v := range data {
		clone[k] = v
	}
	f.secrets = append(f.secrets, clone)
	return nil
}

func (f *fakeEnvStore) DeleteConfigMap(context.Context, string, int64, string) error { return nil }
func (f *fakeEnvStore) DeleteSecret(context.Context, string, int64, string) error    { return nil }

// envFixture mirrors newFixture but mounts the EnvHandler with a
// fakeEnvStore so test cases can exercise the API + storage + k8s
// recording layers.
func envFixture(t *testing.T) (*fixture, *fakeEnvStore, http.Handler) {
	t.Helper()
	f := newFixture(t)
	store := &fakeEnvStore{}
	envSvc := application.NewEnvService(f.store.DB(), store)
	h := NewEnvHandler(f.store, application.NewService(f.store.DB()), envSvc, nil)
	fresh := http.NewServeMux()
	MountAuth(fresh, auth.NewHandler(auth.NewService(f.store.DB())))
	MountApplications(fresh, application.NewHandler(application.NewService(f.store.DB())))
	NewDeploymentHandler(f.store, application.NewService(f.store.DB()), f.orch, nil).Mount(fresh)
	h.Mount(fresh)
	wrapped := New(fresh, Deps{Auth: auth.NewService(f.store.DB())})
	return f, store, wrapped
}

func TestEnv_HappyPathSetsListsDeletes(t *testing.T) {
	f, _, mux := envFixture(t)

	// Set non-secret.
	body := bytes.NewBufferString(`{"namespace":"podium-dev","key":"NODE_ENV","value":"production","is_secret":false}`)
	req := httptest.NewRequest("POST", "/api/applications/1/env", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("set non-secret: %d %s", rec.Code, rec.Body.String())
	}

	// Set secret.
	body = bytes.NewBufferString(`{"namespace":"podium-dev","key":"DB_PASS","value":"hunter2","is_secret":true}`)
	req = httptest.NewRequest("POST", "/api/applications/1/env", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", f.cookie)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("set secret: %d %s", rec.Code, rec.Body.String())
	}

	// List — secret value must be redacted.
	req = httptest.NewRequest("GET", "/api/applications/1/env?namespace=podium-dev", nil)
	req.Header.Set("Cookie", f.cookie)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	var listResp struct {
		EnvVars []struct {
			Key      string `json:"key"`
			Value    string `json:"value"`
			IsSecret bool   `json:"is_secret"`
		} `json:"env_vars"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&listResp)
	if len(listResp.EnvVars) != 2 {
		t.Fatalf("env_vars=%d want 2", len(listResp.EnvVars))
	}
	for _, v := range listResp.EnvVars {
		if v.IsSecret && v.Value != "" {
			t.Errorf("Redacted(%s).Value=%q want empty", v.Key, v.Value)
		}
	}

	// Delete one.
	req = httptest.NewRequest("DELETE", "/api/applications/1/env/NODE_ENV?namespace=podium-dev", nil)
	req.Header.Set("Cookie", f.cookie)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}

	// List again — only the secret remains.
	req = httptest.NewRequest("GET", "/api/applications/1/env?namespace=podium-dev", nil)
	req.Header.Set("Cookie", f.cookie)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	_ = json.NewDecoder(rec.Body).Decode(&listResp)
	if len(listResp.EnvVars) != 1 || listResp.EnvVars[0].Key != "DB_PASS" {
		t.Errorf("after delete: %+v", listResp.EnvVars)
	}
}

func TestEnv_RejectsBadKey(t *testing.T) {
	f, _, mux := envFixture(t)
	body := bytes.NewBufferString(`{"namespace":"podium-dev","key":"BAD-KEY","value":"v","is_secret":false}`)
	req := httptest.NewRequest("POST", "/api/applications/1/env", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d want 400", rec.Code)
	}
}

func TestEnv_RejectsBadNamespace(t *testing.T) {
	f, _, mux := envFixture(t)
	body := bytes.NewBufferString(`{"namespace":"Bad Name","key":"FOO","value":"v","is_secret":false}`)
	req := httptest.NewRequest("POST", "/api/applications/1/env", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d want 400", rec.Code)
	}
}

func TestEnv_RequiresAuth(t *testing.T) {
	_, _, mux := envFixture(t)
	req := httptest.NewRequest("GET", "/api/applications/1/env", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("got %d want 401", rec.Code)
	}
}

func TestEnv_DeleteMissingKeyReturns404(t *testing.T) {
	f, _, mux := envFixture(t)
	req := httptest.NewRequest("DELETE", "/api/applications/1/env/MISSING?namespace=podium-dev", nil)
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d want 404", rec.Code)
	}
}

func TestEnv_K8sErrorReturns502(t *testing.T) {
	f, store, mux := envFixture(t)
	store.returnErr = errFake
	body := bytes.NewBufferString(`{"namespace":"podium-dev","key":"FOO","value":"v","is_secret":false}`)
	req := httptest.NewRequest("POST", "/api/applications/1/env", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", f.cookie)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("got %d want 502", rec.Code)
	}
}

// errFake is a sentinel error used by fakeEnvStore.returnErr; using
// a package-level var keeps the fakeEnvStore simple.
var errFake = &fakeError{}

type fakeError struct{}

func (e *fakeError) Error() string { return "fake k8s error" }
