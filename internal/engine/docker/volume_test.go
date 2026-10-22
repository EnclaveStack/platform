//go:build docker

package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// Filesystem volumes on real Docker (ADR-027, ADR-028, L5-002 §8): shared by replicas and jobs, kept
// across deploys, copied out and back through the archive API.

func newVolume(t *testing.T, d *Driver) *domain.Resource {
	t.Helper()
	r := resource(api.KindVolume, "it-vol-"+suffix())
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := d.EnsureVolume(ctx, r); err != nil {
		t.Fatalf("ensure volume: %v", err)
	}
	if err := d.EnsureVolume(ctx, r); err != nil { // idempotent
		t.Fatalf("second ensure: %v", err)
	}
	t.Cleanup(func() {
		if err := d.Delete(context.Background(), r, engine.ObjectStore{}); err != nil {
			t.Errorf("delete volume: %v", err)
		}
	})
	return r
}

func tarNames(t *testing.T, b []byte) map[string]string {
	t.Helper()
	out := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		if h.Typeflag == tar.TypeReg {
			c, _ := io.ReadAll(tr)
			out[h.Name] = string(c)
		}
	}
}

func TestFilesystemVolumesShareSnapshotAndRestore(t *testing.T) {
	d := fastDriver(t)
	ctx := context.Background()
	vol := newVolume(t, d)
	mount := []engine.Mount{{Volume: engine.FilesystemVolume(vol.ID), Path: "/data"}}

	// A job writes; an app's replicas read the same files, before and after a new revision.
	run := engine.JobRun{RunID: domain.NewID("run"), ResourceID: "it-writer", Name: "it-writer", Image: "busybox:1.37", MemoryMiB: 32, Mounts: mount,
		Command: []string{"sh", "-c", "mkdir -p /data/dir && echo one > /data/a.txt && echo two > /data/dir/b.txt"}}
	if err := d.StartJob(ctx, run); err != nil {
		t.Fatal(err)
	}
	if res, err := d.WaitJob(ctx, run.RunID, 30*time.Second); err != nil || res.ExitCode != 0 {
		t.Fatalf("writer: %v %+v", err, res)
	}
	app := resource(api.KindApp, "it-mount-"+suffix())
	cleanup(t, d, app, engine.ObjectStore{})
	spec := api.AppSpec{Image: "busybox:1.37", Command: []string{"httpd", "-f", "-p", "8080", "-h", "/data"}, Port: 8080, MemoryMiB: 64, Strategy: api.StrategyRolling, Replicas: 2,
		Volumes: []api.VolumeMount{{Volume: "v", Path: "/data", ReadOnly: true}}}
	ensure(t, d, engine.EnsureInput{Resource: app, Spec: spec, Revision: 1, Mounts: []engine.Mount{{Volume: mount[0].Volume, Path: "/data", ReadOnly: true}}})
	for i := 1; i <= 2; i++ {
		name := engine.RevisionName(app, 1)
		if i == 2 {
			name += "-2"
		}
		out := execOut(t, d, name, "cat", "/data/a.txt")
		if strings.TrimSpace(out) != "one" {
			t.Errorf("replica %d reads %q", i, out)
		}
		if code, _ := execCode(t, d, name, "sh", "-c", "echo x > /data/new"); code == 0 {
			t.Errorf("replica %d could write to a read-only mount", i)
		}
	}
	app.Generation++
	ensure(t, d, engine.EnsureInput{Resource: app, Spec: spec, Revision: 2, Mounts: []engine.Mount{{Volume: mount[0].Volume, Path: "/data", ReadOnly: true}}})
	if out := execOut(t, d, engine.RevisionName(app, 2), "cat", "/data/dir/b.txt"); strings.TrimSpace(out) != "two" {
		t.Errorf("the new revision reads %q", out)
	}

	// Usage is reported once the engine counts it.
	if n, err := d.VolumeUsage(ctx, vol); err != nil || n <= 0 {
		t.Errorf("usage %d %v", n, err)
	}

	// Snapshot out and restore into another volume, through the archive API only.
	var buf bytes.Buffer
	if err := d.SnapshotVolume(ctx, vol, &buf); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	got := tarNames(t, buf.Bytes())
	found := map[string]string{}
	for name, c := range got {
		found[strings.TrimPrefix(strings.TrimPrefix(name, "./"), "/")] = c
	}
	if found["a.txt"] != "one\n" || found["dir/b.txt"] != "two\n" {
		t.Fatalf("snapshot entries %v", got)
	}
	other := newVolume(t, d)
	if err := d.RestoreVolume(ctx, other, bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("restore: %v", err)
	}
	check := engine.JobRun{RunID: domain.NewID("run"), ResourceID: "it-reader", Name: "it-reader", Image: "busybox:1.37", MemoryMiB: 32,
		Mounts: []engine.Mount{{Volume: engine.FilesystemVolume(other.ID), Path: "/r"}}, Command: []string{"cat", "/r/a.txt", "/r/dir/b.txt"}}
	if err := d.StartJob(ctx, check); err != nil {
		t.Fatal(err)
	}
	res, err := d.WaitJob(ctx, check.RunID, 30*time.Second)
	if err != nil || res.ExitCode != 0 || string(res.Output) != "one\ntwo\n" {
		t.Errorf("restored volume reads %q (exit %d, %v)", res.Output, res.ExitCode, err)
	}

	// Copy helpers never outlive the copy.
	list, _ := d.cli.ContainerList(ctx, containerListAll())
	for _, c := range list {
		for _, n := range c.Names {
			if strings.Contains(n, copyHelperPrefix) {
				t.Errorf("copy helper left behind: %s", n)
			}
		}
	}

	// Drift: a volume removed outside EnclaveStack is reported missing.
	if err := d.Delete(ctx, other, engine.ObjectStore{}); err != nil {
		t.Fatal(err)
	}
	if h, err := d.Health(ctx, other, engine.ObjectStore{}); err != nil || h != engine.Missing {
		t.Errorf("health of a removed volume: %v %v", h, err)
	}
}

func execCode(t *testing.T, d *Driver, name string, cmd ...string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := d.exec(ctx, name, cmd, nil)
	if err != nil {
		t.Fatalf("exec %v in %s: %v", cmd, name, err)
	}
	return res.ExitCode, res.Output
}

func execOut(t *testing.T, d *Driver, name string, cmd ...string) string {
	t.Helper()
	_, out := execCode(t, d, name, cmd...)
	return out
}

func containerListAll() container.ListOptions { return container.ListOptions{All: true} }
