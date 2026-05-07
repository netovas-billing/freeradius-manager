package api

import (
	_ "embed"
	"net/http"
)

//go:embed openapi/openapi.yaml
var openAPISpec []byte

func (s *Server) handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(openAPISpec)
}
