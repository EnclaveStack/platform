package reconciler_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/enclavestack/platform/pkg/api"
)

// Filesystem volume backups (ADR-059, L2-016 VBK-01 – VBK-03).
func TestVolumeBackupsAndRestores(t *testing.T) {
	b := startBackupEnv(t)
	c, ctx, env := b.c, context.Background(), b.env
	vol := create(t, c, api.KindVolume, "uploads", api.VolumeSpec{Type: api.VolumeFilesystem})
	env.Fake.WriteFile(vol.ID, "a.txt", "one")
	bk, err := c.CreateResource(ctx, api.KindBackup, "uploads-nightly", api.BackupSpec{Source: api.BackupSource{Volume: "uploads"},
		Destination: api.BackupDestination{Bucket: "backups"}})
	if err != nil {
		t.Fatal(err)
	}
	bk = waitReadyWithin(t, c, bk.ID, 10*time.Second)
	run := runOnce(t, c, bk.ID)
	if run.Status != api.RunSucceeded || run.Artifact == nil || !strings.HasSuffix(run.Artifact.Key, ".tar.gz") ||
		!strings.Contains(run.Message, "Backed up volume uploads (filesystem volume)") {
		t.Fatalf("run %+v %+v", run, run.Artifact)
	}

	// Into a new volume.
	cp, err := c.Restore(ctx, bk.ID, api.RestoreRequest{RunID: run.ID, Mode: api.RestoreNew, Name: "uploads-copy"})
	if err != nil {
		t.Fatal(err)
	}
	cp = waitReadyWithin(t, c, cp.ID, 10*time.Second)
	if env.Fake.Files(cp.ID)["a.txt"] != "one" || cp.Outputs["restoredFrom"] == nil || cp.Outputs["pendingRestore"] != nil {
		t.Errorf("copy: files %v outputs %v", env.Fake.Files(cp.ID), cp.Outputs)
	}

	// Over the source: a safety backup first, changed files come back, new ones stay.
	env.Fake.WriteFile(vol.ID, "a.txt", "changed")
	env.Fake.WriteFile(vol.ID, "b.txt", "new")
	if _, err := c.Restore(ctx, bk.ID, api.RestoreRequest{RunID: run.ID, Mode: api.RestoreReplace, ConfirmName: "uploads"}); err != nil {
		t.Fatal(err)
	}
	waitReadyWithin(t, c, vol.ID, 10*time.Second)
	if f := env.Fake.Files(vol.ID); f["a.txt"] != "one" || f["b.txt"] != "new" {
		t.Errorf("replace: %v", f)
	}
	runs, _ := c.ListRuns(ctx, bk.ID, 10, "")
	if len(runs.Items) != 2 || runs.Items[0].Trigger != api.TriggerPreRestore || runs.Items[0].Status != api.RunSucceeded {
		t.Errorf("runs %+v", runs.Items)
	}

	// A disk is backed up with snapshots, not here.
	if _, err := c.CreateResource(ctx, api.KindVolume, "disk-one", api.VolumeSpec{Type: api.VolumeDisk, SizeGiB: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateResource(ctx, api.KindBackup, "disk-nightly", api.BackupSpec{Source: api.BackupSource{Volume: "disk-one"},
		Destination: api.BackupDestination{Bucket: "backups"}}); apiCode(err) != api.CodeValidationFailed {
		t.Errorf("disk source: %v", err)
	}
}
