package gate

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The gate's stylesheet is the design system's (ADR-014): `make contracts` copies tokens.css and gate.css
// here, and this fails when the copy is stale.
func TestStylesheetIsTheDesignSystems(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	ui := filepath.Join(filepath.Dir(file), "..", "..", "packages", "ui", "src")
	tokens, err := os.ReadFile(filepath.Join(ui, "tokens.css"))
	if err != nil {
		t.Fatal(err)
	}
	gate, err := os.ReadFile(filepath.Join(ui, "gate.css"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := files.ReadFile("static/gate.css")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(tokens)+string(gate) {
		t.Fatal("internal/gate/static/gate.css is stale: run `make contracts` and commit the result")
	}
}

// A sign-in only ever returns to a path on the same host (no open redirect).
func TestSafeNext(t *testing.T) {
	cases := map[string]string{
		"":                         "/",
		"/orders?id=7":             "/orders?id=7",
		"//evil.example/x":         "/",
		"/\\evil.example":          "/",
		"https://evil.example/":    "/",
		"/ok\r\nSet-Cookie: x=1":   "/",
		"/.enclave/gate/signin":    "/",
		"javascript:alert(1)":      "/",
		"/reports/2026/q3.pdf#top": "/reports/2026/q3.pdf#top",
	}
	for in, want := range cases {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

// Every page is a full document with the stylesheet and no script.
func TestPagesHaveNoScripts(t *testing.T) {
	for _, name := range []string{"signin.html", "private.html", "signedout.html"} {
		var b strings.Builder
		if err := pages.ExecuteTemplate(&b, name, view{Host: "stage.example.com", Next: "/", Email: "a@example.com"}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		html := b.String()
		if strings.Contains(strings.ToLower(html), "<script") || !strings.Contains(html, PathStyle) || !strings.Contains(html, "stage.example.com") {
			t.Errorf("%s:\n%s", name, html)
		}
	}
}
