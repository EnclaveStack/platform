package reconciler

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/backup"
	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// Backups and restores (ADR-018, L2-005 BAK-01 – BAK-09).

// BackupFailureLimit failed runs in a row put a backup into failed (BAK-09).
const BackupFailureLimit = 3

// OutputPendingRestore marks a database or cache that is to be restored; set by the service, cleared by
// the reconciler once the restore is done. OutputRestoredFrom records what it was restored from.
const (
	OutputPendingRestore = "pendingRestore"
	OutputRestoredFrom   = "restoredFrom"
)

// PendingRestore is the marker's shape.
type PendingRestore struct {
	BackupID   string `json:"backupId"`
	RunID      string `json:"runId"`
	Replace    bool   `json:"replace"`
	SafetyDone bool   `json:"safetyDone,omitempty"`
}

func pendingRestoreOf(outputs map[string]any) *PendingRestore {
	raw, ok := outputs[OutputPendingRestore]
	if !ok || raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var pr PendingRestore
	if json.Unmarshal(b, &pr) != nil || pr.BackupID == "" || pr.RunID == "" {
		return nil
	}
	return &pr
}

func backupSpecOf(res *domain.Resource) (api.BackupSpec, error) {
	raw, _, err := api.NormalizeSpec(api.KindBackup, res.Spec)
	if err != nil {
		return api.BackupSpec{}, err
	}
	return raw.(api.BackupSpec), nil
}

// backupSource returns the database, cache or filesystem volume a backup copies.
func (r *Reconciler) backupSource(ctx context.Context, project string, s api.BackupSpec) (*domain.Resource, error) {
	kind, name := s.Source.Kind(), s.Source.Name()
	src, err := r.Store.GetResourceByName(ctx, project, kind, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, engine.Permanent(fmt.Errorf("the %s %s no longer exists", kind, name))
	}
	if err == nil && kind == api.KindVolume && volumeType(src) == api.VolumeDisk {
		return nil, engine.Permanent(fmt.Errorf("the volume %s is a disk: back it up with a snapshot", name))
	}
	return src, err
}

// dumpTo writes a source's backup into w: a database's or cache's dump, or a filesystem volume's files
// as a gzipped tar (ADR-059).
func (r *Reconciler) dumpTo(ctx context.Context, src *domain.Resource, in engine.EnsureInput, w io.Writer) (engine.DumpInfo, error) {
	if src.Kind != api.KindVolume {
		return r.Driver.Dump(ctx, in, w)
	}
	gz := gzip.NewWriter(w)
	if err := r.Driver.SnapshotVolume(ctx, src, gz); err != nil {
		return engine.DumpInfo{}, err
	}
	return engine.DumpInfo{Ext: engine.DumpExt(in.Spec), Engine: "filesystem volume"}, gz.Close()
}

// restoreFrom restores a backup's file into the target: a dump through the engine, or a volume's archive
// unpacked over its files (files added since the backup stay, ADR-059).
func (r *Reconciler) restoreFrom(ctx context.Context, target *domain.Resource, in engine.EnsureInput, f io.Reader) error {
	if target.Kind != api.KindVolume {
		return r.Driver.Restore(ctx, in, f)
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		return engine.Permanent(fmt.Errorf("the volume backup isn't a gzipped archive: %w", err))
	}
	defer gz.Close()
	return r.Driver.RestoreVolume(ctx, target, gz)
}

// backupTarget resolves where a backup's objects go, with credentials (never logged).
func (r *Reconciler) backupTarget(ctx context.Context, project string, s api.BackupSpec) (backup.Target, error) {
	if d := s.Destination.S3; d != nil {
		raw, err := r.secretValue(ctx, project, d.CredentialsSecret)
		if err != nil {
			return backup.Target{}, err
		}
		var c struct{ AccessKey, SecretKey string }
		if err := json.Unmarshal([]byte(raw), &c); err != nil || c.AccessKey == "" || c.SecretKey == "" {
			return backup.Target{}, engine.Permanent(fmt.Errorf(`the secret %s must hold JSON {"accessKey": "…", "secretKey": "…"}`, d.CredentialsSecret))
		}
		return backup.Target{Endpoint: d.Endpoint, Region: d.Region, Bucket: d.Bucket, AccessKey: c.AccessKey, SecretKey: c.SecretKey}, nil
	}
	return r.bucketTarget(ctx, project, s.Destination.Bucket)
}

// bucketTarget reaches a EnclaveStack bucket from the machine, with the object store's credentials (backups,
// transcoders).
func (r *Reconciler) bucketTarget(ctx context.Context, project, name string) (backup.Target, error) {
	b, err := r.Store.GetResourceByName(ctx, project, api.KindBucket, name)
	if errors.Is(err, store.ErrNotFound) {
		return backup.Target{}, engine.Permanent(fmt.Errorf("the bucket %s no longer exists", name))
	}
	if err != nil {
		return backup.Target{}, err
	}
	endpoint, _ := b.Outputs["endpoint"].(string)
	if b.Phase != api.PhaseReady || endpoint == "" {
		return backup.Target{}, fmt.Errorf("the bucket %s isn't ready", b.Name)
	}
	os, err := r.objectStore(ctx)
	if err != nil {
		return backup.Target{}, err
	}
	return backup.Target{Endpoint: endpoint, Bucket: b.Name, AccessKey: os.AccessKey, SecretKey: os.SecretKey}, nil
}

// ensureBackup checks a backup's source and destination and schedules its next run (ADR-017).
func (r *Reconciler) ensureBackup(ctx context.Context, res *domain.Resource, report engine.Reporter) (engine.Outputs, error) {
	report.Step("check", api.StepActive)
	s, err := backupSpecOf(res)
	if err != nil {
		return nil, engine.Permanent(fmt.Errorf("stored spec unreadable: %w", err))
	}
	if _, err := r.backupSource(ctx, res.Project, s); err != nil {
		return nil, err
	}
	if _, err := r.backupTarget(ctx, res.Project, s); err != nil {
		return nil, err
	}
	report.Step("check", api.StepDone)
	report.Step("schedule", api.StepActive)
	sched, err := api.ParseSchedule(s.Schedule, s.TimeZone)
	if err != nil {
		return nil, engine.Permanent(fmt.Errorf("the schedule can't be used: %w", err))
	}
	if res.NextRunAt == nil || res.ObservedGeneration != res.Generation || res.Phase == api.PhaseProvisioning {
		next := sched.Next(r.Now())
		res.NextRunAt = &next
	}
	res.ConsecutiveFailures = 0
	out := engine.Outputs{"nextRunAt": res.NextRunAt.UTC().Format(time.RFC3339)}
	for _, k := range []string{"lastRunStatus", "lastRunAt", "lastSuccessAt"} {
		if v, ok := res.Outputs[k]; ok {
			out[k] = v
		}
	}
	report.Step("schedule", api.StepDone)
	return out, nil
}

// backupOnce dumps the source and uploads it, returning the artifact and a summary message. Errors are
// for people: they name what failed.
func (r *Reconciler) backupOnce(ctx context.Context, res *domain.Resource, s api.BackupSpec, run *domain.Run) (*api.Artifact, string, error) {
	src, err := r.backupSource(ctx, res.Project, s)
	if err != nil {
		return nil, "", err
	}
	if src.Phase != api.PhaseReady && src.Phase != api.PhaseUpdating {
		return nil, "", fmt.Errorf("the %s %s isn't ready", src.Kind, src.Name)
	}
	in, err := r.inputs(ctx, src)
	if err != nil {
		return nil, "", err
	}
	target, err := r.backupTarget(ctx, res.Project, s)
	if err != nil {
		return nil, "", err
	}
	st, err := r.Opener.Open(ctx, target)
	if err != nil {
		return nil, "", fmt.Errorf("the destination can't be reached: %w", err)
	}
	ext := engine.DumpExt(in.Spec)
	key := backup.Key(s.Prefix, src.Name, run.StartedAt, run.ID, ext)
	pr, pw := io.Pipe()
	type dumped struct {
		info engine.DumpInfo
		err  error
	}
	done := make(chan dumped, 1)
	go func() {
		info, err := r.dumpTo(ctx, src, in, pw)
		_ = pw.CloseWithError(err)
		done <- dumped{info, err}
	}()
	obj, putErr := st.Put(ctx, key, pr, map[string]string{"enclave-source": string(src.Kind) + "/" + src.Name, "enclave-run": run.ID})
	_ = pr.CloseWithError(putErr) // unblock the dump if the upload stopped first
	d := <-done
	switch {
	case d.err != nil:
		return nil, "", fmt.Errorf("the dump failed: %w", d.err)
	case putErr != nil:
		return nil, "", fmt.Errorf("the upload to %s failed: %w", target.Describe(), putErr)
	}
	art := &api.Artifact{Key: key, SizeBytes: obj.Size, SHA256: obj.SHA256, Destination: target.Describe()}
	msg := fmt.Sprintf("Backed up %s %s (%s): %s.", src.Kind, src.Name, d.info.Engine, api.ByteSize(obj.Size))
	// Retention counts only this backup's own objects; another backup of the same source into the same
	// place keeps its own (BAK-04).
	owned, err := r.Store.ArtifactRunIDs(ctx, res.ID)
	if err != nil {
		return nil, "", err
	}
	owned[run.ID] = true // this run's artifact is recorded when it finishes
	// Never delete what a restore is about to read: a safety backup before a replace would otherwise
	// evict the very run being restored when keep is small.
	restoring, err := r.Store.RestoringRuns(ctx, res.ID)
	if err != nil {
		return nil, "", err
	}
	for id := range restoring {
		delete(owned, id)
	}
	if deleted, err := backup.Retain(ctx, st, s.Prefix, src.Name, s.Keep, s.KeepDays, r.Now(), func(id string) bool { return owned[id] }); err != nil {
		r.event(ctx, res, "retention_failed", "Old backups couldn't be removed: "+truncate(err.Error()))
	} else if len(deleted) > 0 {
		msg += fmt.Sprintf(" Removed %d old backup%s.", len(deleted), plural(len(deleted)))
		ids := make([]string, len(deleted))
		for i, k := range deleted {
			ids[i] = backup.RunIDOf(k)
		}
		if err := r.Store.ForgetArtifacts(ctx, res.ID, ids); err != nil {
			r.event(ctx, res, "retention_failed", "Removed old backups but couldn't record it: "+truncate(err.Error()))
		}
	}
	return art, msg, nil
}

// runBackup executes one backup run (scheduled, manual or catch-up).
func (r *Reconciler) runBackup(ctx context.Context, res *domain.Resource, run *domain.Run, log *slog.Logger) {
	s, err := backupSpecOf(res)
	done := context.WithoutCancel(ctx)
	if err != nil {
		r.finish(done, res, run, api.RunFailed, nil, nil, "The backup's spec is unreadable.", log)
		return
	}
	tctx, cancel := context.WithTimeout(ctx, time.Duration(s.TimeoutSeconds)*time.Second)
	defer cancel()
	art, msg, err := r.backupOnce(tctx, res, s, run)
	switch {
	case ctx.Err() != nil:
		return // shutting down: the run stays running and is adopted (and retried) on the next start
	case errors.Is(err, engine.ErrUnavailable):
		return
	case tctx.Err() != nil:
		r.finish(done, res, run, api.RunTimedOut, nil, nil, fmt.Sprintf("Stopped after the timeout of %s.", time.Duration(s.TimeoutSeconds)*time.Second), log)
	case err != nil:
		r.finish(done, res, run, api.RunFailed, nil, nil, truncate(err.Error()), log)
	default:
		run.Artifact = art
		r.finish(done, res, run, api.RunSucceeded, nil, nil, msg, log)
	}
}

// afterBackupRun puts a backup into failed after BackupFailureLimit failures in a row (BAK-09), and
// records the last success.
func (r *Reconciler) afterBackupRun(ctx context.Context, res *domain.Resource, status string, failures int, log *slog.Logger) {
	if status == api.RunSucceeded {
		_ = r.Store.MergeOutputs(ctx, res.ID, map[string]any{"lastSuccessAt": r.Now().UTC().Format(time.RFC3339)})
		return
	}
	if failures < BackupFailureLimit {
		return
	}
	cur, err := r.Store.GetResource(ctx, res.ID)
	if err != nil || cur.Phase != api.PhaseReady {
		return
	}
	msg := fmt.Sprintf("The last %d runs failed. Fix the cause, then use Retry.", failures)
	if err := cur.SetPhase(api.PhaseFailed, msg, r.Now()); err != nil {
		log.Error("cannot mark the backup failed", "error", err)
		return
	}
	cur.UpdatedAt = r.Now()
	if ok, err := r.Store.SaveStatus(ctx, cur, cur.Generation); err == nil && ok {
		r.event(ctx, cur, "failed", msg)
		log.Error("backup failed repeatedly", "failures", failures)
	}
}

// restoreInto restores a database or cache from a backup run (BAK-06, BAK-07). For replace, a safety
// backup of the current data is taken first, and nothing changes if it fails.
func (r *Reconciler) restoreInto(ctx context.Context, target *domain.Resource, in engine.EnsureInput, pr *PendingRestore, report engine.Reporter) (string, error) {
	bres, err := r.Store.GetResource(ctx, pr.BackupID)
	if errors.Is(err, store.ErrNotFound) {
		return "", engine.Permanent(errors.New("the backup to restore from no longer exists"))
	}
	if err != nil {
		return "", err
	}
	s, err := backupSpecOf(bres)
	if err != nil {
		return "", engine.Permanent(err)
	}
	run, err := r.Store.GetRun(ctx, bres.ID, pr.RunID)
	if err != nil || run.Status != api.RunSucceeded || run.Artifact == nil {
		return "", engine.Permanent(fmt.Errorf("the backup run %s can't be restored: it didn't succeed or was removed", pr.RunID))
	}
	from := fmt.Sprintf("%s run %s (%s)", bres.Name, run.ID, run.StartedAt.UTC().Format("2 Jan 2006 15:04 UTC"))
	// A dump only restores into its own engine: the source may have been recreated with another (ADR-030).
	if want := "." + engine.DumpExt(in.Spec); !strings.HasSuffix(run.Artifact.Key, want) {
		return "", engine.Permanent(fmt.Errorf("the backup run %s holds a dump of another engine (%s), which can't be restored into %s", pr.RunID, run.Artifact.Key, target.Name))
	}
	if pr.Replace && !pr.SafetyDone {
		engine.Note(report, "Taking a safety backup of the current data first.")
		safety := &domain.Run{ID: domain.NewID("run"), ResourceID: bres.ID, Trigger: api.TriggerPreRestore, Status: api.RunRunning, StartedAt: r.Now()}
		// Claim the run before it exists: the scheduler adopts every running run, and a second execution
		// would overwrite this object with the data after the restore.
		r.adopt(safety.ID)
		defer r.release("run:" + safety.ID)
		if err := r.Store.InsertRun(ctx, safety); err != nil {
			return "", err
		}
		art, msg, err := r.backupOnce(ctx, bres, s, safety)
		log := r.Log.With("resource_id", bres.ID, "run_id", safety.ID)
		if err != nil {
			r.finish(context.WithoutCancel(ctx), bres, safety, api.RunFailed, nil, nil, "Safety backup before a restore failed: "+truncate(err.Error()), log)
			return "", engine.Permanent(fmt.Errorf("the safety backup failed, so nothing was changed: %w", err))
		}
		safety.Artifact = art
		r.finish(context.WithoutCancel(ctx), bres, safety, api.RunSucceeded, nil, nil, msg, log)
		pr.SafetyDone = true
		target.Outputs = withOutput(target.Outputs, OutputPendingRestore, pr)
		_ = r.Store.MergeOutputs(ctx, target.ID, map[string]any{OutputPendingRestore: pr})
	}
	engine.Note(report, "Downloading "+from+".")
	dest, err := r.backupTarget(ctx, bres.Project, s)
	if err != nil {
		return "", err
	}
	st, err := r.Opener.Open(ctx, dest)
	if err != nil {
		return "", err
	}
	obj, err := st.Get(ctx, run.Artifact.Key)
	if errors.Is(err, backup.ErrNotFound) {
		return "", engine.Permanent(fmt.Errorf("the backup object %s is gone from %s", run.Artifact.Key, dest.Describe()))
	}
	if err != nil {
		return "", fmt.Errorf("the backup can't be read: %w", err)
	}
	defer obj.Close()
	// Verify the checksum before anything changes: the object goes to a temporary file first.
	tmp, err := os.CreateTemp(r.TempDir, "restore-*")
	if err != nil {
		return "", err
	}
	defer func() {
		tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	if _, err := backup.VerifyingCopy(tmp, obj, run.Artifact.SHA256); err != nil {
		return "", engine.Permanent(err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	engine.Note(report, "Restoring "+from+".")
	r.event(ctx, target, "restore_started", "Restoring from "+from+".")
	if err := r.restoreFrom(ctx, target, in, tmp); err != nil {
		return "", fmt.Errorf("the restore failed: %w", err)
	}
	r.event(ctx, target, "restored", "Restored from "+from+".")
	return from, nil
}
