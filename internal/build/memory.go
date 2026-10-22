package build

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// Memory is an in-memory Source for the fake engine (tests, demos): there is no network to clone from.
// Any repository resolves, holding a minimal Dockerfile, until Push gives it content.
type Memory struct {
	mu    sync.Mutex
	repos map[string]*memRepo // URL → repo
}

type memRepo struct {
	refs    map[string]string            // ref → commit
	files   map[string]map[string]string // commit → path → content
	refuse  string
	pushNum int
}

// DefaultDockerfile is what an unknown repository holds.
const DefaultDockerfile = "FROM busybox:1.37\nCMD [\"httpd\", \"-f\", \"-p\", \"8080\"]\n"

// NewMemory returns an empty in-memory Source.
func NewMemory() *Memory { return &Memory{repos: map[string]*memRepo{}} }

func (m *Memory) repo(url string) *memRepo {
	r := m.repos[url]
	if r == nil {
		r = &memRepo{refs: map[string]string{}, files: map[string]map[string]string{}}
		m.repos[url] = r
	}
	return r
}

// Push makes ref point at a new commit holding files, and returns the commit.
func (m *Memory) Push(url, ref string, files map[string]string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.repo(url)
	r.pushNum++
	sum := sha1.Sum([]byte(url + "#" + ref + "#" + string(rune('a'+r.pushNum))))
	commit := hex.EncodeToString(sum[:])
	r.refs[ref], r.files[commit] = commit, files
	return commit
}

// Refuse makes a repository fail with msg as a permanent error ("" clears it), like rejected credentials.
func (m *Memory) Refuse(url, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.repo(url).refuse = msg
}

func (m *Memory) lookup(r Repo) (string, map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	repo := m.repo(r.URL)
	if repo.refuse != "" {
		return "", nil, Permanent(errors.New(repo.refuse))
	}
	if IsCommit(r.Ref) {
		if f, ok := repo.files[r.Ref]; ok {
			return r.Ref, f, nil
		}
	}
	commit, ok := repo.refs[r.Ref]
	if !ok {
		sum := sha1.Sum([]byte(r.URL + "#" + r.Ref))
		commit = hex.EncodeToString(sum[:])
		repo.refs[r.Ref], repo.files[commit] = commit, map[string]string{"Dockerfile": DefaultDockerfile}
	}
	return commit, repo.files[commit], nil
}

// Resolve implements Source.
func (m *Memory) Resolve(_ context.Context, r Repo) (string, error) {
	commit, _, err := m.lookup(r)
	return commit, err
}

// Checkout implements Source.
func (m *Memory) Checkout(_ context.Context, r Repo, dir string) (string, error) {
	commit, files, err := m.lookup(r)
	if err != nil {
		return "", err
	}
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return "", err
		}
	}
	return commit, nil
}
