package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path"
	"path/filepath"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/filesync"
	"github.com/tonistiigi/fsutil"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
)

// Builds through the Engine API with BuildKit and a client session (ADR-019; spike recorded there).

const buildTimeout = 60 * time.Minute

// Build implements engine.Driver.
func (d *Driver) Build(ctx context.Context, in engine.BuildInput, log io.Writer) (engine.BuildResult, error) {
	bctx, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()
	// The context goes through the build session (file sync), as the Docker CLI sends it to BuildKit, not
	// as the request body: Docker Desktop's API proxy truncates chunked build bodies (ADR-019).
	ctxFS, err := fsutil.NewFS(in.ContextDir)
	if err != nil {
		return engine.BuildResult{}, engine.Permanent(fmt.Errorf("open the build context: %w", err))
	}
	filtered, err := fsutil.NewFilterFS(ctxFS, &fsutil.FilterOpt{ExcludePatterns: in.Excludes})
	if err != nil {
		return engine.BuildResult{}, engine.Permanent(fmt.Errorf(".dockerignore: %w", err))
	}
	dfFS, err := fsutil.NewFS(filepath.Dir(filepath.Join(in.ContextDir, filepath.FromSlash(in.Dockerfile))))
	if err != nil {
		return engine.BuildResult{}, engine.Permanent(fmt.Errorf("open the Dockerfile's directory: %w", err))
	}
	s, err := session.NewSession(bctx, "enclave-"+in.Resource.Name)
	if err != nil {
		return engine.BuildResult{}, err
	}
	s.Allow(filesync.NewFSSyncProvider(filesync.StaticDirSource{"context": filtered, "dockerfile": dfFS}))
	go func() {
		_ = s.Run(bctx, func(ctx context.Context, proto string, meta map[string][]string) (net.Conn, error) {
			return d.cli.DialHijack(ctx, "/session", proto, meta)
		})
	}()
	defer s.Close()
	args := map[string]*string{}
	for k, v := range in.BuildArgs {
		args[k] = &v
	}
	resp, err := d.cli.ImageBuild(bctx, nil, build.ImageBuildOptions{
		Tags: []string{in.Tag}, Dockerfile: path.Base(in.Dockerfile), BuildArgs: args, Version: build.BuilderBuildKit, SessionID: s.ID(),
		RemoteContext: "client-session", Remove: true, ForceRemove: true,
		Labels: map[string]string{LabelManaged: "true", LabelResourceID: in.Resource.ID, LabelKind: string(in.Resource.Kind)},
	})
	if err != nil {
		return engine.BuildResult{}, d.classify(fmt.Errorf("start the build: %w", err))
	}
	defer resp.Body.Close()
	p := newProgress(log)
	dec := json.NewDecoder(resp.Body)
	var failure string
	for {
		var m struct {
			Stream string          `json:"stream"`
			Error  string          `json:"error"`
			ID     string          `json:"id"`
			Aux    json.RawMessage `json:"aux"`
		}
		if err := dec.Decode(&m); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return engine.BuildResult{}, d.classify(fmt.Errorf("read the build output: %w", err))
		}
		switch {
		case m.Error != "":
			failure = m.Error
		case m.ID == "moby.buildkit.trace":
			p.trace(m.Aux)
		case m.Stream != "":
			fmt.Fprint(log, m.Stream)
		}
	}
	if failure != "" {
		fmt.Fprintf(log, "ERROR: %s\n", failure)
		return engine.BuildResult{}, classifyBuild(errors.New(failure))
	}
	img, err := d.cli.ImageInspect(bctx, in.Tag)
	if err != nil {
		return engine.BuildResult{}, d.classify(fmt.Errorf("inspect the built image: %w", err))
	}
	return engine.BuildResult{ImageID: img.ID}, nil
}

// classifyBuild: a Dockerfile problem fails the same way every time for this commit (BLD-03).
func classifyBuild(err error) error {
	msg := err.Error()
	for _, permanent := range []string{"did not complete successfully", "dockerfile parse error", "failed to compute cache key", "unknown instruction",
		"not found", "failed to parse", "pull access denied", "invalid reference format"} {
		if strings.Contains(msg, permanent) {
			return engine.Permanent(err)
		}
	}
	return err
}

// progress turns BuildKit's trace into lines like `docker build --progress=plain`: "#n step", "#n output".
type progress struct {
	w     io.Writer
	num   map[string]int
	seen  map[string]bool
	done  map[string]bool
	steps int
}

func newProgress(w io.Writer) *progress {
	return &progress{w: w, num: map[string]int{}, seen: map[string]bool{}, done: map[string]bool{}}
}

func (p *progress) id(digest string) int {
	if n, ok := p.num[digest]; ok {
		return n
	}
	p.steps++
	p.num[digest] = p.steps
	return p.steps
}

func (p *progress) trace(aux json.RawMessage) {
	var raw []byte
	if json.Unmarshal(aux, &raw) != nil {
		return
	}
	var st controlapi.StatusResponse
	if st.UnmarshalVT(raw) != nil {
		return
	}
	for _, v := range st.Vertexes {
		n := p.id(v.Digest)
		if v.Started != nil && !p.seen[v.Digest] {
			p.seen[v.Digest] = true
			fmt.Fprintf(p.w, "#%d %s\n", n, v.Name)
		}
		if v.Completed != nil && !p.done[v.Digest] {
			p.done[v.Digest] = true
			switch {
			case v.Error != "":
				fmt.Fprintf(p.w, "#%d ERROR: %s\n", n, v.Error)
			case v.Cached:
				fmt.Fprintf(p.w, "#%d CACHED\n", n)
			case v.Started != nil:
				fmt.Fprintf(p.w, "#%d DONE %.1fs\n", n, v.Completed.AsTime().Sub(v.Started.AsTime()).Seconds())
			}
		}
	}
	for _, l := range st.Logs {
		n := p.id(l.Vertex)
		for _, line := range strings.Split(strings.TrimRight(string(l.Msg), "\n"), "\n") {
			fmt.Fprintf(p.w, "#%d %s\n", n, line)
		}
	}
}

// ImageExists implements engine.Driver.
func (d *Driver) ImageExists(ctx context.Context, ref string) (bool, error) {
	if _, err := d.cli.ImageInspect(ctx, ref); err == nil {
		return true, nil
	} else if cerrdefs.IsNotFound(err) {
		return false, nil
	} else {
		return false, d.classify(err)
	}
}

// RemoveBuiltImages implements engine.Driver: every image labelled with the app, except keep.
func (d *Driver) RemoveBuiltImages(ctx context.Context, r *domain.Resource, keep []string) error {
	list, err := d.cli.ImageList(ctx, image.ListOptions{Filters: filters.NewArgs(filters.Arg("label", LabelResourceID+"="+r.ID))})
	if err != nil {
		return d.classify(err)
	}
	for _, img := range list {
		kept := false
		for _, tag := range img.RepoTags {
			for _, k := range keep {
				kept = kept || tag == k
			}
		}
		if kept {
			continue
		}
		_, err := d.cli.ImageRemove(ctx, img.ID, image.RemoveOptions{Force: true, PruneChildren: true})
		switch {
		case err == nil, cerrdefs.IsNotFound(err):
		case cerrdefs.IsConflict(err):
			// Still used by a container (e.g. a job run): kept now, removed by a later clean-up.
		default:
			return d.classify(fmt.Errorf("remove built image %s: %w", img.ID, err))
		}
	}
	return nil
}

// localImageMissing explains a built image that is gone: EnclaveStack never pulls enclave.local/ images.
func localImageMissing(ref string) error {
	return engine.Permanent(fmt.Errorf("the built image %s is gone; redeploy to build it again", ref))
}
