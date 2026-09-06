// Package main is the entry point for the Podium control plane.
//
// See spec.md §1 (overview), §32 (backend structure), §39 (Dockerization),
// and AGENTS.md §3–§4 (technology rules, architecture).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/podium/podium/internal/api"
	"github.com/podium/podium/internal/application"
	"github.com/podium/podium/internal/auth"
	"github.com/podium/podium/internal/deployment"
	"github.com/podium/podium/internal/docker"
	"github.com/podium/podium/internal/kubernetes"
	"github.com/podium/podium/internal/storage"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "podium: fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	// Config — minimal for M1; richer env-var wiring lands in M7.
	addr := getenv("PODIUM_ADDR", ":8080")
	dbPath := getenv("PODIUM_DB_PATH", filepath.Join("data", "podium.db"))
	adminUser := getenv("PODIUM_ADMIN_USERNAME", "admin")
	adminPass := os.Getenv("PODIUM_ADMIN_PASSWORD")
	sourceRoot := getenv("PODIUM_SOURCE_ROOT", filepath.Join(filepath.Dir(dbPath), "sources"))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Storage + auth.
	db, err := storage.Open(ctx, dbPath, storage.Options{})
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer db.Close()

	if err := auth.SeedAdmin(ctx, db, adminUser, adminPass); err != nil {
		return fmt.Errorf("seed admin: %w", err)
	}

	authSvc := auth.NewService(db)
	appSvc := application.NewService(db)

	// Deployment pipeline: docker client (real) + orchestrator. The
	// real K8sApplier lands in M3; in M2 the orchestrator stops at
	// BUILT. Failures here are non-fatal so the dashboard still
	// serves even without a kind cluster.
	builder, builderErr := docker.NewClient(nil, nil)
	if builderErr != nil {
		logger.Warn("docker client unavailable; deploy endpoints will surface build errors", "err", builderErr)
	}
	var deployBuilder docker.Builder = docker.NopBuilder{Err: builderErr}
	var fetcher docker.SourceFetcher = docker.NopFetcher{Err: builderErr}
	if builderErr == nil {
		deployBuilder = builder
		fetcher = docker.NewGitSourceFetcher()
	}
	queries := storage.NewQueries(db)

	// K8s applier: try to connect to a cluster (kind on the host).
	// Failures are non-fatal so the dashboard still serves; deploys
	// after BUILT then mark FAILED with "deploy_failed: <kubeconfig
	// error>" (mirrors the docker NopBuilder fallback in M2).
	var k8sApplier deployment.K8sApplier
	k8sClient, k8sErr := kubernetes.New()
	if k8sErr != nil {
		logger.Warn("kubernetes client unavailable; deploy endpoints will stop at BUILT", "err", k8sErr)
		k8sApplier = kubernetes.NopApplier{Err: k8sErr}
	} else {
		logger.Info("kubernetes client connected", "source", k8sClient.Source)
		k8sApplier = &kubernetes.Applier{
			Client: k8sClient,
			Store:  queries,
			App:    appSvc,
			Loader: deployBuilder,
		}
	}

	orch := deployment.NewOrchestrator(
		queries, deployBuilder, fetcher, sourceRoot,
		deployment.Timeouts{Build: 15 * time.Minute},
		k8sApplier,
	)

	// HTTP wiring.
	mux := http.NewServeMux()
	api.MountAuth(mux, auth.NewHandler(authSvc))
	api.MountApplications(mux, application.NewHandler(appSvc))
	api.NewDeploymentHandler(queries, appSvc, orch, logger).Mount(mux)
	api.NewAdminHandler(authSvc).Mount(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	handler := api.New(mux, api.Deps{Auth: authSvc, Logger: logger})

	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// WriteTimeout stays 0 (unbounded) so future SSE / log streaming in
		// M6 doesn't get cut off.
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("podium listening", slog.String("addr", addr), slog.String("db", dbPath))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("podium shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

func getenv(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
