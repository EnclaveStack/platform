// Package gittest serves real Git repositories over smart HTTP for tests (the system's git http-backend),
// optionally behind a token, so code that clones is tested against a real Git server.
package gittest

import (
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Server is a Git HTTP server holding bare repositories.
type Server struct {
	t    testing.TB
	root string
	URL  string
}

// New starts a server; with token set, every request needs it as the basic-auth password. It skips the
// test when git isn't installed.
func New(t testing.TB, token string) *Server {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	out, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Skip("git --exec-path failed")
	}
	backend := filepath.Join(strings.TrimSpace(string(out)), "git-http-backend")
	root := t.TempDir()
	h := &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" {
			if _, pw, ok := r.BasicAuth(); !ok || pw != token {
				w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return &Server{t: t, root: root, URL: srv.URL}
}

// Git runs git in dir with a fixed identity and no global config.
func (s *Server) Git(dir string, args ...string) string {
	s.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		s.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Repo creates a bare repository and a work tree that pushes to it; it returns the work tree and the URL.
func (s *Server) Repo(name string) (work, url string) {
	s.t.Helper()
	bare := filepath.Join(s.root, name+".git")
	s.Git(s.root, "init", "--bare", "-b", "main", bare)
	work = s.t.TempDir()
	s.Git(work, "init", "-b", "main")
	s.Git(work, "remote", "add", "origin", bare)
	return work, s.URL + "/" + name + ".git"
}

// Commit writes files into the work tree, commits them and returns the commit.
func (s *Server) Commit(work string, files map[string]string, msg string) string {
	s.t.Helper()
	for name, body := range files {
		p := filepath.Join(work, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			s.t.Fatal(err)
		}
	}
	s.Git(work, "add", "-A")
	s.Git(work, "commit", "-m", msg)
	return s.Git(work, "rev-parse", "HEAD")
}

// Push commits files and pushes the branch.
func (s *Server) Push(work string, files map[string]string, msg string) string {
	s.t.Helper()
	c := s.Commit(work, files, msg)
	s.Git(work, "push", "origin", "main")
	return c
}
