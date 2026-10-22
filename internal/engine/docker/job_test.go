//go:build docker

package docker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// Job runs on real Docker (ADR-017, L2-005 JOB-04, JOB-05).

func jobRun(job *domain.Resource, cmd ...string) engine.JobRun {
	return engine.JobRun{RunID: domain.NewID("run"), ResourceID: job.ID, Name: job.Name, Image: "busybox:1.37", Command: cmd,
		Env: map[string]string{"GREETING": "hello from env"}, MemoryMiB: 64}
}

func TestJobRunsRecordOutputExitCodesAndTimeouts(t *testing.T) {
	d := newDriver(t)
	ctx := context.Background()
	job := resource(api.KindJob, "it-job-"+suffix())
	cleanup(t, d, job, engine.ObjectStore{})

	ok := jobRun(job, "sh", "-c", `echo "$GREETING"; echo to-stderr >&2`)
	if err := d.StartJob(ctx, ok); err != nil {
		t.Fatal(err)
	}
	if err := d.StartJob(ctx, ok); err != nil { // idempotent: a second start does nothing
		t.Fatal(err)
	}
	res, err := d.WaitJob(ctx, ok.RunID, time.Minute)
	if err != nil || res.ExitCode != 0 || res.TimedOut || !strings.Contains(string(res.Output), "hello from env") || !strings.Contains(string(res.Output), "to-stderr") {
		t.Fatalf("ok run: %+v %q %v", res, res.Output, err)
	}
	if _, err := d.WaitJob(ctx, ok.RunID, time.Minute); !errors.Is(err, engine.ErrJobMissing) {
		t.Errorf("finished run's container should be gone: %v", err)
	}

	bad := jobRun(job, "sh", "-c", "exit 7")
	_ = d.StartJob(ctx, bad)
	if res, err := d.WaitJob(ctx, bad.RunID, time.Minute); err != nil || res.ExitCode != 7 {
		t.Errorf("exit code: %+v %v", res, err)
	}

	slow := jobRun(job, "sleep", "60")
	_ = d.StartJob(ctx, slow)
	start := time.Now()
	res, err = d.WaitJob(ctx, slow.RunID, 2*time.Second)
	if err != nil || !res.TimedOut || time.Since(start) > 20*time.Second {
		t.Errorf("timeout: %+v after %s (%v)", res, time.Since(start), err)
	}

	big := jobRun(job, "sh", "-c", "i=0; while [ $i -lt 2000 ]; do echo line-$i-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx; i=$((i+1)); done")
	_ = d.StartJob(ctx, big)
	res, err = d.WaitJob(ctx, big.RunID, time.Minute)
	if err != nil || len(res.Output) > engine.OutputLimit || !strings.Contains(string(res.Output), "line-1999-") || strings.Contains(string(res.Output), "line-0-") {
		t.Errorf("output tail: %d bytes, last line kept=%v (%v)", len(res.Output), strings.Contains(string(res.Output), "line-1999-"), err)
	}

	// Deleting the job stops a run still going.
	long := jobRun(job, "sleep", "300")
	_ = d.StartJob(ctx, long)
	if err := d.Delete(ctx, job, engine.ObjectStore{}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.WaitJob(ctx, long.RunID, time.Minute); !errors.Is(err, engine.ErrJobMissing) {
		t.Errorf("run outlived its deleted job: %v", err)
	}
}
