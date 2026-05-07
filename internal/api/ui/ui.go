// Package ui serves the embedded operator-facing web console for
// radius-manager-api. The UI is a single-page app (plain HTML/JS/CSS,
// no build pipeline) compiled into the Go binary via //go:embed.
//
// The console talks to the same RM-API instance it is served from,
// using a Bearer token that the operator pastes once and the browser
// stores in localStorage. See docs/PRD-RadiusManagerAPI.md.
package ui

import (
	"embed"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/go-chi/chi/v5"
)

//go:embed static
var staticFS embed.FS

// fsRoot returns the embedded static/ directory rooted at "/", so callers
// can treat "/index.html" the same way they would on a real filesystem.
func fsRoot() fs.FS {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		// fs.Sub on a //go:embed root only fails on misconfiguration; this
		// would be a build-time error caught immediately in tests.
		panic("ui: static sub fs: " + err.Error())
	}
	return sub
}

// Handler returns an http.Handler that serves the embedded UI assets.
// It is meant to be mounted under /ui/ — the prefix is stripped before
// the file lookup so /ui/app.js maps to static/app.js.
//
// Unknown paths return 404. SPA fallback is intentionally NOT enabled
// because the console is a single page; there are no client-side routes.
func Handler() http.Handler {
	root := fsRoot()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// chi.Mount keeps the full URL.Path on the request but exposes the
		// post-mount tail via RouteContext.RoutePath. Prefer that when it
		// is set so we work whether mounted under /ui/ or any other prefix.
		upath := r.URL.Path
		if rctx := chi.RouteContext(r.Context()); rctx != nil && rctx.RoutePath != "" {
			upath = rctx.RoutePath
		}
		upath = strings.TrimPrefix(upath, "/")
		if upath == "" {
			upath = "index.html"
		}

		// Defensive normalization. embed.FS already rejects "..", but
		// normalize first for predictable 404s.
		clean := path.Clean("/" + upath)[1:]
		if clean == "" || clean == "." {
			clean = "index.html"
		}

		f, err := root.Open(clean)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()

		stat, err := f.Stat()
		if err != nil || stat.IsDir() {
			http.NotFound(w, r)
			return
		}

		setContentType(w, clean)
		// Modest cache: assets are versioned by binary release, not by
		// hash, so we keep this short to avoid stale UIs after upgrades.
		w.Header().Set("Cache-Control", "public, max-age=60")
		http.ServeContent(w, r, clean, stat.ModTime(), readSeeker(root, clean))
	})
}

// RedirectToTrailing returns an http.Handler that 308-redirects /ui to /ui/.
// Mounting under /ui/ alone makes chi serve /ui as 404; this gives operators
// the friendlier behavior of typing /ui and getting the dashboard.
func RedirectToTrailing() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		target := r.URL.Path + "/"
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusPermanentRedirect)
	}
}

// readSeeker re-opens the file as an io.ReadSeeker for http.ServeContent.
// embed.FS files implement io.Seeker, but fs.File does not advertise it on
// the interface, so we re-open through the underlying FS via fs.ReadFile +
// bytes.Reader-like wrapping isn't needed: embed.FS's *openFile already
// implements Seeker. We type-assert and fall back to a buffer reader.
func readSeeker(root fs.FS, name string) interface {
	Read(p []byte) (int, error)
	Seek(offset int64, whence int) (int64, error)
} {
	f, err := root.Open(name)
	if err != nil {
		// Should be impossible — caller already opened it. Return a
		// nil-friendly empty reader so ServeContent yields 200/empty
		// rather than panicking.
		return emptyReadSeeker{}
	}
	if rs, ok := f.(interface {
		Read(p []byte) (int, error)
		Seek(offset int64, whence int) (int64, error)
	}); ok {
		return rs
	}
	// Fallback: read into memory. UI assets are small so this is fine.
	data, _ := fs.ReadFile(root, name)
	return &memReadSeeker{data: data}
}

// setContentType picks a content type from the file extension. We do this
// explicitly rather than relying on mime.TypeByExtension because the host
// system's mime database can be inconsistent (macOS vs Alpine).
func setContentType(w http.ResponseWriter, name string) {
	switch {
	case strings.HasSuffix(name, ".html"):
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	case strings.HasSuffix(name, ".js"):
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	case strings.HasSuffix(name, ".css"):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case strings.HasSuffix(name, ".svg"):
		w.Header().Set("Content-Type", "image/svg+xml")
	case strings.HasSuffix(name, ".json"):
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	case strings.HasSuffix(name, ".ico"):
		w.Header().Set("Content-Type", "image/x-icon")
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
	}
}

// memReadSeeker is a minimal in-memory read+seek for the fallback path.
type memReadSeeker struct {
	data []byte
	off  int64
}

func (m *memReadSeeker) Read(p []byte) (int, error) {
	if m.off >= int64(len(m.data)) {
		return 0, io.EOF
	}
	n := copy(p, m.data[m.off:])
	m.off += int64(n)
	return n, nil
}

func (m *memReadSeeker) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = m.off + offset
	case io.SeekEnd:
		abs = int64(len(m.data)) + offset
	}
	if abs < 0 {
		return 0, errors.New("ui: invalid seek")
	}
	m.off = abs
	return abs, nil
}

type emptyReadSeeker struct{}

func (emptyReadSeeker) Read(_ []byte) (int, error)         { return 0, io.EOF }
func (emptyReadSeeker) Seek(_ int64, _ int) (int64, error) { return 0, nil }
