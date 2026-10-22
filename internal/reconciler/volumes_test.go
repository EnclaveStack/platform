package reconciler_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/server"
	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
)

// Volumes and snapshots (ADR-027, ADR-028, L2-007 VOL-*, SNP-*) through the real API on the fake engine
// and the fake hypervisor.

func TestFilesystemVolumesMountIntoAppsAndJobsAndSurviveDeploys(t *testing.T) {
	env := testenv.Start(t, testenv.WithConfig(func(c *server.Config) { c.DriftEvery = 100 * time.Millisecond }))
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()

	vol, err := c.CreateResource(ctx, api.KindVolume, "uploads", api.VolumeSpec{Type: api.VolumeFilesystem})
	if err != nil {
		t.Fatal(err)
	}
	waitReadyWithin(t, c, vol.ID, 10*time.Second)
	if !env.Fake.VolumeExists(vol.ID) {
		t.Fatal("the engine volume wasn't created")
	}
	app, err := c.CreateResource(ctx, api.KindApp, "web", api.AppSpec{Image: "nginx:1.27", Port: 80, Replicas: 2,
		Volumes: []api.VolumeMount{{Volume: "uploads", Path: "/usr/share/nginx/html"}}})
	if err != nil {
		t.Fatal(err)
	}
	waitReadyWithin(t, c, app.ID, 10*time.Second)
	want := engine.Mount{Volume: engine.FilesystemVolume(vol.ID), Path: "/usr/share/nginx/html"}
	if m := env.Fake.Mounts(app.ID); len(m) != 1 || m[0] != want {
		t.Fatalf("app mounts %+v, want %+v", m, want)
	}

	// The same volume after a redeploy (VOL-04); a read-only mount in a job.
	env.Fake.WriteFile(vol.ID, "index.html", "hello")
	if _, err := c.Action(ctx, app.ID, api.ActionRedeploy); err != nil {
		t.Fatal(err)
	}
	waitReadyWithin(t, c, app.ID, 10*time.Second)
	if m := env.Fake.Mounts(app.ID); len(m) != 1 || m[0] != want || env.Fake.Files(vol.ID)["index.html"] != "hello" {
		t.Errorf("after a redeploy: mounts %+v files %v", m, env.Fake.Files(vol.ID))
	}
	job := newJob(t, c, "thumbs", api.JobSpec{Schedule: "@daily", Image: "alpine:3.20", Volumes: []api.VolumeMount{{Volume: "uploads", Path: "/in", ReadOnly: true}}})
	run, err := c.RunNow(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, c, job.ID, run.ID)
	for _, jr := range env.Fake.StartedJobs() {
		if jr.RunID == run.ID && (len(jr.Mounts) != 1 || !jr.Mounts[0].ReadOnly || jr.Mounts[0].Path != "/in") {
			t.Errorf("job mounts %+v", jr.Mounts)
		}
	}

	// Outputs: what it's attached to and the space used (VOL-06).
	waitFor(t, 10*time.Second, func() bool {
		got, _ := c.GetResource(ctx, vol.ID)
		return got.Outputs["attachedTo"] == "app/web,job/thumbs"
	})

	// Refusals: in use (VOL-07), a disk mounted in an app, a missing volume, a filesystem with a size.
	if _, err := c.DeleteResource(ctx, vol.ID, nil); apiCode(err) != api.CodeResourceInUse {
		t.Errorf("deleting a mounted volume: %v", err)
	}
	disk, err := c.CreateResource(ctx, api.KindVolume, "data", api.VolumeSpec{Type: api.VolumeDisk, SizeGiB: 5})
	if err != nil {
		t.Fatal(err)
	}
	waitReadyWithin(t, c, disk.ID, 10*time.Second)
	for name, mounts := range map[string][]api.VolumeMount{
		"disk":    {{Volume: "data", Path: "/data"}},
		"missing": {{Volume: "nowhere", Path: "/data"}},
	} {
		if _, err := c.CreateResource(ctx, api.KindApp, "bad-"+name, api.AppSpec{Image: "nginx:1.27", Volumes: mounts}); apiCode(err) != api.CodeValidationFailed {
			t.Errorf("%s mount: %v", name, err)
		}
	}
	if _, err := c.CreateResource(ctx, api.KindVolume, "sized", api.VolumeSpec{Type: api.VolumeFilesystem, SizeGiB: 5}); apiCode(err) != api.CodeValidationFailed {
		t.Errorf("a filesystem with a size: %v", err)
	}

	// Unmounted, it can go, and its data goes with it (CP-19).
	if _, err := c.UpdateResource(ctx, app.ID, map[string]any{"volumes": []any{}}, nil); err != nil {
		t.Fatal(err)
	}
	waitReadyWithin(t, c, app.ID, 10*time.Second)
	if _, err := c.DeleteResource(ctx, job.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.WaitGone(ctx, job.ID, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DeleteResource(ctx, vol.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.WaitGone(ctx, vol.ID, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if env.Fake.VolumeExists(vol.ID) {
		t.Error("the engine volume outlived its resource")
	}
}

func TestFilesystemSnapshotsRestoreIntoNewVolumes(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()

	vol, _ := c.CreateResource(ctx, api.KindVolume, "uploads", api.VolumeSpec{Type: api.VolumeFilesystem})
	waitReadyWithin(t, c, vol.ID, 10*time.Second)
	env.Fake.WriteFile(vol.ID, "a.txt", "one")
	env.Fake.WriteFile(vol.ID, "dir/b.txt", "two")

	snap, err := c.CreateResource(ctx, api.KindSnapshot, "before", api.SnapshotSpec{Source: "volume/uploads"})
	if err != nil {
		t.Fatal(err)
	}
	got := waitReadyWithin(t, c, snap.ID, 10*time.Second)
	if got.Outputs["type"] != api.VolumeFilesystem || got.Outputs["inUse"] != false || got.Outputs["sizeBytes"].(float64) <= 0 || got.Outputs["takenAt"] == nil {
		t.Errorf("snapshot outputs %v", got.Outputs)
	}
	file := filepath.Join(env.DataDir, "snapshots", snap.ID+".tar")
	if st, err := os.Stat(file); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot file: %v %v", st, err)
	}

	// A change to the source after the snapshot doesn't reach it; a new volume holds the old files.
	env.Fake.WriteFile(vol.ID, "a.txt", "changed")
	restored, err := c.CreateResource(ctx, api.KindVolume, "restored", api.VolumeSpec{Type: api.VolumeFilesystem, Snapshot: "before"})
	if err != nil {
		t.Fatal(err)
	}
	r := waitReadyWithin(t, c, restored.ID, 10*time.Second)
	if files := env.Fake.Files(restored.ID); files["a.txt"] != "one" || files["dir/b.txt"] != "two" || r.Outputs["filledFrom"] != "before" {
		t.Errorf("restored files %v outputs %v", files, r.Outputs)
	}

	// The copy is independent: the snapshot and its source can both go (SNP-02, SNP-06).
	if _, err := c.DeleteResource(ctx, snap.ID, nil); err != nil {
		t.Fatalf("deleting a snapshot a filled volume came from: %v", err)
	}
	if err := c.WaitGone(ctx, snap.ID, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("snapshot file left behind: %v", err)
	}
	if _, err := c.UpdateResource(ctx, restored.ID, map[string]any{"snapshot": "before"}, nil); err != nil {
		t.Errorf("a no-op update of a filled volume after its snapshot went: %v", err)
	}

	// Rules: immutable, types must match, the source must exist, the snapshot must be ready.
	snap2, _ := c.CreateResource(ctx, api.KindSnapshot, "again", api.SnapshotSpec{Source: "volume/uploads"})
	waitReadyWithin(t, c, snap2.ID, 10*time.Second)
	if _, err := c.UpdateResource(ctx, snap2.ID, map[string]any{"source": "volume/restored"}, nil); apiCode(err) != api.CodeImmutableField {
		t.Errorf("changing a snapshot's source: %v", err)
	}
	if _, err := c.CreateResource(ctx, api.KindVolume, "wrong-type", api.VolumeSpec{Type: api.VolumeDisk, Snapshot: "again"}); apiCode(err) != api.CodeValidationFailed {
		t.Errorf("a disk from a filesystem snapshot: %v", err)
	}
	if _, err := c.CreateResource(ctx, api.KindSnapshot, "orphan", api.SnapshotSpec{Source: "volume/nowhere"}); apiCode(err) != api.CodeValidationFailed {
		t.Errorf("a snapshot of nothing: %v", err)
	}
}

func TestDiskVolumesGrowAndCopyThroughSnapshots(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()

	disk, err := c.CreateResource(ctx, api.KindVolume, "data", api.VolumeSpec{Type: api.VolumeDisk, SizeGiB: 50})
	if err != nil {
		t.Fatal(err)
	}
	got := waitReadyWithin(t, c, disk.ID, 10*time.Second)
	if size, _, ok := env.FakeHypervisor.Disk(disk.ID); !ok || size != 50 || got.Outputs["sizeGiB"] != float64(50) {
		t.Fatalf("disk %d %v outputs %v", size, ok, got.Outputs)
	}
	if _, err := c.UpdateResource(ctx, disk.ID, map[string]any{"sizeGiB": 60}, nil); err != nil {
		t.Fatal(err)
	}
	waitReadyWithin(t, c, disk.ID, 10*time.Second)
	if size, _, _ := env.FakeHypervisor.Disk(disk.ID); size != 60 {
		t.Errorf("grown to %d GiB, want 60", size)
	}
	_, err = c.UpdateResource(ctx, disk.ID, map[string]any{"sizeGiB": 40}, nil)
	var ae *api.Error
	if apiCode(err) != api.CodeValidationFailed || !asAPIError(err, &ae) || ae.Details[0].Code != api.FieldGrowOnly {
		t.Errorf("shrinking a disk: %v", err)
	}

	env.FakeHypervisor.WriteDisk(disk.ID, "ext4 with files")
	snap, _ := c.CreateResource(ctx, api.KindSnapshot, "data-1", api.SnapshotSpec{Source: "volume/data"})
	s := waitReadyWithin(t, c, snap.ID, 10*time.Second)
	if s.Outputs["type"] != api.VolumeDisk || s.Outputs["sizeGiB"] != float64(60) {
		t.Errorf("disk snapshot outputs %v", s.Outputs)
	}
	if _, err := os.Stat(filepath.Join(env.DataDir, "snapshots", snap.ID+".qcow2")); err != nil {
		t.Errorf("disk snapshot file: %v", err)
	}
	if _, err := c.CreateResource(ctx, api.KindVolume, "small", api.VolumeSpec{Type: api.VolumeDisk, SizeGiB: 10, Snapshot: "data-1"}); apiCode(err) != api.CodeValidationFailed {
		t.Errorf("a disk smaller than its snapshot: %v", err)
	}
	cp, _ := c.CreateResource(ctx, api.KindVolume, "data-copy", api.VolumeSpec{Type: api.VolumeDisk, SizeGiB: 80, Snapshot: "data-1"})
	waitReadyWithin(t, c, cp.ID, 10*time.Second)
	if size, contents, _ := env.FakeHypervisor.Disk(cp.ID); size != 80 || contents != "ext4 with files" {
		t.Errorf("copy %d GiB %q", size, contents)
	}

	if _, err := c.DeleteResource(ctx, disk.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.WaitGone(ctx, disk.ID, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := env.FakeHypervisor.Disk(disk.ID); ok {
		t.Error("the disk file outlived its volume")
	}
}

func TestDisksWaitForTheHypervisorAndDriftRecreatesFilesystems(t *testing.T) {
	env := testenv.Start(t, testenv.WithConfig(func(c *server.Config) { c.DriftEvery = 100 * time.Millisecond }))
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()

	env.FakeHypervisor.SetSupport(false, "Install QEMU: brew install qemu")
	disk, _ := c.CreateResource(ctx, api.KindVolume, "data", api.VolumeSpec{Type: api.VolumeDisk})
	waitFor(t, 10*time.Second, func() bool {
		got, _ := c.GetResource(ctx, disk.ID)
		return got.Status.Message == "Install QEMU: brew install qemu"
	})
	if got, _ := c.GetResource(ctx, disk.ID); got.Status.Phase == api.PhaseFailed {
		t.Fatalf("a missing QEMU must not fail the volume: %+v", got.Status)
	}
	env.FakeHypervisor.SetSupport(true, "")
	if _, err := c.Action(ctx, disk.ID, api.ActionReconcile); err != nil {
		t.Fatal(err)
	}
	waitReadyWithin(t, c, disk.ID, 45*time.Second)

	vol, _ := c.CreateResource(ctx, api.KindVolume, "uploads", api.VolumeSpec{Type: api.VolumeFilesystem})
	waitReadyWithin(t, c, vol.ID, 10*time.Second)
	env.Fake.KillVolume(vol.ID)
	waitFor(t, 10*time.Second, func() bool { return env.Fake.VolumeExists(vol.ID) })
}

func asAPIError(err error, out **api.Error) bool { return errors.As(err, out) }
