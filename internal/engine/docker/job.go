package docker

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/enclavestack/platform/internal/engine"
)

// LabelRun ties a job container to its run (ADR-017).
const LabelRun = "enclave.run-id"

func jobContainerName(run engine.JobRun) string {
	_, suffix, _ := strings.Cut(run.RunID, "_")
	return "es-job-" + run.Name + "-" + suffix
}

// StartJob implements engine.Driver.
func (d *Driver) StartJob(ctx context.Context, run engine.JobRun) error {
	name := jobContainerName(run)
	c, err := d.inspect(ctx, name)
	if err != nil {
		return err
	}
	if c == nil {
		if err := d.ensureImage(ctx, run.Image, false); err != nil {
			return err
		}
		env := make([]string, 0, len(run.Env))
		for k, v := range run.Env {
			env = append(env, k+"="+v)
		}
		cfg := &container.Config{Image: run.Image, Env: env,
			Labels: map[string]string{LabelManaged: "true", LabelResourceID: run.ResourceID, LabelKind: "job", LabelRun: run.RunID}}
		if len(run.Command) > 0 {
			cfg.Cmd = run.Command
		}
		// No restart policy: a run runs once. Never privileged, no host network or host mounts (L3-006 §2).
		host := &container.HostConfig{Resources: container.Resources{Memory: int64(run.MemoryMiB) << 20}, Mounts: volumeMounts(run.Mounts)}
		netCfg, err := d.endpoint(ctx, run.Placement, nil)
		if err != nil {
			return err
		}
		cctx, cancel := context.WithTimeout(ctx, stepTimeout)
		_, err = d.cli.ContainerCreate(cctx, cfg, host, netCfg, nil, name)
		cancel()
		if err != nil {
			return d.classify(fmt.Errorf("create run container: %w", err))
		}
	} else if c.State != nil && c.State.Status != "created" {
		return nil // already started (or finished): WaitJob takes it from here
	}
	return d.start(ctx, name)
}

// WaitJob implements engine.Driver.
func (d *Driver) WaitJob(ctx context.Context, runID string, timeout time.Duration) (engine.JobResult, error) {
	lctx, cancel := context.WithTimeout(ctx, stepTimeout)
	list, err := d.cli.ContainerList(lctx, container.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", LabelRun+"="+runID))})
	cancel()
	if err != nil {
		return engine.JobResult{}, d.classify(fmt.Errorf("find run container: %w", err))
	}
	if len(list) == 0 {
		return engine.JobResult{}, engine.ErrJobMissing
	}
	id := list[0].ID
	var res engine.JobResult
	wctx, wcancel := context.WithTimeout(ctx, timeout)
	statusCh, errCh := d.cli.ContainerWait(wctx, id, container.WaitConditionNotRunning)
	select {
	case st := <-statusCh:
		res.ExitCode = int(st.StatusCode)
	case err := <-errCh:
		wcancel()
		if ctx.Err() != nil {
			return res, ctx.Err() // shutdown: the run keeps going and is adopted on the next start
		}
		if wctx.Err() == nil {
			return res, d.classify(fmt.Errorf("wait for run: %w", err))
		}
		res.TimedOut = true
		kctx, kcancel := context.WithTimeout(context.WithoutCancel(ctx), stepTimeout)
		if err := d.cli.ContainerKill(kctx, id, "KILL"); err != nil && !isNotFound(err) && !strings.Contains(err.Error(), "is not running") {
			kcancel()
			return res, d.classify(fmt.Errorf("stop timed-out run: %w", err))
		}
		kcancel()
		res.ExitCode = 137
	}
	wcancel()
	octx := context.WithoutCancel(ctx)
	res.Output = d.jobOutput(octx, id)
	if err := d.remove(octx, id); err != nil {
		d.log.Warn("remove finished run container", "run_id", runID, "error", err)
	}
	return res, nil
}

// jobOutput returns the last engine.OutputLimit bytes of a container's output.
func (d *Driver) jobOutput(ctx context.Context, id string) []byte {
	lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rc, err := d.cli.ContainerLogs(lctx, id, container.LogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		return nil
	}
	defer rc.Close()
	var buf tailBuffer
	_, _ = stdcopy.StdCopy(&buf, &buf, rc)
	return buf.Bytes()
}

// tailBuffer keeps only the last engine.OutputLimit bytes written to it.
type tailBuffer struct{ bytes.Buffer }

func (b *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	b.Buffer.Write(p)
	if extra := b.Len() - engine.OutputLimit; extra > 0 {
		b.Next(extra)
	}
	return n, nil
}

func (b *tailBuffer) Bytes() []byte { return append([]byte(nil), b.Buffer.Bytes()...) }
