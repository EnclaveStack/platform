package fake

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
)

// Builds on the fake engine (ADR-019): each Dockerfile instruction is a logged step, and a RUN that ends
// in "exit N" (N > 0) fails the way BuildKit reports it. No image is really built.

var exitCode = regexp.MustCompile(`exit\s+([1-9][0-9]*)`)

// Build implements engine.Driver.
func (d *Driver) Build(ctx context.Context, in engine.BuildInput, log io.Writer) (engine.BuildResult, error) {
	if err := d.Ping(ctx); err != nil {
		return engine.BuildResult{}, err
	}
	raw, err := os.ReadFile(filepath.Join(in.ContextDir, filepath.FromSlash(in.Dockerfile)))
	if err != nil {
		return engine.BuildResult{}, engine.Permanent(fmt.Errorf("there's no %s in the build context", in.Dockerfile))
	}
	dockerfile := string(raw)
	var steps []string
	sc := bufio.NewScanner(strings.NewReader(dockerfile))
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" && !strings.HasPrefix(line, "#") {
			steps = append(steps, line)
		}
	}
	for i, step := range steps {
		if err := d.sleep(ctx); err != nil {
			return engine.BuildResult{}, err
		}
		fmt.Fprintf(log, "#%d [%d/%d] %s\n", i+1, i+1, len(steps), step)
		if cmd, ok := strings.CutPrefix(step, "RUN "); ok {
			for k, v := range in.BuildArgs {
				cmd = strings.ReplaceAll(cmd, "$"+k, v)
			}
			if out, ok := strings.CutPrefix(cmd, "echo "); ok && !exitCode.MatchString(cmd) {
				fmt.Fprintf(log, "#%d %s\n", i+1, out) // the step's output, like BuildKit's log lines
			}
			if m := exitCode.FindStringSubmatch(cmd); m != nil {
				fmt.Fprintf(log, "#%d ERROR: exit code: %s\n", i+1, m[1])
				return engine.BuildResult{}, engine.Permanent(fmt.Errorf("process \"/bin/sh -c %s\" did not complete successfully: exit code: %s", cmd, m[1]))
			}
		}
	}
	d.mu.Lock()
	d.images[in.Tag] = in.Resource.ID
	d.builds++
	id := fmt.Sprintf("sha256:%064d", d.builds)
	d.mu.Unlock()
	fmt.Fprintf(log, "#%d naming to %s done\n", len(steps)+1, in.Tag)
	return engine.BuildResult{ImageID: id}, nil
}

// HasImage implements engine.Driver: built images exist once built; any other image "pulls".
func (d *Driver) HasImage(_ context.Context, _ *domain.Resource, ref string) (bool, error) {
	if !strings.HasPrefix(ref, "enclave.local/") {
		return true, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.images[ref]
	return ok, nil
}

// RemoveBuiltImages implements engine.Driver.
func (d *Driver) RemoveBuiltImages(_ context.Context, r *domain.Resource, keep []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for tag, id := range d.images {
		if id == r.ID && !contains(keep, tag) {
			delete(d.images, tag)
		}
	}
	return nil
}

// BuiltImages returns an app's built image tags (tests).
func (d *Driver) BuiltImages(resourceID string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for tag, id := range d.images {
		if id == resourceID {
			out = append(out, tag)
		}
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
