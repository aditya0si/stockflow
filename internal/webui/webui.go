// Package webui serves the embedded operator UI. The Vite build writes into
// internal/webui/dist. When the UI has not been built, dist contains only a
// placeholder and Handler serves a small explanatory page instead, so the Go
// binary always compiles and the API remains usable.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var assets embed.FS

const missingIndexPage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>StockFlow operator UI</title></head>
<body style="font-family: system-ui, sans-serif; max-width: 40rem; margin: 3rem auto; padding: 0 1rem;">
<h1>Operator UI not built</h1>
<p>The API is running, but the bundled web assets were not built into this binary.</p>
<pre>cd web &amp;&amp; npm ci &amp;&amp; npm run build</pre>
<p>Then rebuild the Go binary, or run <code>docker compose up --build</code>.</p>
</body></html>`

// Handler serves the SPA, falling back to index.html for client-side routes.
func Handler() http.Handler {
	sub, err := fs.Sub(assets, "dist")
	if err != nil {
		return fallbackOnly()
	}
	index, indexErr := fs.ReadFile(sub, "index.html")
	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		path := strings.TrimPrefix(r.URL.Path, "/")
		if path != "" {
			if info, statErr := fs.Stat(sub, path); statErr == nil && !info.IsDir() {
				fileServer.ServeHTTP(w, r)
				return
			}
		}

		if indexErr != nil {
			serveMissing(w)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(index)
	})
}

func fallbackOnly() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveMissing(w)
	})
}

func serveMissing(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(missingIndexPage))
}
