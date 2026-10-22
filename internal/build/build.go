// Package build turns a Git source into an image build context (ADR-019, L2-005 BLD-01 – BLD-05): it
// resolves refs, checks out commits, and tars the build context the way `docker build` would, honouring
// .dockerignore. The engine driver does the build itself.
package build

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/enclavestack/platform/pkg/api"
)

// Credentials come from a source app's credentials secret, stored as JSON: {"token"} (optionally with
// "username") for https, {"privateKey","knownHosts"} for ssh.
type Credentials struct {
	Username   string `json:"username,omitempty"`
	Token      string `json:"token,omitempty"`
	PrivateKey string `json:"privateKey,omitempty"`
	KnownHosts string `json:"knownHosts,omitempty"`
}

// ParseCredentials reads a credentials secret's value.
func ParseCredentials(raw string) (Credentials, error) {
	var c Credentials
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return c, Permanent(errors.New(`the credentials secret isn't JSON like {"token": "…"} or {"privateKey": "…", "knownHosts": "…"}`))
	}
	if c.Token == "" && c.PrivateKey == "" {
		return c, Permanent(errors.New(`the credentials secret has neither "token" nor "privateKey"`))
	}
	if c.PrivateKey != "" && c.KnownHosts == "" {
		return c, Permanent(errors.New(`an SSH key needs "knownHosts" too: EnclaveStack never trusts a host on first use`))
	}
	return c, nil
}

// Repo is a repository and the ref to build.
type Repo struct {
	URL   string
	Ref   string
	Creds *Credentials
}

// Source reads repositories.
type Source interface {
	// Resolve returns the commit a ref points to now (a full commit SHA is returned as is).
	Resolve(ctx context.Context, r Repo) (string, error)
	// Checkout writes the working tree of the ref (or commit) into dir and returns the commit it holds.
	Checkout(ctx context.Context, r Repo, dir string) (string, error)
}

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// IsCommit reports whether a ref is a full commit SHA.
func IsCommit(ref string) bool { return commitPattern.MatchString(ref) }

// InputsHash identifies everything a build depends on, so an unchanged app skips its build (BLD-02).
func InputsHash(g api.GitSource, commit string) string {
	h := sha256.New()
	fmt.Fprintf(h, "git=%s\ncommit=%s\ndockerfile=%s\ncontext=%s\n", g.Git, commit, g.Dockerfile, g.Context)
	for _, k := range slices.Sorted(maps.Keys(g.BuildArgs)) {
		fmt.Fprintf(h, "arg %s=%s\n", k, g.BuildArgs[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Tag names a built image: enclave.local/<app>:r<revision>-<12-char commit>. Such images are never pulled.
func Tag(app string, revision int64, commit string) string {
	return fmt.Sprintf("%s%s:r%d-%s", LocalPrefix, app, revision, commit[:min(12, len(commit))])
}

// LocalPrefix starts every built image's name.
const LocalPrefix = "enclave.local/"

// IsLocal reports whether an image reference names a built image.
func IsLocal(ref string) bool { return strings.HasPrefix(ref, LocalPrefix) }

// permanentError marks failures that retrying can't fix (bad credentials, unknown ref, broken context).
type permanentError struct{ error }

func (p permanentError) Unwrap() error { return p.error }

// Permanent marks err as not worth retrying.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err}
}

// IsPermanent reports whether err was marked permanent.
func IsPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}
