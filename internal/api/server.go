// Package api implements the HTTP layer for radius-manager-api.
// Routes are documented in docs/SRS-RadiusManagerAPI.md §4.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	uipkg "github.com/heirro/freeradius-manager/internal/api/ui"
	"github.com/heirro/freeradius-manager/internal/manager"
	"github.com/heirro/freeradius-manager/pkg/types"
)

type Server struct {
	manager manager.Manager
	auth    Authenticator
	audit   AuditWriter
	logger  *slog.Logger
	router  http.Handler
}

func NewServer(mgr manager.Manager, auth Authenticator, logger *slog.Logger, opts ...ServerOption) *Server {
	s := &Server{
		manager: mgr,
		auth:    auth,
		logger:  logger,
		audit:   io.Discard,
	}
	for _, opt := range opts {
		opt(s)
	}
	s.router = s.buildRouter()
	return s
}

// ServerOption configures optional server behavior.
type ServerOption func(*Server)

// WithAuditWriter sets the destination for the audit log (one JSON line
// per mutating request). Defaults to io.Discard.
func WithAuditWriter(w AuditWriter) ServerOption {
	return func(s *Server) { s.audit = w }
}

func (s *Server) Handler() http.Handler { return s.router }

func (s *Server) buildRouter() http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(s.requestLogger)
	r.Use(middleware.Timeout(120 * time.Second))

	// Public health endpoint (no auth) per SRS §4.2.5.
	r.Get("/v1/server/health", s.handleHealth)

	// OpenAPI spec — public, useful for onboarding ERP devs.
	r.Get("/v1/openapi.yaml", s.handleOpenAPI)

	// Operator-facing web console (public asset routes; the API calls the
	// UI makes are still gated by the auth middleware below).
	r.Get("/ui", uipkg.RedirectToTrailing())
	r.Mount("/ui/", uipkg.Handler())

	r.Group(func(r chi.Router) {
		r.Use(s.authMiddleware)
		r.Use(s.auditMiddleware(s.audit))

		r.Route("/v1/instances", func(r chi.Router) {
			r.Get("/", s.handleListInstances)
			r.Post("/", s.handleCreateInstance)

			r.Route("/{name}", func(r chi.Router) {
				r.Get("/", s.handleGetInstance)
				r.Delete("/", s.handleDeleteInstance)
				r.Post("/start", s.handleStartInstance)
				r.Post("/stop", s.handleStopInstance)
				r.Post("/restart", s.handleRestartInstance)
				r.Post("/test", s.handleTestInstance)
			})
		})

		r.Get("/v1/server/info", s.handleServerInfo)
	})

	return r
}

func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		s.logger.LogAttrs(r.Context(), slog.LevelInfo, "http",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", ww.Status()),
			slog.Int("bytes", ww.BytesWritten()),
			slog.Duration("dur", time.Since(start)),
			slog.String("req_id", middleware.GetReqID(r.Context())),
		)
	})
}

// writeJSON marshals v as JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError converts a manager error into the wire APIError format with
// the appropriate HTTP status. See SRS §4.3.
func writeError(w http.ResponseWriter, err error, instanceName string) {
	apiErr := types.APIError{
		Error:    "internal_error",
		Message:  err.Error(),
		Instance: instanceName,
	}
	status := http.StatusInternalServerError

	switch {
	case errors.Is(err, manager.ErrInstanceNotFound):
		status = http.StatusNotFound
		apiErr.Error = "instance_not_found"
	case errors.Is(err, manager.ErrInstanceExists):
		status = http.StatusConflict
		apiErr.Error = "instance_exists"
	case errors.Is(err, manager.ErrPortExhausted):
		status = http.StatusConflict
		apiErr.Error = "port_exhausted"
	case errors.Is(err, manager.ErrInvalidName):
		status = http.StatusBadRequest
		apiErr.Error = "invalid_input"
	case errors.Is(err, manager.ErrNotImplemented):
		status = http.StatusNotImplemented
		apiErr.Error = "not_implemented"
		apiErr.Message = "this operation is not yet implemented in the v0.1.0 scaffold"
	}
	writeJSON(w, status, apiErr)
}

// withTimeout returns a request-scoped context with a sensible default if
// the chi-applied timeout has not already kicked in.
func withTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}
