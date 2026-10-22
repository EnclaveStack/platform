package build

import (
	"context"
	iofs "io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tonistiigi/fsutil"

	"github.com/enclavestack/platform/internal/build/gittest"
	"github.com/enclavestack/platform/pkg/api"
)

func TestGitResolvesAndChecksOutBranchesTagsAndCommits(t *testing.T) {
	srv := gittest.New(t, "")
	work, url := srv.Repo("api")
	first := srv.Commit(work, map[string]string{"Dockerfile": "FROM busybox:1.37\n", "v.txt": "one"}, "one")
	srv.Git(work, "tag", "-a", "v1", "-m", "v1")
	srv.Git(work, "tag", "light")
	second := srv.Commit(work, map[string]string{"v.txt": "two"}, "two")
	srv.Git(work, "push", "origin", "main", "--tags")

	ctx := context.Background()
	g := Git{TempDir: t.TempDir()}
	for ref, want := range map[string]string{"main": second, "v1": first, "light": first, "HEAD": second, first: first} {
		if got, err := g.Resolve(ctx, Repo{URL: url, Ref: ref}); err != nil || got != want {
			t.Errorf("resolve %s = %s %v, want %s", ref, got, err, want)
		}
	}
	if _, err := g.Resolve(ctx, Repo{URL: url, Ref: "nope"}); !IsPermanent(err) || !strings.Contains(err.Error(), `no branch or tag "nope"`) {
		t.Errorf("unknown ref: %v", err)
	}
	if _, err := g.Resolve(ctx, Repo{URL: srv.URL + "/missing.git", Ref: "main"}); !IsPermanent(err) {
		t.Errorf("missing repository should be permanent: %v", err)
	}
	for ref, want := range map[string]struct{ commit, body string }{"main": {second, "two"}, "v1": {first, "one"}, first: {first, "one"}} {
		dir := filepath.Join(t.TempDir(), "co")
		got, err := g.Checkout(ctx, Repo{URL: url, Ref: ref}, dir)
		body, _ := os.ReadFile(filepath.Join(dir, "v.txt"))
		if err != nil || got != want.commit || string(body) != want.body {
			t.Errorf("checkout %s: %s %q %v", ref, got, body, err)
		}
	}
}

func TestGitTokenAuth(t *testing.T) {
	srv := gittest.New(t, "s3cret-token")
	work, url := srv.Repo("private")
	head := srv.Commit(work, map[string]string{"Dockerfile": "FROM busybox:1.37\n"}, "init")
	srv.Git(work, "-c", "http.extraHeader=", "push", "origin", "main") // pushes to the bare path directly
	ctx := context.Background()
	g := Git{TempDir: t.TempDir()}
	if _, err := g.Resolve(ctx, Repo{URL: url, Ref: "main"}); !IsPermanent(err) || !strings.Contains(err.Error(), "or it is private: a private repository needs a credentials secret") {
		t.Errorf("no credentials: %v", err)
	}
	if _, err := g.Resolve(ctx, Repo{URL: url, Ref: "main", Creds: &Credentials{Token: "wrong"}}); !IsPermanent(err) || !strings.Contains(err.Error(), "check the credentials secret") {
		t.Errorf("wrong token: %v", err)
	}
	if got, err := g.Resolve(ctx, Repo{URL: url, Ref: "main", Creds: &Credentials{Token: "s3cret-token"}}); err != nil || got != head {
		t.Errorf("token: %s %v", got, err)
	}
	if got, err := g.Checkout(ctx, Repo{URL: url, Ref: "main", Creds: &Credentials{Token: "s3cret-token"}}, filepath.Join(t.TempDir(), "co")); err != nil || got != head {
		t.Errorf("checkout with token: %s %v", got, err)
	}
}

func TestUnreachableRepositoryIsTransient(t *testing.T) {
	_, err := Git{}.Resolve(context.Background(), Repo{URL: "http://127.0.0.1:1/x.git", Ref: "main"})
	if err == nil || IsPermanent(err) {
		t.Errorf("connection refused should be transient: %v", err)
	}
}

// sent lists what BuildKit receives from a context: the files fsutil walks after the excludes.
func sent(t *testing.T, bc Context) []string {
	t.Helper()
	base, err := fsutil.NewFS(bc.Dir)
	if err != nil {
		t.Fatal(err)
	}
	fs, err := fsutil.NewFilterFS(base, &fsutil.FilterOpt{ExcludePatterns: bc.Excludes})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	err = fs.Walk(context.Background(), "", func(p string, d iofs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			names = append(names, filepath.ToSlash(p))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(names)
	return names
}

func TestPrepareContextHonoursDockerignoreAndStaysInside(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"app/Dockerfile":            "FROM busybox:1.37\n",
		"app/.dockerignore":         "node_modules\n*.env\n!keep.env\n",
		"app/main.go":               "package main\n",
		"app/keep.env":              "A=1\n",
		"app/secret.env":            "TOKEN=x\n",
		"app/node_modules/x/i.js":   "x",
		"app/src/lib/util.go":       "package lib\n",
		"other/outside.txt":         "no",
		"app/docker/Dockerfile.dev": "FROM busybox:1.37\n",
	}
	for name, body := range files {
		p := filepath.Join(root, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(body), 0o644)
	}
	bc, err := PrepareContext(root, "app", "Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	got := sent(t, bc)
	want := []string{".dockerignore", "Dockerfile", "docker/Dockerfile.dev", "keep.env", "main.go", "src/lib/util.go"}
	if !slices.Equal(got, want) {
		t.Errorf("context\n got %v\nwant %v", got, want)
	}
	if bc, err := PrepareContext(root, "app", "./docker/Dockerfile.dev"); err != nil || bc.Dockerfile != "docker/Dockerfile.dev" {
		t.Errorf("nested Dockerfile: %+v %v", bc, err)
	}
	if _, err := PrepareContext(root, "app", "Missing"); !IsPermanent(err) {
		t.Errorf("missing Dockerfile: %v", err)
	}
	if _, err := PrepareContext(root, "nope", "Dockerfile"); !IsPermanent(err) {
		t.Errorf("missing context: %v", err)
	}
	if _, err := PrepareContext(root, "app/main.go", "Dockerfile"); !IsPermanent(err) {
		t.Errorf("a file as the context: %v", err)
	}
	outside := t.TempDir()
	_ = os.WriteFile(filepath.Join(outside, "Dockerfile"), []byte("FROM scratch\n"), 0o644)
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareContext(root, "escape", "Dockerfile"); !IsPermanent(err) || !strings.Contains(err.Error(), "leaves the repository") {
		t.Errorf("context through a symlink out of the repo: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "Dockerfile"), filepath.Join(root, "app", "Linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareContext(root, "app", "Linked"); !IsPermanent(err) {
		t.Errorf("Dockerfile symlinked out of the context: %v", err)
	}
}

func TestInputsHashCredentialsTagAndMemory(t *testing.T) {
	g := api.GitSource{Git: "https://github.com/acme/api.git", Ref: "main", Dockerfile: "Dockerfile", Context: ".", BuildArgs: map[string]string{"A": "1", "B": "2"}}
	base := InputsHash(g, "aaaa")
	if InputsHash(g, "aaaa") != base {
		t.Error("hash not stable")
	}
	g2 := g
	g2.BuildArgs = map[string]string{"B": "2", "A": "1"}
	if InputsHash(g2, "aaaa") != base {
		t.Error("hash depends on map order")
	}
	g2.Ref = "release" // the ref doesn't matter, only the commit it resolved to
	if InputsHash(g2, "aaaa") != base {
		t.Error("ref changed the hash")
	}
	for _, change := range []func(*api.GitSource){func(s *api.GitSource) { s.Dockerfile = "x" }, func(s *api.GitSource) { s.Context = "y" }, func(s *api.GitSource) { s.BuildArgs = map[string]string{"A": "9"} }} {
		c := g
		change(&c)
		if InputsHash(c, "aaaa") == base {
			t.Errorf("a changed input kept the hash: %+v", c)
		}
	}
	if InputsHash(g, "bbbb") == base {
		t.Error("commit change kept the hash")
	}
	if tag := Tag("api", 7, "0123456789abcdef0123456789abcdef01234567"); tag != "enclave.local/api:r7-0123456789ab" || !IsLocal(tag) || IsLocal("nginx:1.27") {
		t.Errorf("tag %q", tag)
	}
	for raw, ok := range map[string]bool{`{"token":"t"}`: true, `{"privateKey":"k","knownHosts":"h"}`: true, `{"privateKey":"k"}`: false, `{}`: false, `token`: false} {
		if _, err := ParseCredentials(raw); (err == nil) != ok {
			t.Errorf("credentials %s: %v", raw, err)
		}
	}

	m := NewMemory()
	ctx := context.Background()
	r := Repo{URL: "https://example.com/a.git", Ref: "main"}
	c1, _ := m.Resolve(ctx, r)
	if c1b, _ := m.Resolve(ctx, r); c1b != c1 || !IsCommit(c1) {
		t.Errorf("memory resolve %s %s", c1, c1b)
	}
	c2 := m.Push(r.URL, "main", map[string]string{"Dockerfile": "FROM busybox:1.37\n", "x": "y"})
	dir := t.TempDir()
	if got, err := m.Checkout(ctx, r, dir); err != nil || got != c2 || got == c1 {
		t.Errorf("after push: %s %v", got, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "x")); string(b) != "y" {
		t.Errorf("checkout content %q", b)
	}
	m.Refuse(r.URL, "denied")
	if _, err := m.Resolve(ctx, r); !IsPermanent(err) {
		t.Errorf("refused: %v", err)
	}
}
