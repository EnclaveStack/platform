// Package webui serves the console's built assets. Release builds embed console/dist (build tag
// embedconsole, copied here by `make build`); development builds serve a directory from disk.
package webui

import (
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
)

// FS returns the console assets: embedded ones when built with -tags embedconsole, else dir on disk
// (nil when neither exists).
func FS(dir string) fs.FS {
	if embedded != nil {
		return embedded
	}
	if dir == "" {
		return nil
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil
	}
	return os.DirFS(dir)
}

const notBuilt = `<!doctype html><meta charset="utf-8"><title>EnclaveStack</title>
<body style="font-family:system-ui;max-width:40rem;margin:4rem auto;line-height:1.5">
<h1>The console isn't built</h1><p>The API is running at <code>/api/v1</code>. Build the console with
<code>make build</code>, or run <code>pnpm dev</code> for the development console on port 5173.</p></body>`

// Handler serves files from assets with single-page-app fallback to index.html. Hashed assets get
// long-lived caching; index.html is never cached so deploys take effect immediately.
func Handler(assets fs.FS) http.Handler {
	if assets == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(notBuilt))
		})
	}
	files := http.FileServerFS(assets)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		if st, err := fs.Stat(assets, name); err != nil || st.IsDir() {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
			name = "index.html"
		}
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}
