package build

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/go-git/go-git/v5/storage/memory"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Git reads repositories over https and ssh with go-git. TempDir holds known_hosts files while connecting.
type Git struct{ TempDir string }

func (g Git) auth(r Repo) (transport.AuthMethod, func(), error) {
	noop := func() {}
	if r.Creds == nil {
		return nil, noop, nil
	}
	u, err := url.Parse(r.URL)
	if err != nil {
		return nil, noop, Permanent(err)
	}
	if u.Scheme != "ssh" {
		if r.Creds.Token == "" {
			return nil, noop, Permanent(errors.New(`an https repository needs "token" in the credentials secret`))
		}
		user := r.Creds.Username
		if user == "" {
			user = "x-access-token" // GitHub, GitLab and Gitea accept a token with any user name
		}
		return &githttp.BasicAuth{Username: user, Password: r.Creds.Token}, noop, nil
	}
	if r.Creds.PrivateKey == "" {
		return nil, noop, Permanent(errors.New(`an ssh repository needs "privateKey" and "knownHosts" in the credentials secret`))
	}
	user := u.User.Username()
	if user == "" {
		user = "git"
	}
	keys, err := gitssh.NewPublicKeys(user, []byte(r.Creds.PrivateKey), "")
	if err != nil {
		return nil, noop, Permanent(fmt.Errorf("the SSH private key can't be read: %w", err))
	}
	f, err := os.CreateTemp(g.TempDir, "known_hosts-*")
	if err != nil {
		return nil, noop, err
	}
	cleanup := func() { _ = os.Remove(f.Name()) }
	_, werr := f.WriteString(r.Creds.KnownHosts + "\n")
	_ = f.Close()
	if werr != nil {
		cleanup()
		return nil, noop, werr
	}
	cb, err := knownhosts.New(f.Name())
	if err != nil {
		cleanup()
		return nil, noop, Permanent(fmt.Errorf("knownHosts can't be read: %w", err))
	}
	keys.HostKeyCallback = cb
	return keys, cleanup, nil
}

// classify marks errors no retry can fix as permanent; network trouble stays transient.
func classify(err error, r Repo) error {
	switch {
	case err == nil:
		return nil
	case (errors.Is(err, transport.ErrAuthenticationRequired) || errors.Is(err, transport.ErrAuthorizationFailed)) && r.Creds == nil:
		// Hosts like GitHub answer a missing repository the same way as a private one.
		return Permanent(fmt.Errorf("there's no repository at %s, or it is private: a private repository needs a credentials secret", r.URL))
	case errors.Is(err, transport.ErrAuthenticationRequired), errors.Is(err, transport.ErrAuthorizationFailed):
		return Permanent(fmt.Errorf("the repository refused access: check the credentials secret (%w)", err))
	case errors.Is(err, transport.ErrRepositoryNotFound):
		return Permanent(fmt.Errorf("there's no repository at %s, or the credentials can't see it", r.URL))
	case errors.Is(err, plumbing.ErrReferenceNotFound), errors.Is(err, git.ErrBranchNotFound):
		return Permanent(fmt.Errorf("the repository has no branch or tag %q", r.Ref))
	case strings.Contains(err.Error(), "knownhosts: key mismatch"), strings.Contains(err.Error(), "knownhosts: key is unknown"):
		return Permanent(fmt.Errorf("the SSH host key doesn't match knownHosts: %w", err))
	}
	return fmt.Errorf("the repository can't be reached: %w", err)
}

// Resolve implements Source.
func (g Git) Resolve(ctx context.Context, r Repo) (string, error) {
	if IsCommit(r.Ref) {
		return r.Ref, nil
	}
	auth, cleanup, err := g.auth(r)
	if err != nil {
		return "", err
	}
	defer cleanup()
	remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{Name: "origin", URLs: []string{r.URL}})
	refs, err := remote.ListContext(ctx, &git.ListOptions{Auth: auth, PeelingOption: git.AppendPeeled})
	if err != nil {
		return "", classify(err, r)
	}
	found := map[plumbing.ReferenceName]plumbing.Hash{}
	for _, ref := range refs {
		if ref.Type() == plumbing.HashReference {
			found[ref.Name()] = ref.Hash()
		}
	}
	candidates := []plumbing.ReferenceName{plumbing.NewBranchReferenceName(r.Ref), plumbing.ReferenceName("refs/tags/" + r.Ref + "^{}"), plumbing.NewTagReferenceName(r.Ref)}
	if r.Ref == "HEAD" || strings.HasPrefix(r.Ref, "refs/") {
		candidates = []plumbing.ReferenceName{plumbing.ReferenceName(r.Ref)}
	}
	for _, name := range candidates {
		if h, ok := found[name]; ok {
			return h.String(), nil
		}
	}
	if r.Ref == "HEAD" {
		for _, ref := range refs { // HEAD is usually symbolic: follow it
			if ref.Name() == plumbing.HEAD && ref.Type() == plumbing.SymbolicReference {
				if h, ok := found[ref.Target()]; ok {
					return h.String(), nil
				}
			}
		}
	}
	return "", Permanent(fmt.Errorf("the repository has no branch or tag %q", r.Ref))
}

// Checkout implements Source: a shallow single-branch clone for branches and tags, a full clone for a commit.
func (g Git) Checkout(ctx context.Context, r Repo, dir string) (string, error) {
	auth, cleanup, err := g.auth(r)
	if err != nil {
		return "", err
	}
	defer cleanup()
	if IsCommit(r.Ref) {
		repo, err := git.PlainCloneContext(ctx, dir, false, &git.CloneOptions{URL: r.URL, Auth: auth, NoCheckout: true, Tags: git.NoTags})
		if err != nil {
			return "", classify(err, r)
		}
		wt, err := repo.Worktree()
		if err != nil {
			return "", err
		}
		if err := wt.Checkout(&git.CheckoutOptions{Hash: plumbing.NewHash(r.Ref), Force: true}); err != nil {
			return "", Permanent(fmt.Errorf("the repository has no commit %s: %w", r.Ref, err))
		}
		return r.Ref, nil
	}
	if strings.HasPrefix(r.Ref, "refs/") { // a full ref, like a pull request's head (refs/pull/7/head, ADR-056)
		return fetchRef(ctx, r, auth, dir)
	}
	var lastErr error
	for _, name := range []plumbing.ReferenceName{plumbing.NewBranchReferenceName(r.Ref), plumbing.NewTagReferenceName(r.Ref)} {
		_ = os.RemoveAll(dir)
		repo, err := git.PlainCloneContext(ctx, dir, false, &git.CloneOptions{URL: r.URL, Auth: auth, ReferenceName: name, SingleBranch: true, Depth: 1, Tags: git.NoTags})
		if err != nil {
			lastErr = err
			if errors.Is(err, plumbing.ErrReferenceNotFound) || strings.Contains(err.Error(), "couldn't find remote ref") {
				continue // try the ref as a tag
			}
			return "", classify(err, r)
		}
		head, err := repo.Head()
		if err != nil {
			return "", err
		}
		return head.Hash().String(), nil
	}
	return "", classify(lastErr, r)
}

// fetchRef checks out a full ref that isn't a branch or tag: fetched alone, one commit deep.
func fetchRef(ctx context.Context, r Repo, auth transport.AuthMethod, dir string) (string, error) {
	_ = os.RemoveAll(dir)
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		return "", err
	}
	remote, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{r.URL}})
	if err != nil {
		return "", err
	}
	local := plumbing.ReferenceName("refs/remotes/origin/enclave-checkout")
	spec := config.RefSpec("+" + r.Ref + ":" + local.String())
	if err := remote.FetchContext(ctx, &git.FetchOptions{RefSpecs: []config.RefSpec{spec}, Auth: auth, Depth: 1, Tags: git.NoTags}); err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) || strings.Contains(err.Error(), "couldn't find remote ref") {
			return "", Permanent(fmt.Errorf("the repository has no ref %q", r.Ref))
		}
		return "", classify(err, r)
	}
	ref, err := repo.Reference(local, true)
	if err != nil {
		return "", err
	}
	wt, err := repo.Worktree()
	if err != nil {
		return "", err
	}
	if err := wt.Checkout(&git.CheckoutOptions{Hash: ref.Hash(), Force: true}); err != nil {
		return "", err
	}
	return ref.Hash().String(), nil
}

// CleanDir removes a checkout.
func CleanDir(dir string) { _ = os.RemoveAll(filepath.Clean(dir)) }
