package webui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

func TestServesAssetsWithSPAFallbackAndCacheHeaders(t *testing.T) {
	assets := fstest.MapFS{
		"index.html":      {Data: []byte("<!doctype html><title>console</title>")},
		"assets/app-1.js": {Data: []byte("console.log(1)")},
		"favicon.svg":     {Data: []byte("<svg/>")},
	}
	h := Handler(assets)
	if rec := get(t, h, "/assets/app-1.js"); rec.Code != 200 || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("hashed asset: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	for _, path := range []string{"/", "/apps", "/apps/app_123", "/settings/tokens"} {
		rec := get(t, h, path)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<title>console</title>") || rec.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: %d cache=%q", path, rec.Code, rec.Header().Get("Cache-Control"))
		}
	}
	if rec := get(t, h, "/favicon.svg"); rec.Code != 200 || rec.Body.String() != "<svg/>" {
		t.Errorf("favicon: %d", rec.Code)
	}
}

func TestNotBuiltPageAndDiskFallback(t *testing.T) {
	rec := get(t, Handler(nil), "/")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "isn't built") {
		t.Errorf("not-built page: %d", rec.Code)
	}
	if FS("") != nil || FS(filepath.Join(t.TempDir(), "missing")) != nil {
		t.Error("missing directory should give no assets")
	}
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "index.html"), []byte("disk"), 0o644)
	if rec := get(t, Handler(FS(dir)), "/x"); rec.Body.String() != "disk" {
		t.Errorf("disk assets: %q", rec.Body.String())
	}
}
