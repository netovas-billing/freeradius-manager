package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// AuditWriter is the destination for one-line audit records (JSON).
// Production typically uses a file via os.OpenFile with append mode.
type AuditWriter interface {
	io.Writer
}

type auditRecord struct {
	Time      time.Time `json:"time"`
	ReqID     string    `json:"req_id"`
	Subject   string    `json:"subject,omitempty"`
	Method    string    `json:"method"`
	Path      string    `json:"path"`
	Instance  string    `json:"instance,omitempty"`
	Status    int       `json:"status"`
	DurMS     int64     `json:"dur_ms"`
	RemoteIP  string    `json:"remote_ip,omitempty"`
}

// auditMiddleware emits one JSON line per mutating request. Read-only
// requests are skipped to keep the audit log focused on state changes.
func (s *Server) auditMiddleware(w AuditWriter) func(http.Handler) http.Handler {
	logger := s.logger
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			if !isMutating(r.Method) {
				next.ServeHTTP(rw, r)
				return
			}
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(rw, r.ProtoMajor)
			next.ServeHTTP(ww, r)

			rec := auditRecord{
				Time:     start.UTC(),
				ReqID:    middleware.GetReqID(r.Context()),
				Method:   r.Method,
				Path:     r.URL.Path,
				Instance: chi.URLParam(r, "name"),
				Status:   ww.Status(),
				DurMS:    time.Since(start).Milliseconds(),
				RemoteIP: r.RemoteAddr,
				Subject:  subjectFromContext(r.Context()),
			}
			if err := writeAuditLine(w, rec); err != nil {
				logger.LogAttrs(r.Context(), slog.LevelWarn, "audit_write_failed",
					slog.Any("err", err))
			}
		})
	}
}

func isMutating(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

func writeAuditLine(w AuditWriter, rec auditRecord) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

// subjectKey is a context key for the authenticated subject id, set by
// authMiddleware on success.
type subjectKeyType struct{}

var subjectKey = subjectKeyType{}

func subjectFromContext(ctx context.Context) string {
	v := ctx.Value(subjectKey)
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
