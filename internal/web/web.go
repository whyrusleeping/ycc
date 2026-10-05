// Package web owns the daemon's embedded desktop web client: a TypeScript +
// React single-page app whose source lives in clients/web and whose production
// bundle (index.html, fingerprinted assets/*, manifest.json) is built into dist,
// committed, and compiled into the binary via go:embed. It is served from the
// daemon's mux behind the `--web` flag; building or testing the Go code never
// needs Node (see CONTRIBUTING.md for rebuilding the bundle).
//
// The assets are served unauthenticated by design (docs/design/web-client.md):
// they are public application code that carry no secrets. The RPC surface the
// client talks to stays behind the bearer AuthInterceptor unchanged — the user
// enters the token in the page, which presents it on every Connect call. The
// daemon's non-loopback no-token guardrail (internal/daemon/serve.go) is also
// unaffected: enabling --web never relaxes it.
//
// Routing: files under /assets/ are content-fingerprinted and served as
// immutable; a missing asset is a 404. Every other GET serves the matching
// root file or, for client-side routes such as /p/<project>/s/<session>, falls
// back to index.html, which is never cached so a daemon upgrade takes effect
// on reload. The Connect handler keeps its own path prefix on the daemon mux.
package web

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed dist
var assets embed.FS

const (
	cacheImmutable = "public, max-age=31536000, immutable"
	cacheNone      = "no-cache"
	// The bundle loads only same-origin scripts, styles, and Connect calls;
	// no event or tool text is ever inserted as HTML, and this policy keeps a
	// regression from executing injected markup.
	contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; " +
		"connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"
)

// Assets returns the embedded bundle rooted at dist (index.html, assets/...).
func Assets() fs.FS {
	sub, err := fs.Sub(assets, "dist")
	if err != nil {
		// The dist directory is embedded at compile time, so this can only fail
		// if the embed is broken — a programming error, not a runtime condition.
		panic("web: embedded dist FS unavailable: " + err.Error())
	}
	return sub
}

// Handler returns an http.Handler that serves the embedded client. It is
// intended to be mounted at "/" on the daemon mux; http.ServeMux
// longest-prefix routing keeps the Connect handler on its own path prefix.
func Handler() http.Handler {
	sub := Assets()
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		panic("web: embedded index.html unavailable: " + err.Error())
	}
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if strings.HasPrefix(name, "assets/") {
			if !isFile(sub, name) {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", cacheImmutable)
			files.ServeHTTP(w, r)
			return
		}
		if name != "" && name != "index.html" && isFile(sub, name) {
			w.Header().Set("Cache-Control", cacheNone)
			files.ServeHTTP(w, r)
			return
		}
		// "/", "/index.html", and every client-side route.
		w.Header().Set("Cache-Control", cacheNone)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		w.Header().Set("Referrer-Policy", "no-referrer")
		http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
	})
}

func isFile(fsys fs.FS, name string) bool {
	info, err := fs.Stat(fsys, name)
	return err == nil && !info.IsDir()
}
