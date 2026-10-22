package reconciler

import (
	"context"

	"fmt"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/backup"
	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/transcode"
	"github.com/enclavestack/platform/pkg/api"
)

// Transcoders (ADR-034, L2-008 TRN-*): the scheduler lists each watching transcoder's input folder,
// creates a run per new object, and runs convert one file each in an FFmpeg container.

// DefaultTranscodeWatch is how often a transcoder's input folder is listed.
const DefaultTranscodeWatch = 10 * time.Second

// baselineKey marks a transcoder's first listing as taken (the objects found then aren't converted).
const baselineKey = ""

func transcoderSpecOf(res *domain.Resource) (api.TranscoderSpec, error) {
	raw, _, err := api.NormalizeSpec(api.KindTranscoder, res.Spec)
	if err != nil {
		return api.TranscoderSpec{}, err
	}
	return raw.(api.TranscoderSpec), nil
}

// ensureTranscoder checks both buckets and starts watching (TRN-01, TRN-03). A new input folder starts
// a new baseline.
func (r *Reconciler) ensureTranscoder(ctx context.Context, res *domain.Resource, report engine.Reporter) (engine.Outputs, error) {
	s, err := transcoderSpecOf(res)
	if err != nil {
		return nil, engine.Permanent(fmt.Errorf("stored spec unreadable: %w", err))
	}
	report.Step("check", api.StepActive)
	for _, b := range []string{s.Input.Bucket, s.Output.Bucket} {
		if _, err := r.bucketTarget(ctx, res.Project, b); err != nil {
			return nil, err
		}
	}
	report.Step("check", api.StepDone)
	report.Step("watch", api.StepActive)
	input := s.Input.Bucket + "/" + s.Input.Prefix
	if prev, _ := res.Outputs["input"].(string); prev != input {
		if err := r.Store.ForgetSeen(ctx, res.ID); err != nil {
			return nil, err
		}
	}
	out := engine.Outputs{"watching": s.Watching(), "input": input}
	for _, k := range []string{"lastRunStatus", "lastRunAt"} {
		if v, ok := res.Outputs[k]; ok {
			out[k] = v
		}
	}
	report.Step("watch", api.StepDone)
	return out, nil
}

// interruptTranscodes fails the runs a previous process left running: a conversion can't be resumed
// (L3-007 §11).
func (r *Reconciler) interruptTranscodes(ctx context.Context) {
	running, err := r.Store.RunningRuns(ctx)
	if err != nil {
		return
	}
	for _, run := range running {
		res, err := r.Store.GetResource(ctx, run.ResourceID)
		if err != nil || res.Kind != api.KindTranscoder {
			continue
		}
		r.finish(ctx, res, run, api.RunFailed, nil, nil, "Interrupted: EnclaveStack stopped while converting. Transcode the file again to retry.", r.Log)
	}
}

// watchTranscoders lists each ready, watching transcoder's input at most every TranscodeWatch and starts
// runs for objects it hasn't seen, within its concurrency.
func (r *Reconciler) watchTranscoders(ctx context.Context) {
	every := r.TranscodeWatch
	if every <= 0 {
		every = DefaultTranscodeWatch
	}
	list, err := r.Store.ResourcesOfKind(ctx, api.KindTranscoder)
	if err != nil {
		return
	}
	now := r.Now()
	for _, res := range list {
		if res.Phase != api.PhaseReady || now.Sub(r.watchedAt[res.ID]) < every {
			continue
		}
		r.watchedAt[res.ID] = now
		s, err := transcoderSpecOf(res)
		if err != nil || !s.Watching() {
			continue
		}
		if err := r.watchOnce(ctx, res, s); err != nil && ctx.Err() == nil {
			r.Log.Warn("listing a transcoder's input failed", "resource_id", res.ID, "error", err)
		}
	}
}

func objectTag(o backup.Object) string {
	return strconv.FormatInt(o.Size, 10) + "@" + o.Modified.UTC().Format(time.RFC3339Nano)
}

func (r *Reconciler) watchOnce(ctx context.Context, res *domain.Resource, s api.TranscoderSpec) error {
	target, err := r.bucketTarget(ctx, res.Project, s.Input.Bucket)
	if err != nil {
		return err
	}
	st, err := r.Opener.Open(ctx, target)
	if err != nil {
		return err
	}
	objects, err := st.List(ctx, s.Input.Prefix)
	if err != nil {
		return err
	}
	slices.SortFunc(objects, func(a, b backup.Object) int { return strings.Compare(a.Key, b.Key) })
	seen, err := r.Store.SeenObjects(ctx, res.ID)
	if err != nil {
		return err
	}
	now := r.Now()
	if _, baselined := seen[baselineKey]; !baselined { // TRN-03: only objects that appear from now on
		for _, o := range objects {
			if err := r.Store.MarkSeen(ctx, res.ID, o.Key, objectTag(o), "", now); err != nil {
				return err
			}
		}
		return r.Store.MarkSeen(ctx, res.ID, baselineKey, "", "", now)
	}
	running, err := r.Store.CountRunning(ctx, res.ID)
	if err != nil {
		return err
	}
	for _, o := range objects {
		if running >= s.Concurrency {
			return nil // the rest wait for the next listing
		}
		if strings.HasSuffix(o.Key, "/") || seen[o.Key] == objectTag(o) {
			continue
		}
		run := &domain.Run{ID: domain.NewID("run"), ResourceID: res.ID, Trigger: api.TriggerWatch, Status: api.RunRunning, StartedAt: now, Key: o.Key}
		if err := r.Store.InsertRun(ctx, run); err != nil {
			return err
		}
		if err := r.Store.MarkSeen(ctx, res.ID, o.Key, objectTag(o), run.ID, now); err != nil {
			return err
		}
		running++
	}
	return nil
}

// runTranscode converts one object with every preset and uploads what FFmpeg wrote (TRN-02, TRN-05,
// TRN-06). A file that isn't media fails its run; the transcoder stays ready.
func (r *Reconciler) runTranscode(ctx context.Context, res *domain.Resource, run *domain.Run, log *slog.Logger) {
	done := context.WithoutCancel(ctx)
	fail := func(msg string) { r.finish(done, res, run, api.RunFailed, nil, nil, msg, log) }
	s, err := transcoderSpecOf(res)
	if err != nil {
		fail("The transcoder's spec is unreadable.")
		return
	}
	in, out, err := r.transcodeStores(ctx, res.Project, s)
	if err != nil {
		fail(err.Error())
		return
	}
	objects, err := in.List(ctx, run.Key)
	if err != nil {
		fail("The input bucket can't be listed: " + truncate(err.Error()))
		return
	}
	i := slices.IndexFunc(objects, func(o backup.Object) bool { return o.Key == run.Key })
	if i < 0 {
		fail(fmt.Sprintf("The object %s no longer exists in %s.", run.Key, s.Input.Bucket))
		return
	}
	src, err := in.Get(ctx, run.Key)
	if err != nil {
		fail("The input can't be read: " + truncate(err.Error()))
		return
	}
	defer src.Close()
	var written []string
	upload := func(name string, size int64, body io.Reader) error {
		key := transcode.OutputKey(s.Output.Prefix, s.Input.Prefix, run.Key, name)
		if _, err := out.Put(ctx, key, body, map[string]string{"enclave-transcoder": res.Name, "enclave-run": run.ID, "enclave-source": run.Key}); err != nil {
			return fmt.Errorf("upload %s: %w", key, err)
		}
		written = append(written, key)
		return nil
	}
	timeout := time.Duration(s.TimeoutSeconds) * time.Second
	result, err := r.Driver.Transcode(ctx, engine.TranscodeRun{RunID: run.ID, ResourceID: res.ID, Name: res.Name, InputName: transcode.InputName(run.Key),
		InputSize: objects[i].Size, Presets: s.Presets, MemoryMiB: s.MemoryMiB, Timeout: timeout}, src, upload)
	run.Outputs = written
	exit := result.ExitCode
	switch {
	case ctx.Err() != nil:
		return // failed as interrupted on the next start
	case err != nil:
		r.finish(done, res, run, api.RunFailed, nil, result.Output, "The conversion failed: "+truncate(err.Error()), log)
	case result.TimedOut:
		r.finish(done, res, run, api.RunTimedOut, &exit, result.Output, fmt.Sprintf("Stopped after the timeout of %s.", timeout), log)
	case exit != 0:
		r.finish(done, res, run, api.RunFailed, &exit, result.Output, "FFmpeg couldn't convert "+run.Key+": "+lastLine(result.Output), log)
	default:
		r.finish(done, res, run, api.RunSucceeded, &exit, result.Output, fmt.Sprintf("Converted %s into %d file%s.", run.Key, len(written), plural(len(written))), log)
	}
}

func (r *Reconciler) transcodeStores(ctx context.Context, project string, s api.TranscoderSpec) (backup.Store, backup.Store, error) {
	var stores [2]backup.Store
	for i, b := range []string{s.Input.Bucket, s.Output.Bucket} {
		t, err := r.bucketTarget(ctx, project, b)
		if err != nil {
			return nil, nil, err
		}
		if stores[i], err = r.Opener.Open(ctx, t); err != nil {
			return nil, nil, fmt.Errorf("the bucket %s can't be reached: %w", b, err)
		}
	}
	return stores[0], stores[1], nil
}

func lastLine(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) == 0 || lines[len(lines)-1] == "" {
		return "no output"
	}
	return truncate(lines[len(lines)-1])
}
