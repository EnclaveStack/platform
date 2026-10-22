package reconciler_test

import (
	"context"
	"io"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/backup"
	"github.com/enclavestack/platform/internal/engine/fake"
	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// Backups and restore (ADR-018, L2-005 BAK-01 – BAK-09) through the real API on the fake engine, with
// in-memory backup storage.

type backupEnv struct {
	env    *testenv.Env
	c      *client.Client
	db     api.Resource
	bucket api.Resource
}

func startBackupEnv(t *testing.T) backupEnv {
	t.Helper()
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	db, _ := c.CreateResource(ctx, api.KindDatabase, "orders", api.DatabaseSpec{})
	bucket, _ := c.CreateResource(ctx, api.KindBucket, "backups", api.BucketSpec{})
	db = waitReadyWithin(t, c, db.ID, 10*time.Second)
	bucket = waitReadyWithin(t, c, bucket.ID, 10*time.Second)
	env.Fake.SetData(db.ID, "rows=1000")
	return backupEnv{env, c, db, bucket}
}

func (b backupEnv) target() backup.Target {
	return backup.Target{Endpoint: b.bucket.Outputs["endpoint"].(string), Bucket: "backups"}
}

func (b backupEnv) object(t *testing.T, key string) string {
	t.Helper()
	st, _ := b.env.Backups.Open(context.Background(), b.target())
	r, err := st.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("object %s: %v", key, err)
	}
	defer r.Close()
	body, _ := io.ReadAll(r)
	return string(body)
}

func runOnce(t *testing.T, c *client.Client, id string) api.RunDetail {
	t.Helper()
	run, err := c.RunNow(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return waitRun(t, c, id, run.ID)
}

var objectKey = regexp.MustCompile(`^nightly/orders/\d{8}T\d{6}\.\d{3}Z-run_[a-z2-7]{16}\.pgdump$`)

func TestBackupsStreamToABucketRetainAndRestore(t *testing.T) {
	b := startBackupEnv(t)
	c, ctx := b.c, context.Background()
	bk, err := c.CreateResource(ctx, api.KindBackup, "orders-nightly", api.BackupSpec{Source: api.BackupSource{Database: "orders"},
		Destination: api.BackupDestination{Bucket: "backups"}, Prefix: "nightly", Keep: 2})
	if err != nil {
		t.Fatal(err)
	}
	bk = waitReadyWithin(t, c, bk.ID, 10*time.Second)
	if bk.Outputs["nextRunAt"] == nil {
		t.Errorf("next run %v", bk.Outputs)
	}

	first := runOnce(t, c, bk.ID)
	if first.Status != api.RunSucceeded || first.Artifact == nil || !objectKey.MatchString(first.Artifact.Key) || len(first.Artifact.SHA256) != 64 ||
		!strings.Contains(first.Message, "Backed up database orders") {
		t.Fatalf("run %+v %+v", first, first.Artifact)
	}
	if got := b.object(t, first.Artifact.Key); got != "FAKEDUMP\nrows=1000" {
		t.Errorf("stored %q", got)
	}
	for i := 0; i < 3; i++ {
		runOnce(t, c, bk.ID)
	}
	if objs := b.env.Backups.Objects(b.target()); len(objs) != 2 {
		t.Errorf("keep 2: %d objects left: %v", len(objs), objs)
	}
	latest, _ := c.ListRuns(ctx, bk.ID, 1, "")
	run := latest.Items[0]
	got, _ := c.GetResource(ctx, bk.ID)
	if got.Outputs["lastSuccessAt"] == nil || got.Outputs["lastRunStatus"] != api.RunSucceeded {
		t.Errorf("outputs %v", got.Outputs)
	}

	// Restore into a new database: the source is untouched (BAK-06).
	copyRes, err := c.Restore(ctx, bk.ID, api.RestoreRequest{RunID: run.ID, Mode: api.RestoreNew, Name: "orders-copy"})
	if err != nil {
		t.Fatal(err)
	}
	copyRes = waitReadyWithin(t, c, copyRes.ID, 10*time.Second)
	if b.env.Fake.Data(copyRes.ID) != "rows=1000" || !strings.Contains(copyRes.Outputs["restoredFrom"].(string), "orders-nightly run "+run.ID) || copyRes.Outputs["pendingRestore"] != nil {
		t.Errorf("restored copy: data %q outputs %v", b.env.Fake.Data(copyRes.ID), copyRes.Outputs)
	}
	if b.env.Fake.Data(b.db.ID) != "rows=1000" {
		t.Error("restoring into a new database touched the source")
	}

	// Replace: the name must be typed; a safety backup of the current data comes first (BAK-07).
	b.env.Fake.SetData(b.db.ID, "rows=990")
	if _, err := c.Restore(ctx, bk.ID, api.RestoreRequest{RunID: run.ID, Mode: api.RestoreReplace, ConfirmName: "order"}); apiCode(err) != api.CodeValidationFailed {
		t.Errorf("wrong confirm name: %v", err)
	}
	src, err := c.Restore(ctx, bk.ID, api.RestoreRequest{RunID: run.ID, Mode: api.RestoreReplace, ConfirmName: "orders"})
	if err != nil || src.ID != b.db.ID || src.Status.Phase != api.PhaseUpdating {
		t.Fatalf("replace: %+v %v", src.Status, err)
	}
	waitReadyWithin(t, c, src.ID, 10*time.Second)
	if b.env.Fake.Data(b.db.ID) != "rows=1000" {
		t.Errorf("replace didn't restore: %q", b.env.Fake.Data(b.db.ID))
	}
	runs, _ := c.ListRuns(ctx, bk.ID, 10, "")
	var safety *api.Run
	for i := range runs.Items {
		if runs.Items[i].Trigger == api.TriggerPreRestore {
			safety = &runs.Items[i]
		}
	}
	if safety == nil || safety.Status != api.RunSucceeded || b.object(t, safety.Artifact.Key) != "FAKEDUMP\nrows=990" {
		t.Errorf("safety backup %+v", safety)
	}
	if ev := eventTypes(t, c, b.db.ID); ev["restore_requested"] != 1 || ev["restored"] != 1 {
		t.Errorf("events %v", ev)
	}

	// A damaged object is refused before anything changes.
	b.env.Backups.Corrupt(b.target(), run.Artifact.Key)
	bad, _ := c.Restore(ctx, bk.ID, api.RestoreRequest{RunID: run.ID, Mode: api.RestoreNew, Name: "orders-bad"})
	failed := waitFailed(t, c, bad.ID)
	if !strings.Contains(failed.Status.Message, "checksum") || !strings.Contains(failed.Status.Message, "Delete orders-bad and restore from another run") {
		t.Errorf("corrupt object: %q", failed.Status.Message)
	}

	// Rules: in-use sources and buckets, immutable source, unrestorable runs, backups have no logs.
	if _, err := c.DeleteResource(ctx, b.db.ID, nil); apiCode(err) != api.CodeResourceInUse {
		t.Errorf("delete backed-up database: %v", err)
	}
	if _, err := c.DeleteResource(ctx, b.bucket.ID, nil); apiCode(err) != api.CodeResourceInUse {
		t.Errorf("delete destination bucket: %v", err)
	}
	if _, err := c.UpdateResource(ctx, bk.ID, map[string]any{"source": map[string]any{"cache": "sessions"}}, nil); apiCode(err) != api.CodeImmutableField {
		t.Errorf("change source: %v", err)
	}
	if _, err := c.Restore(ctx, bk.ID, api.RestoreRequest{RunID: "run_nope", Mode: api.RestoreNew, Name: "x-copy"}); apiCode(err) != api.CodeRunNotRestorable {
		t.Errorf("unknown run: %v", err)
	}
	if _, err := c.Logs(ctx, bk.ID, 10); apiCode(err) != api.CodeActionNotSupported {
		t.Errorf("backup logs: %v", err)
	}

	// Deleting the backup keeps its stored objects (BAK-08) and frees its source.
	stored := b.env.Backups.Objects(b.target())
	if len(stored) == 0 {
		t.Fatal("no objects before delete")
	}
	if _, err := c.DeleteResource(ctx, bk.ID, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		_, err := c.GetResource(ctx, bk.ID)
		return apiCode(err) == api.CodeNotFound
	})
	if after := b.env.Backups.Objects(b.target()); !slices.Equal(after, stored) {
		t.Errorf("deleting the backup changed its objects: %v → %v", stored, after)
	}
	if _, err := c.DeleteResource(ctx, b.db.ID, nil); err != nil {
		t.Errorf("source still held after its backup was deleted: %v", err)
	}
}

func TestBackupFailuresAreReportedAndThreeInARowFailIt(t *testing.T) {
	b := startBackupEnv(t)
	c, ctx := b.c, context.Background()
	if _, err := c.CreateResource(ctx, api.KindSecret, "s3-creds", api.SecretSpec{Value: `{"accessKey":"AKIA","secretKey":"s3cr3t"}`}); err != nil {
		t.Fatal(err)
	}
	ext := api.BackupDestination{S3: &api.S3Destination{Endpoint: "https://s3.example.com", Bucket: "acme-backups", CredentialsSecret: "s3-creds"}}
	bk, err := c.CreateResource(ctx, api.KindBackup, "offsite", api.BackupSpec{Source: api.BackupSource{Database: "orders"}, Destination: ext})
	if err != nil {
		t.Fatal(err)
	}
	waitReadyWithin(t, c, bk.ID, 10*time.Second)
	b.env.Backups.Refuse(backup.Target{Endpoint: "https://s3.example.com", Bucket: "acme-backups"}, "the destination rejected the credentials (AccessDenied)")
	if r := runOnce(t, c, bk.ID); r.Status != api.RunFailed || !strings.Contains(r.Message, "rejected the credentials") || strings.Contains(r.Message, "s3cr3t") {
		t.Errorf("refused upload: %+v", r)
	}
	b.env.Backups.Refuse(backup.Target{Endpoint: "https://s3.example.com", Bucket: "acme-backups"}, "")
	b.env.Fake.SetFault("orders", fake.Fault{DumpFails: true})
	for i := 0; i < 2; i++ {
		if r := runOnce(t, c, bk.ID); r.Status != api.RunFailed || !strings.Contains(r.Message, "dump failed") {
			t.Errorf("dump failure: %+v", r)
		}
	}
	var failed api.Resource
	waitFor(t, 10*time.Second, func() bool {
		failed, _ = c.GetResource(ctx, bk.ID)
		return failed.Status.Phase == api.PhaseFailed
	})
	if !strings.Contains(failed.Status.Message, "The last 3 runs failed") {
		t.Errorf("three failures: %q", failed.Status.Message)
	}
	if _, err := c.RunNow(ctx, bk.ID); apiCode(err) != api.CodeActionNotSupported {
		t.Errorf("run on a failed backup: %v", err)
	}
	b.env.Fake.SetFault("orders", fake.Fault{})
	if _, err := c.Action(ctx, bk.ID, api.ActionReconcile); err != nil {
		t.Fatal(err)
	}
	waitReadyWithin(t, c, bk.ID, 10*time.Second)
	if r := runOnce(t, c, bk.ID); r.Status != api.RunSucceeded || !strings.HasPrefix(r.Artifact.Destination, "https://s3.example.com/acme-backups") {
		t.Errorf("after fixing: %+v %+v", r, r.Artifact)
	}

	for name, spec := range map[string]map[string]any{
		"no-source":    {"destination": map[string]any{"bucket": "backups"}},
		"two-sources":  {"source": map[string]any{"database": "orders", "cache": "x"}, "destination": map[string]any{"bucket": "backups"}},
		"no-dest":      {"source": map[string]any{"database": "orders"}},
		"plain-http":   {"source": map[string]any{"database": "orders"}, "destination": map[string]any{"s3": map[string]any{"endpoint": "http://s3.example.com", "bucket": "acme-backups", "credentialsSecret": "s3-creds"}}},
		"bad-prefix":   {"source": map[string]any{"database": "orders"}, "destination": map[string]any{"bucket": "backups"}, "prefix": "../up"},
		"missing-db":   {"source": map[string]any{"database": "nope"}, "destination": map[string]any{"bucket": "backups"}},
		"missing-cred": {"source": map[string]any{"database": "orders"}, "destination": map[string]any{"s3": map[string]any{"endpoint": "https://s3.example.com", "bucket": "acme-backups", "credentialsSecret": "nope"}}},
		"keep":         {"source": map[string]any{"database": "orders"}, "destination": map[string]any{"bucket": "backups"}, "keep": 400},
	} {
		if _, err := c.CreateResource(ctx, api.KindBackup, "bad-"+name, spec); apiCode(err) != api.CodeValidationFailed {
			t.Errorf("%s: want validation error, got %v", name, err)
		}
	}
}

// Two backups of one database into one bucket and prefix keep their own counts, and a run whose object
// retention removed says so and can't be restored (BAK-04).
func TestRetentionIsPerBackupAndForgetsRemovedRuns(t *testing.T) {
	b := startBackupEnv(t)
	c, ctx := b.c, context.Background()
	spec := func(keep int) api.BackupSpec {
		return api.BackupSpec{Source: api.BackupSource{Database: "orders"}, Destination: api.BackupDestination{Bucket: "backups"}, Prefix: "shared", Keep: keep}
	}
	hourly, err := c.CreateResource(ctx, api.KindBackup, "orders-hourly", spec(1))
	if err != nil {
		t.Fatal(err)
	}
	daily, err := c.CreateResource(ctx, api.KindBackup, "orders-daily", spec(3))
	if err != nil {
		t.Fatal(err)
	}
	waitReadyWithin(t, c, hourly.ID, 10*time.Second)
	waitReadyWithin(t, c, daily.ID, 10*time.Second)
	var dailyRuns []api.RunDetail
	for i := 0; i < 3; i++ {
		dailyRuns = append(dailyRuns, runOnce(t, c, daily.ID))
	}
	oldHourly := runOnce(t, c, hourly.ID)
	runOnce(t, c, hourly.ID) // keep 1: removes oldHourly's object, and only that
	objs := b.env.Backups.Objects(b.target())
	for _, r := range dailyRuns {
		if !slices.Contains(objs, r.Artifact.Key) {
			t.Errorf("the hourly backup's retention deleted the daily backup's %s; left %v", r.Artifact.Key, objs)
		}
	}
	if slices.Contains(objs, oldHourly.Artifact.Key) || len(objs) != 4 {
		t.Errorf("hourly keep 1: objects %v", objs)
	}

	expired, err := c.GetRun(ctx, hourly.ID, oldHourly.ID)
	if err != nil || expired.Artifact != nil || !strings.Contains(expired.Message, "Removed by retention.") {
		t.Errorf("expired run %+v %v", expired, err)
	}
	_, err = c.Restore(ctx, hourly.ID, api.RestoreRequest{RunID: oldHourly.ID, Mode: api.RestoreNew, Name: "orders-old"})
	if apiCode(err) != api.CodeRunNotRestorable || !strings.Contains(err.Error(), "removed by retention") {
		t.Errorf("restoring an expired run: %v", err)
	}
	if _, err := c.GetResource(ctx, "orders-old"); err == nil {
		t.Error("a database was created for an expired run")
	}
}

// Replacing from an older run with a small keep: the safety backup's retention must not delete the run
// being restored before the restore has read it.
func TestSafetyBackupNeverEvictsTheRunBeingRestored(t *testing.T) {
	b := startBackupEnv(t)
	c, ctx := b.c, context.Background()
	bk, err := c.CreateResource(ctx, api.KindBackup, "orders-two", api.BackupSpec{Source: api.BackupSource{Database: "orders"},
		Destination: api.BackupDestination{Bucket: "backups"}, Keep: 2})
	if err != nil {
		t.Fatal(err)
	}
	waitReadyWithin(t, c, bk.ID, 10*time.Second)
	older := runOnce(t, c, bk.ID) // rows=1000
	b.env.Fake.SetData(b.db.ID, "rows=2000")
	runOnce(t, c, bk.ID)
	b.env.Fake.SetData(b.db.ID, "rows=damaged")

	if _, err := c.Restore(ctx, bk.ID, api.RestoreRequest{RunID: older.ID, Mode: api.RestoreReplace, ConfirmName: "orders"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		r, _ := c.GetResource(ctx, b.db.ID)
		return r.Status.Phase == api.PhaseReady || r.Status.Phase == api.PhaseFailed
	})
	got, _ := c.GetResource(ctx, b.db.ID)
	if got.Status.Phase != api.PhaseReady || b.env.Fake.Data(b.db.ID) != "rows=1000" {
		t.Fatalf("replace from the older run: phase %s %q, data %q", got.Status.Phase, got.Status.Message, b.env.Fake.Data(b.db.ID))
	}
	// Once restored, the next run trims back to keep.
	runOnce(t, c, bk.ID)
	if objs := b.env.Backups.Objects(b.target()); len(objs) != 2 {
		t.Errorf("after the next run: %v", objs)
	}
}

// A replace that fails before touching the data (here: a damaged object) leaves the source recoverable:
// Retry brings it back as it was instead of attempting the same restore again.
func TestFailedReplaceLeavesTheSourceRecoverable(t *testing.T) {
	b := startBackupEnv(t)
	c, ctx := b.c, context.Background()
	bk, err := c.CreateResource(ctx, api.KindBackup, "orders-bk", api.BackupSpec{Source: api.BackupSource{Database: "orders"}, Destination: api.BackupDestination{Bucket: "backups"}})
	if err != nil {
		t.Fatal(err)
	}
	waitReadyWithin(t, c, bk.ID, 10*time.Second)
	run := runOnce(t, c, bk.ID)
	b.env.Fake.SetData(b.db.ID, "rows=today")
	b.env.Backups.Corrupt(b.target(), run.Artifact.Key)
	if _, err := c.Restore(ctx, bk.ID, api.RestoreRequest{RunID: run.ID, Mode: api.RestoreReplace, ConfirmName: "orders"}); err != nil {
		t.Fatal(err)
	}
	failed := waitFailed(t, c, b.db.ID)
	if !strings.Contains(failed.Status.Message, "checksum") || !strings.Contains(failed.Status.Message, "Nothing was changed; use Retry") || failed.Outputs["pendingRestore"] != nil {
		t.Errorf("failed replace: %q outputs %v", failed.Status.Message, failed.Outputs)
	}
	if got := b.env.Fake.Data(b.db.ID); got != "rows=today" {
		t.Errorf("data changed by a failed restore: %q", got)
	}
	if _, err := c.Action(ctx, b.db.ID, api.ActionReconcile); err != nil {
		t.Fatal(err)
	}
	back := waitReadyWithin(t, c, b.db.ID, 10*time.Second)
	if b.env.Fake.Data(b.db.ID) != "rows=today" || back.Outputs["restoredFrom"] != nil {
		t.Errorf("after Retry: data %q outputs %v", b.env.Fake.Data(b.db.ID), back.Outputs)
	}
}
