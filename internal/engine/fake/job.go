package fake

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/engine"
)

// Fake runs read their command: "sleep N" takes N seconds (fractions allowed), "exit N" ends with code
// N, and anything printed is "<job>: <command>". Real time, so tests use small numbers.

var (
	sleepArg = regexp.MustCompile(`sleep\s+([0-9.]+)`)
	exitArg  = regexp.MustCompile(`exit\s+([0-9]+)`)
)

type fakeRun struct {
	run     engine.JobRun
	started time.Time
}

// JobStarts returns how many times StartJob actually started a run (it must be at most once).
func (d *Driver) JobStarts(runID string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.jobStarts[runID]
}

// StartedJobs returns the runs started so far, in no particular order.
func (d *Driver) StartedJobs() []engine.JobRun {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]engine.JobRun, 0, len(d.jobs))
	for _, r := range d.jobs {
		out = append(out, r.run)
	}
	return out
}

// JobRunOf returns what a run started with, also after it ended.
func (d *Driver) JobRunOf(runID string) (engine.JobRun, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r, ok := d.jobRuns[runID]
	return r, ok
}

// StartJob implements engine.Driver.
func (d *Driver) StartJob(ctx context.Context, run engine.JobRun) error {
	if err := d.Ping(ctx); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.jobs == nil {
		d.jobs, d.jobStarts = map[string]*fakeRun{}, map[string]int{}
	}
	if r := d.jobs[run.RunID]; r != nil {
		return nil
	}
	d.jobs[run.RunID] = &fakeRun{run: run, started: time.Now()}
	if d.jobImages == nil {
		d.jobImages = map[string]string{}
	}
	d.jobImages[run.RunID] = run.Image
	if d.jobRuns == nil {
		d.jobRuns = map[string]engine.JobRun{}
	}
	d.jobRuns[run.RunID] = run
	d.jobStarts[run.RunID]++
	return nil
}

// WaitJob implements engine.Driver.
func (d *Driver) WaitJob(ctx context.Context, runID string, timeout time.Duration) (engine.JobResult, error) {
	d.mu.Lock()
	r := d.jobs[runID]
	d.mu.Unlock()
	if r == nil {
		return engine.JobResult{}, engine.ErrJobMissing
	}
	cmd := strings.Join(r.run.Command, " ")
	var dur time.Duration
	if m := sleepArg.FindStringSubmatch(cmd); m != nil {
		secs, _ := strconv.ParseFloat(m[1], 64)
		dur = time.Duration(secs * float64(time.Second))
	}
	remaining := dur - time.Since(r.started)
	res := engine.JobResult{Output: []byte(fmt.Sprintf("%s: %s\n", r.run.Name, cmd))}
	wait := remaining
	if remaining > timeout {
		wait, res.TimedOut, res.ExitCode = timeout, true, 137
	}
	if wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return engine.JobResult{}, ctx.Err()
		case <-t.C:
		}
	}
	if !res.TimedOut {
		if m := exitArg.FindStringSubmatch(cmd); m != nil {
			res.ExitCode, _ = strconv.Atoi(m[1])
		}
	}
	d.mu.Lock()
	delete(d.jobs, runID)
	d.mu.Unlock()
	return res, nil
}

// JobImage returns the image a run started with (tests).
func (d *Driver) JobImage(runID string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.jobImages[runID]
}
