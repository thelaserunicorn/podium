package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/podium/podium/internal/application"
	"github.com/podium/podium/internal/auth"
	"github.com/podium/podium/internal/deployment"
	"github.com/podium/podium/internal/storage"
	"github.com/podium/podium/internal/templates"
)

// TemplatesHandler owns the dashboard's "Templates" tab:
//
//	GET  /api/templates              — list the catalog (RequireAuth)
//	POST /api/templates/{id}/use     — create an application from a template (RequireAuth)
//
// "Use" creates the application through application.Service.Create
// and optionally queues a first deploy via Orchestrator.Queue — the
// same chokepoint used by the manual /deploy endpoint, so the
// 409-on-active-deploy contract (DECISIONS B) can only change in one
// place.
type TemplatesHandler struct {
	apps   *application.Service
	store  *storage.Queries
	orch   *deployment.Orchestrator
	logger *slog.Logger
}

func NewTemplatesHandler(apps *application.Service, store *storage.Queries, orch *deployment.Orchestrator, logger *slog.Logger) *TemplatesHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &TemplatesHandler{apps: apps, store: store, orch: orch, logger: logger}
}

func (h *TemplatesHandler) Mount(mux *http.ServeMux) {
	mux.Handle("GET /api/templates", RequireAuth(http.HandlerFunc(h.list)))
	mux.Handle("POST /api/templates/{id}/use", RequireAuth(http.HandlerFunc(h.use)))
}

type listResponse struct {
	Templates []templates.Template `json:"templates"`
}

func (h *TemplatesHandler) list(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, listResponse{Templates: templates.All()})
}

// useRequest is the JSON body of POST /api/templates/{id}/use.
// Empty Name → the template's lowercase language tag. Empty Namespace →
// "podium-dev". Replicas defaults to 3 when nil.
type useRequest struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Deploy    bool   `json:"deploy"`
	Replicas  *int   `json:"replicas,omitempty"`
}

// useResponse is returned by POST /api/templates/{id}/use. Deployment
// is omitted unless the client asked for an immediate deploy and the
// orchestrator successfully queued one.
type useResponse struct {
	Application *application.ApplicationDTO `json:"application"`
	Deployment  *storage.Deployment         `json:"deployment,omitempty"`
}

func (h *TemplatesHandler) use(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	tpl, ok := templates.Find(r.PathValue("id"))
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "unknown_template")
		return
	}

	var req useRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid json")
			return
		}
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		// application.Service.Create still runs ValidateName; this
		// default is just a convenience for the common case.
		name = strings.ToLower(tpl.Language)
	}

	namespace := req.Namespace
	if namespace == "" {
		namespace = "podium-dev"
	}

	replicas := 3
	if req.Replicas != nil {
		replicas = *req.Replicas
	}

	app, err := h.apps.Create(r.Context(), application.CreateInput{
		Name:          name,
		RepositoryURL: tpl.RepositoryURL,
		ContainerPort: tpl.ContainerPort,
		UserID:        u.ID,
	})
	if err != nil {
		switch {
		case errors.Is(err, application.ErrDuplicateName):
			writeJSONError(w, http.StatusConflict, "duplicate_name")
		case errors.Is(err, application.ErrInvalidName),
			errors.Is(err, application.ErrInvalidRepoURL),
			errors.Is(err, application.ErrInvalidPort):
			writeJSONError(w, http.StatusBadRequest, "invalid_template_input")
		default:
			h.logger.Error("create app from template", "err", err, "template_id", tpl.ID)
			writeJSONError(w, http.StatusInternalServerError, "internal_error")
		}
		return
	}

	dto := application.ApplicationDTO{
		ID:            app.ID,
		UserID:        app.UserID,
		Name:          app.Name,
		RepositoryURL: app.RepositoryURL,
		ContainerPort: app.ContainerPort,
		Version:       app.Version,
		CreatedAt:     app.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
		UpdatedAt:     app.UpdatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
	}
	resp := useResponse{Application: &dto}

	if req.Deploy && h.orch != nil {
		d, derr := h.orch.Queue(r.Context(), deployment.QueueInput{
			App:       &app,
			Namespace: namespace,
			Replicas:  replicas,
		})
		switch {
		case derr == nil:
			resp.Deployment = d
		case errors.Is(derr, application.ErrInvalidNamespace),
			errors.Is(derr, application.ErrInvalidReplicas):
			// App was created; validation only fails on the deploy side.
			// Return the application so the user can fix and retry.
			resp.Deployment = nil
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"application": resp.Application,
				"error":       derr.Error(),
			})
			return
		case errors.Is(derr, deployment.ErrActiveDeployment):
			writeJSON(w, http.StatusConflict, map[string]any{
				"application": resp.Application,
				"error":       "deployment_already_in_progress",
			})
			return
		default:
			h.logger.Error("queue deploy from template", "err", derr, "app_id", app.ID)
			// Surface the application that succeeded; the deploy
			// failure is logged but doesn't fail the request.
			resp.Deployment = nil
		}
	}

	writeJSON(w, http.StatusCreated, resp)
}
