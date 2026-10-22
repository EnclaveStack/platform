package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// DefaultProbeImage runs HTTP and TCP health checks from inside the enclave network, so checks work
// with any app image and on macOS, where the host cannot reach container IPs (ADR-016, DAR-009).
// busybox 1.37, pinned by its multi-arch index digest.
const DefaultProbeImage = "busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"

// Implicit checks when an app sets none (L2-005 DEP-02).
const (
	implicitInterval = time.Second
	implicitTimeout  = time.Second
	implicitRetries  = 60
	settleNoCheck    = 5 * time.Second
	execOutputLimit  = 64 << 10
)

// ensureProbe makes sure the idle probe helper container exists and runs.
func (d *Driver) ensureProbe(ctx context.Context) error {
	c, err := d.inspect(ctx, engine.ProbeName)
	if err != nil {
		return err
	}
	if c != nil && c.Config.Image != d.opts.ProbeImage {
		if err := d.remove(ctx, engine.ProbeName); err != nil {
			return err
		}
		c = nil
	}
	if c == nil {
		if err := d.ensureImage(ctx, d.opts.ProbeImage, false); err != nil {
			return err
		}
		cfg := &container.Config{Image: d.opts.ProbeImage, Cmd: []string{"sleep", "2147483647"}, Labels: map[string]string{LabelManaged: "true", LabelKind: "system"}}
		host := &container.HostConfig{RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
			Resources: container.Resources{Memory: 32 << 20}}
		netCfg := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{engine.NetworkName: {}}}
		cctx, cancel := context.WithTimeout(ctx, stepTimeout)
		_, err := d.cli.ContainerCreate(cctx, cfg, host, netCfg, nil, engine.ProbeName)
		cancel()
		if err != nil && !strings.Contains(err.Error(), "already in use") {
			return d.classify(fmt.Errorf("create probe helper: %w", err))
		}
		if err := d.joinManagedNetworks(ctx, engine.ProbeName); err != nil {
			return err
		}
	}
	if c == nil || c.State == nil || !c.State.Running {
		sctx, cancel := context.WithTimeout(ctx, stepTimeout)
		defer cancel()
		if err := d.cli.ContainerStart(sctx, engine.ProbeName, container.StartOptions{}); err != nil {
			return d.classify(fmt.Errorf("start probe helper: %w", err))
		}
	}
	return nil
}

// execResult is a finished command run inside a container.
type execResult struct {
	ExitCode int
	Output   string
}

// exec runs an argument array (never a shell string) inside a running container and waits for it.
// Output is capped at 64 KiB.
func (d *Driver) exec(ctx context.Context, name string, cmd, env []string) (execResult, error) {
	created, err := d.cli.ContainerExecCreate(ctx, name, container.ExecOptions{Cmd: cmd, Env: env, AttachStdout: true, AttachStderr: true})
	if err != nil {
		return execResult{}, d.classify(fmt.Errorf("exec in %s: %w", name, err))
	}
	att, err := d.cli.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{})
	if err != nil {
		return execResult{}, d.classify(fmt.Errorf("attach exec in %s: %w", name, err))
	}
	defer att.Close()
	var out limitedBuffer
	done := make(chan error, 1)
	go func() {
		_, err := stdcopy.StdCopy(&out, &out, att.Reader)
		done <- err
	}()
	select {
	case <-ctx.Done():
		return execResult{}, ctx.Err()
	case err := <-done:
		if err != nil && !errors.Is(err, io.EOF) {
			return execResult{}, fmt.Errorf("read exec output: %w", err)
		}
	}
	insp, err := d.cli.ContainerExecInspect(ctx, created.ID)
	if err != nil {
		return execResult{}, d.classify(fmt.Errorf("inspect exec: %w", err))
	}
	return execResult{ExitCode: insp.ExitCode, Output: strings.TrimSpace(out.String())}, nil
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := execOutputLimit - b.Len(); room > 0 {
		b.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// effectiveCheck applies the implicit default when an app has none (L2-005 DEP-02).
func effectiveCheck(s api.AppSpec) api.HealthCheck {
	if s.HealthCheck != nil {
		return *s.HealthCheck
	}
	if s.Port > 0 {
		return api.HealthCheck{Type: api.HealthTCP, IntervalSeconds: int(implicitInterval / time.Second), TimeoutSeconds: int(implicitTimeout / time.Second), Retries: implicitRetries}
	}
	return api.HealthCheck{Type: api.HealthNone}
}

// probeRevision waits until a new revision passes its health check, reporting each attempt. It fails
// fast if the container stops.
func (d *Driver) probeRevision(ctx context.Context, name string, s api.AppSpec, report engine.Reporter) error {
	hc := effectiveCheck(s)
	if hc.Type == api.HealthNone {
		engine.Note(report, "Waiting for the new revision to keep running for 5 seconds.")
		return d.waitStable(ctx, name, settleNoCheck)
	}
	if hc.Type != api.HealthCommand {
		if err := d.ensureProbe(ctx); err != nil {
			return err
		}
	}
	if hc.StartPeriodSeconds > 0 {
		engine.Note(report, fmt.Sprintf("Waiting %d s for the new revision to start.", hc.StartPeriodSeconds))
		if err := d.waitStable(ctx, name, time.Duration(hc.StartPeriodSeconds)*time.Second); err != nil {
			return err
		}
	}
	port := hc.Port
	if port == 0 {
		port = s.Port
	}
	interval := time.Duration(hc.IntervalSeconds) * time.Second
	timeout := time.Duration(hc.TimeoutSeconds) * time.Second
	last := ""
	for attempt := 1; attempt <= hc.Retries; attempt++ {
		if err := d.stillRunning(ctx, name); err != nil {
			return err
		}
		pctx, cancel := context.WithTimeout(ctx, timeout+5*time.Second)
		var res execResult
		var err error
		switch hc.Type {
		case api.HealthHTTP:
			res, err = d.exec(pctx, engine.ProbeName, []string{"wget", "-q", "-T", strconv.Itoa(hc.TimeoutSeconds), "-O", "/dev/null",
				"http://" + name + ":" + strconv.Itoa(port) + hc.Path}, nil)
		case api.HealthTCP:
			res, err = d.exec(pctx, engine.ProbeName, []string{"nc", "-z", "-w", strconv.Itoa(hc.TimeoutSeconds), name, strconv.Itoa(port)}, nil)
		case api.HealthCommand:
			res, err = d.exec(pctx, name, hc.Command, nil)
		}
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			last = err.Error()
		} else if res.ExitCode == 0 {
			engine.Note(report, fmt.Sprintf("Health check passed on attempt %d of %d.", attempt, hc.Retries))
			return nil
		} else {
			last = describeProbe(hc.Type, res)
		}
		engine.Note(report, fmt.Sprintf("Health check attempt %d of %d · last: %s", attempt, hc.Retries, last))
		if attempt < hc.Retries {
			if err := sleepCtx(ctx, interval); err != nil {
				return err
			}
		}
	}
	return fmt.Errorf("the health check did not pass after %d attempts (last: %s)", hc.Retries, last)
}

func describeProbe(kind string, r execResult) string {
	out := r.Output
	if len(out) > 200 {
		out = out[:200] + "…"
	}
	switch {
	case out != "":
		return out
	case kind == api.HealthTCP:
		return "connection refused"
	}
	return "exit code " + strconv.Itoa(r.ExitCode)
}

// waitStable fails if the container stops or restarts within d; otherwise returns after d.
func (d *Driver) waitStable(ctx context.Context, name string, dur time.Duration) error {
	deadline := time.Now().Add(dur)
	for {
		if err := d.stillRunning(ctx, name); err != nil {
			return err
		}
		if !time.Now().Before(deadline) {
			return nil
		}
		if err := sleepCtx(ctx, min(500*time.Millisecond, time.Until(deadline))); err != nil {
			return err
		}
	}
}

func (d *Driver) stillRunning(ctx context.Context, name string) error {
	c, err := d.inspect(ctx, name)
	if err != nil {
		return err
	}
	switch {
	case c == nil:
		return fmt.Errorf("the new revision disappeared")
	case c.State == nil:
		return nil
	case c.State.Status == "exited" || c.State.Status == "dead" || c.State.Restarting || c.RestartCount > 0:
		return fmt.Errorf("the new revision stopped (exit code %d)", c.State.ExitCode)
	}
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
