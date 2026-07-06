// Package web owns the daemon's embedded web client: a dependency-free static
// single-page app (index.html + app.js + app.css) compiled into the binary via
// go:embed and served from the daemon's existing mux behind the `--web` flag
// (design docs/design/web-client.md §3).
//
// The assets are served UNauthenticated by design (web-client.md §4): they are
// public application code that carry no secrets, so gating them behind the
// bearer token would only complicate the initial page load. The RPC surface the
// client talks to stays behind the bearer AuthInterceptor unchanged — the token
// is handed to the SPA at runtime and presented on every Connect call. The
// daemon's non-loopback no-token guardrail (internal/daemon/serve.go) is also
// unaffected: enabling --web never relaxes it.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed dist
var assets embed.FS

// Handler returns an http.Handler that serves the embedded static assets
// (index.html at "/", plus app.js/app.css) rooted at the dist directory. It is
// intended to be mounted at "/" on the daemon mux; http.ServeMux longest-prefix
// routing keeps the Connect handler on its own path prefix.
func Handler() http.Handler {
	sub, err := fs.Sub(assets, "dist")
	if err != nil {
		// The dist directory is embedded at compile time, so this can only fail
		// if the embed is broken — a programming error, not a runtime condition.
		panic("web: embedded dist FS unavailable: " + err.Error())
	}
	return http.FileServerFS(sub)
}
