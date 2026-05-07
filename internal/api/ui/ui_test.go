package ui_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/heirro/freeradius-manager/internal/api/ui"
)

// newRouter wires the UI handler under /ui/ exactly the way the API server
// does in production, so the tests exercise the same prefix-stripping path.
func newRouter() http.Handler {
	r := chi.NewRouter()
	r.Get("/ui", ui.RedirectToTrailing())
	r.Mount("/ui/", ui.Handler())
	return r
}

func TestUI_IndexServesHTML(t *testing.T) {
	r := newRouter()
	req := httptest.NewRequest(http.MethodGet, "/ui/", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", w.Code, w.Body.String())
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Errorf("expected text/html content-type, got %q", ct)
	}
	if !strings.Contains(w.Body.String(), "<title>") {
		t.Errorf("expected body to contain <title>, body=%q", w.Body.String())
	}
}

func TestUI_AppJSServesJavascript(t *testing.T) {
	r := newRouter()
	req := httptest.NewRequest(http.MethodGet, "/ui/app.js", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "javascript") {
		t.Errorf("expected javascript content-type, got %q", ct)
	}
	if w.Body.Len() == 0 {
		t.Errorf("expected non-empty app.js body")
	}
}

func TestUI_StyleCSSServesCSS(t *testing.T) {
	r := newRouter()
	req := httptest.NewRequest(http.MethodGet, "/ui/style.css", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "css") {
		t.Errorf("expected css content-type, got %q", ct)
	}
}

func TestUI_NonexistentReturns404(t *testing.T) {
	r := newRouter()
	req := httptest.NewRequest(http.MethodGet, "/ui/does-not-exist.txt", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d (body=%s)", w.Code, w.Body.String())
	}
}

func TestUI_RedirectFromBareUI(t *testing.T) {
	r := newRouter()
	req := httptest.NewRequest(http.MethodGet, "/ui", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// 301/308 are both acceptable here; just want a redirect to /ui/.
	if w.Code < 300 || w.Code >= 400 {
		t.Fatalf("expected redirect (3xx), got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.HasSuffix(loc, "/ui/") {
		t.Errorf("expected Location to end with /ui/, got %q", loc)
	}
}
