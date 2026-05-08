package api

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/netovas-billing/freeradius-manager/pkg/types"
)

// Authenticator validates a bearer token. Implementations may use a static
// token (single-tenant deployment) or a token store (multi-tenant).
type Authenticator interface {
	Validate(token string) (subjectID string, ok bool)
}

// StaticTokenAuth is a single-token authenticator suitable for v0.1.0
// where each RADIUS VM has one token shared with the ERP.
type StaticTokenAuth struct {
	Token    string
	Subject  string
}

func (a *StaticTokenAuth) Validate(token string) (string, bool) {
	if a.Token == "" || token == "" {
		return "", false
	}
	if subtle.ConstantTimeCompare([]byte(a.Token), []byte(token)) == 1 {
		subj := a.Subject
		if subj == "" {
			subj = "default"
		}
		return subj, true
	}
	return "", false
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(header, prefix) {
			writeJSON(w, http.StatusUnauthorized, types.APIError{
				Error:   "unauthorized",
				Message: "missing or malformed Authorization header",
			})
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(header, prefix))
		subj, ok := s.auth.Validate(token)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, types.APIError{
				Error:   "unauthorized",
				Message: "invalid token",
			})
			return
		}
		ctx := context.WithValue(r.Context(), subjectKey, subj)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
