package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/heirro/freeradius-manager/internal/manager"
	"github.com/heirro/freeradius-manager/pkg/types"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := withTimeout(r, 2*time.Second)
	defer cancel()
	h, err := s.manager.HealthCheck(ctx)
	if err != nil {
		writeError(w, err, "")
		return
	}
	status := http.StatusOK
	if h.Status != "healthy" {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, h)
}

func (s *Server) handleServerInfo(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := withTimeout(r, 5*time.Second)
	defer cancel()
	info, err := s.manager.ServerInfo(ctx)
	if err != nil {
		writeError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) handleListInstances(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := withTimeout(r, 10*time.Second)
	defer cancel()
	list, err := s.manager.ListInstances(ctx)
	if err != nil {
		writeError(w, err, "")
		return
	}
	if list == nil {
		list = []types.Instance{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateInstance(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, err, "")
		return
	}
	var req types.CreateInstanceRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, types.APIError{
			Error: "invalid_input", Message: "request body must be valid JSON",
		})
		return
	}
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, types.APIError{
			Error: "invalid_input", Message: "field 'name' is required",
		})
		return
	}

	ctx, cancel := withTimeout(r, 90*time.Second)
	defer cancel()
	resp, err := s.manager.CreateInstance(ctx, req)
	if err != nil {
		writeError(w, err, req.Name)
		return
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleGetInstance(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	includeSecrets := r.URL.Query().Get("include_secrets") == "true"

	ctx, cancel := withTimeout(r, 5*time.Second)
	defer cancel()
	inst, err := s.manager.GetInstance(ctx, name, includeSecrets)
	if err != nil {
		writeError(w, err, name)
		return
	}
	writeJSON(w, http.StatusOK, inst)
}

func (s *Server) handleDeleteInstance(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	withDB := r.URL.Query().Get("with_db") == "true"

	ctx, cancel := withTimeout(r, 60*time.Second)
	defer cancel()
	resp, err := s.manager.DeleteInstance(ctx, name, withDB)
	if err != nil {
		// Idempotent: not-found is a successful no-op for delete.
		if errors.Is(err, manager.ErrInstanceNotFound) {
			writeJSON(w, http.StatusOK, map[string]any{
				"name":            name,
				"already_deleted": true,
			})
			return
		}
		writeError(w, err, name)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleStartInstance(w http.ResponseWriter, r *http.Request) {
	s.simpleNameAction(w, r, s.manager.StartInstance, "started")
}

func (s *Server) handleStopInstance(w http.ResponseWriter, r *http.Request) {
	s.simpleNameAction(w, r, s.manager.StopInstance, "stopped")
}

func (s *Server) handleRestartInstance(w http.ResponseWriter, r *http.Request) {
	s.simpleNameAction(w, r, s.manager.RestartInstance, "restarted")
}

type simpleAction func(ctx context.Context, name string) error

func (s *Server) simpleNameAction(w http.ResponseWriter, r *http.Request, fn simpleAction, verb string) {
	name := chi.URLParam(r, "name")
	ctx, cancel := withTimeout(r, 30*time.Second)
	defer cancel()
	if err := fn(ctx, name); err != nil {
		writeError(w, err, name)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "result": verb})
}

func (s *Server) handleTestInstance(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	ctx, cancel := withTimeout(r, 15*time.Second)
	defer cancel()
	res, err := s.manager.TestInstance(ctx, name)
	if err != nil {
		writeError(w, err, name)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
