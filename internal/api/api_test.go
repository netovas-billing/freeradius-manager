package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/netovas-billing/freeradius-manager/internal/api"
	"github.com/netovas-billing/freeradius-manager/internal/manager"
	"github.com/netovas-billing/freeradius-manager/pkg/types"
)

// stubManager is a configurable in-memory implementation of manager.Manager
// used to drive HTTP-layer tests without touching real systemd / DB / FS.
type stubManager struct {
	// Inject return values per call.
	createResp *types.CreateInstanceResponse
	createErr  error

	deleteResp *types.DeleteInstanceResponse
	deleteErr  error

	getInst *types.Instance
	getErr  error

	listInsts []types.Instance
	listErr   error

	startErr   error
	stopErr    error
	restartErr error

	testResult *types.TestResult
	testErr    error

	serverInfo *types.ServerInfo
	serverErr  error

	health    *types.Health
	healthErr error

	// Spy fields capture arguments from the most recent call.
	gotCreateReq        types.CreateInstanceRequest
	gotDeleteName       string
	gotDeleteWithDB     bool
	gotGetName          string
	gotGetIncludeSecret bool
	gotStartName        string
	gotStopName         string
	gotRestartName      string
	gotTestName         string
}

func (s *stubManager) CreateInstance(_ context.Context, req types.CreateInstanceRequest) (*types.CreateInstanceResponse, error) {
	s.gotCreateReq = req
	return s.createResp, s.createErr
}

func (s *stubManager) DeleteInstance(_ context.Context, name string, withDB bool) (*types.DeleteInstanceResponse, error) {
	s.gotDeleteName = name
	s.gotDeleteWithDB = withDB
	return s.deleteResp, s.deleteErr
}

func (s *stubManager) GetInstance(_ context.Context, name string, includeSecrets bool) (*types.Instance, error) {
	s.gotGetName = name
	s.gotGetIncludeSecret = includeSecrets
	return s.getInst, s.getErr
}

func (s *stubManager) ListInstances(_ context.Context) ([]types.Instance, error) {
	return s.listInsts, s.listErr
}

func (s *stubManager) StartInstance(_ context.Context, name string) error {
	s.gotStartName = name
	return s.startErr
}

func (s *stubManager) StopInstance(_ context.Context, name string) error {
	s.gotStopName = name
	return s.stopErr
}

func (s *stubManager) RestartInstance(_ context.Context, name string) error {
	s.gotRestartName = name
	return s.restartErr
}

func (s *stubManager) TestInstance(_ context.Context, name string) (*types.TestResult, error) {
	s.gotTestName = name
	return s.testResult, s.testErr
}

func (s *stubManager) ServerInfo(_ context.Context) (*types.ServerInfo, error) {
	if s.serverInfo == nil && s.serverErr == nil {
		return &types.ServerInfo{Hostname: "test-host", RMAPIVersion: "v0.1.0"}, nil
	}
	return s.serverInfo, s.serverErr
}

func (s *stubManager) HealthCheck(_ context.Context) (*types.Health, error) {
	if s.health == nil && s.healthErr == nil {
		return &types.Health{Status: "healthy", Checks: map[string]string{}}, nil
	}
	return s.health, s.healthErr
}

// newTestServer builds a Server wired with the given stub and an in-memory
// audit buffer. Returns the http.Handler, the audit buffer, and the stub for
// tests that want to inspect call args.
func newTestServer(t *testing.T, stub *stubManager) (http.Handler, *bytes.Buffer) {
	t.Helper()
	if stub == nil {
		stub = &stubManager{}
	}
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := api.NewServer(
		stub,
		&api.StaticTokenAuth{Token: "good", Subject: "default"},
		logger,
		api.WithAuditWriter(buf),
	)
	return srv.Handler(), buf
}

// do issues a request against the handler and returns the recorder.
func do(t *testing.T, h http.Handler, method, target, token string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, body)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// ---------- Public endpoint tests ---------------------------------------

func TestHealth_PublicNoAuth(t *testing.T) {
	h, _ := newTestServer(t, nil)
	w := do(t, h, http.MethodGet, "/v1/server/health", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", w.Code, w.Body.String())
	}
}

func TestOpenAPI_PublicAndYAMLContentType(t *testing.T) {
	h, _ := newTestServer(t, nil)
	w := do(t, h, http.MethodGet, "/v1/openapi.yaml", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if ct != "application/yaml" {
		t.Errorf("expected Content-Type application/yaml, got %q", ct)
	}
	if w.Body.Len() < 100 {
		t.Errorf("expected non-trivial body (>100 bytes), got %d", w.Body.Len())
	}
}

// ---------- Auth gating tests -------------------------------------------

func TestProtectedEndpoints_RequireAuth(t *testing.T) {
	h, _ := newTestServer(t, nil)
	cases := []struct {
		method, path string
	}{
		{http.MethodGet, "/v1/server/info"},
		{http.MethodGet, "/v1/instances"},
		{http.MethodGet, "/v1/instances/foo"},
		{http.MethodPost, "/v1/instances/foo/start"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			w := do(t, h, tc.method, tc.path, "", nil)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d (body=%s)", w.Code, w.Body.String())
			}
			var body types.APIError
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("bad json body: %v", err)
			}
			if body.Error != "unauthorized" {
				t.Errorf("expected error=unauthorized, got %q", body.Error)
			}
		})
	}
}

func TestProtectedEndpoints_AcceptValidToken(t *testing.T) {
	stub := &stubManager{
		listInsts: []types.Instance{},
		getInst:   &types.Instance{Name: "foo"},
	}
	h, _ := newTestServer(t, stub)
	cases := []struct {
		method, path string
	}{
		{http.MethodGet, "/v1/server/info"},
		{http.MethodGet, "/v1/instances"},
		{http.MethodGet, "/v1/instances/foo"},
		{http.MethodPost, "/v1/instances/foo/start"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			w := do(t, h, tc.method, tc.path, "good", nil)
			if w.Code == http.StatusUnauthorized {
				t.Fatalf("expected non-401, got 401 (body=%s)", w.Body.String())
			}
		})
	}
}

func TestProtectedEndpoints_RejectWrongToken(t *testing.T) {
	h, _ := newTestServer(t, nil)
	cases := []struct {
		method, path string
	}{
		{http.MethodGet, "/v1/server/info"},
		{http.MethodGet, "/v1/instances"},
		{http.MethodGet, "/v1/instances/foo"},
		{http.MethodPost, "/v1/instances/foo/start"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			w := do(t, h, tc.method, tc.path, "wrong", nil)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", w.Code)
			}
		})
	}
}

// ---------- CreateInstance tests ----------------------------------------

func TestCreate_201OnSuccess(t *testing.T) {
	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	stub := &stubManager{
		createResp: &types.CreateInstanceResponse{
			Name:      "mitra_x",
			Status:    types.StatusRunning,
			APIURL:    "http://10.0.0.1:9000",
			CreatedAt: now,
		},
	}
	h, _ := newTestServer(t, stub)

	body := strings.NewReader(`{"name":"mitra_x"}`)
	w := do(t, h, http.MethodPost, "/v1/instances", "good", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (body=%s)", w.Code, w.Body.String())
	}
	var got types.CreateInstanceResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if got.Name != "mitra_x" {
		t.Errorf("expected name=mitra_x, got %q", got.Name)
	}
	if stub.gotCreateReq.Name != "mitra_x" {
		t.Errorf("stub did not receive name=mitra_x, got %q", stub.gotCreateReq.Name)
	}
}

func TestCreate_400OnInvalidJSON(t *testing.T) {
	h, _ := newTestServer(t, nil)
	w := do(t, h, http.MethodPost, "/v1/instances", "good", strings.NewReader("{not json"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	var body types.APIError
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad json body: %v", err)
	}
	if body.Error != "invalid_input" {
		t.Errorf("expected error=invalid_input, got %q", body.Error)
	}
}

func TestCreate_400OnMissingName(t *testing.T) {
	h, _ := newTestServer(t, nil)
	w := do(t, h, http.MethodPost, "/v1/instances", "good", strings.NewReader(`{}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	var body types.APIError
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad json body: %v", err)
	}
	if body.Error != "invalid_input" {
		t.Errorf("expected error=invalid_input, got %q", body.Error)
	}
	if !strings.Contains(strings.ToLower(body.Message), "name") {
		t.Errorf("expected message to mention 'name', got %q", body.Message)
	}
}

func TestCreate_409OnInstanceExists(t *testing.T) {
	stub := &stubManager{createErr: manager.ErrInstanceExists}
	h, _ := newTestServer(t, stub)
	w := do(t, h, http.MethodPost, "/v1/instances", "good", strings.NewReader(`{"name":"foo"}`))
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
	var body types.APIError
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad json body: %v", err)
	}
	if body.Error != "instance_exists" {
		t.Errorf("expected error=instance_exists, got %q", body.Error)
	}
}

func TestCreate_400OnInvalidName(t *testing.T) {
	stub := &stubManager{createErr: manager.ErrInvalidName}
	h, _ := newTestServer(t, stub)
	w := do(t, h, http.MethodPost, "/v1/instances", "good", strings.NewReader(`{"name":"BAD-NAME"}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	var body types.APIError
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad json body: %v", err)
	}
	if body.Error != "invalid_input" {
		t.Errorf("expected error=invalid_input, got %q", body.Error)
	}
}

func TestCreate_501OnNotImplemented(t *testing.T) {
	stub := &stubManager{createErr: manager.ErrNotImplemented}
	h, _ := newTestServer(t, stub)
	w := do(t, h, http.MethodPost, "/v1/instances", "good", strings.NewReader(`{"name":"foo"}`))
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", w.Code)
	}
	var body types.APIError
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad json body: %v", err)
	}
	if body.Error != "not_implemented" {
		t.Errorf("expected error=not_implemented, got %q", body.Error)
	}
}

// ---------- GetInstance tests -------------------------------------------

func TestGet_404OnNotFound(t *testing.T) {
	stub := &stubManager{getErr: manager.ErrInstanceNotFound}
	h, _ := newTestServer(t, stub)
	w := do(t, h, http.MethodGet, "/v1/instances/missing", "good", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
	var body types.APIError
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad json body: %v", err)
	}
	if body.Error != "instance_not_found" {
		t.Errorf("expected error=instance_not_found, got %q", body.Error)
	}
}

func TestGet_IncludeSecretsQueryParam(t *testing.T) {
	stub := &stubManager{getInst: &types.Instance{Name: "foo"}}
	h, _ := newTestServer(t, stub)
	w := do(t, h, http.MethodGet, "/v1/instances/foo?include_secrets=true", "good", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !stub.gotGetIncludeSecret {
		t.Errorf("stub did not receive includeSecrets=true")
	}
	if stub.gotGetName != "foo" {
		t.Errorf("expected stub to be called with name=foo, got %q", stub.gotGetName)
	}
}

// ---------- DeleteInstance tests ----------------------------------------

func TestDelete_IdempotentNotFound(t *testing.T) {
	stub := &stubManager{deleteErr: manager.ErrInstanceNotFound}
	h, _ := newTestServer(t, stub)
	w := do(t, h, http.MethodDelete, "/v1/instances/foo", "good", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (idempotent), got %d (body=%s)", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if v, ok := body["already_deleted"].(bool); !ok || !v {
		t.Errorf("expected already_deleted=true, got %v", body["already_deleted"])
	}
}

func TestDelete_WithDBQueryParam(t *testing.T) {
	stub := &stubManager{
		deleteResp: &types.DeleteInstanceResponse{
			Name:            "foo",
			DeletedAt:       time.Now(),
			DatabaseDropped: true,
		},
	}
	h, _ := newTestServer(t, stub)
	w := do(t, h, http.MethodDelete, "/v1/instances/foo?with_db=true", "good", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !stub.gotDeleteWithDB {
		t.Errorf("stub did not receive withDB=true")
	}
	if stub.gotDeleteName != "foo" {
		t.Errorf("expected stub name=foo, got %q", stub.gotDeleteName)
	}
}

// ---------- Lifecycle action tests --------------------------------------

func TestStartStopRestart_DelegateToManager(t *testing.T) {
	stub := &stubManager{}
	h, _ := newTestServer(t, stub)

	if w := do(t, h, http.MethodPost, "/v1/instances/foo/start", "good", nil); w.Code != http.StatusOK {
		t.Fatalf("start: expected 200, got %d (body=%s)", w.Code, w.Body.String())
	}
	if stub.gotStartName != "foo" {
		t.Errorf("StartInstance: expected name=foo, got %q", stub.gotStartName)
	}

	if w := do(t, h, http.MethodPost, "/v1/instances/bar/stop", "good", nil); w.Code != http.StatusOK {
		t.Fatalf("stop: expected 200, got %d", w.Code)
	}
	if stub.gotStopName != "bar" {
		t.Errorf("StopInstance: expected name=bar, got %q", stub.gotStopName)
	}

	if w := do(t, h, http.MethodPost, "/v1/instances/baz/restart", "good", nil); w.Code != http.StatusOK {
		t.Fatalf("restart: expected 200, got %d", w.Code)
	}
	if stub.gotRestartName != "baz" {
		t.Errorf("RestartInstance: expected name=baz, got %q", stub.gotRestartName)
	}
}

// ---------- Audit middleware tests --------------------------------------

func TestAuditMiddleware_WritesOneLinePerMutation(t *testing.T) {
	stub := &stubManager{}
	h, buf := newTestServer(t, stub)

	// One POST (mutating) + one GET (read).
	_ = do(t, h, http.MethodPost, "/v1/instances/foo/start", "good", nil)
	_ = do(t, h, http.MethodGet, "/v1/instances", "good", nil)

	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 audit line, got %d (raw=%q)", len(lines), out)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("audit line is not valid JSON: %v (line=%q)", err, lines[0])
	}
	if rec["method"] != "POST" {
		t.Errorf("expected method=POST in audit line, got %v", rec["method"])
	}
}

func TestAuditMiddleware_SkipsReadMethods(t *testing.T) {
	stub := &stubManager{listInsts: []types.Instance{}}
	h, buf := newTestServer(t, stub)

	_ = do(t, h, http.MethodGet, "/v1/instances", "good", nil)
	_ = do(t, h, http.MethodGet, "/v1/instances/foo", "good", nil)
	_ = do(t, h, http.MethodGet, "/v1/server/info", "good", nil)

	if buf.Len() != 0 {
		t.Fatalf("expected audit buffer to be empty for GETs, got %q", buf.String())
	}
}

func TestAuditMiddleware_RecordsSubject(t *testing.T) {
	stub := &stubManager{}
	h, buf := newTestServer(t, stub)

	_ = do(t, h, http.MethodPost, "/v1/instances/foo/start", "good", nil)

	out := strings.TrimRight(buf.String(), "\n")
	if out == "" {
		t.Fatalf("expected an audit line, got empty buffer")
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(out), &rec); err != nil {
		t.Fatalf("invalid audit JSON: %v (line=%q)", err, out)
	}
	if rec["subject"] != "default" {
		t.Errorf("expected subject=default, got %v", rec["subject"])
	}
}
