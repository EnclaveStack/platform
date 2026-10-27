package agent

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/engine/docker"
	"github.com/enclavestack/platform/internal/engine/fake"
	"github.com/enclavestack/platform/internal/engine/qemu"
)

// HostConfig is `enclave agent run` on a real computer: which engines to serve.
type HostConfig struct {
	StateDir string
	Listen   string
	// Driver is "docker" (default) or "fake" for demos and tests.
	Driver         string
	PublishAddress string
	QEMU           string
	Log            *slog.Logger
	// Started is given the agent's server before it listens (see RunConfig).
	Started func(*Server)
}

// RunHost builds this computer's engines and runs the agent until ctx ends.
func RunHost(ctx context.Context, cfg HostConfig) error {
	if cfg.StateDir == "" {
		cfg.StateDir = DefaultStateDir()
	}
	run := RunConfig{StateDir: cfg.StateDir, Listen: cfg.Listen, Log: cfg.Log, Started: cfg.Started}
	switch cfg.Driver {
	case "fake":
		d := fake.New(300 * time.Millisecond)
		run.Driver, run.Hypervisor = d, fake.NewHypervisor()
		run.Sweep = d.Sweep
	case "", "docker":
		d, err := docker.New(docker.Options{PublishAddress: cfg.PublishAddress, Agent: true, Log: cfg.Log})
		if err != nil {
			return err
		}
		run.Driver = d
		run.Sweep = func(ctx context.Context, keep []string) ([]string, error) { return d.Sweep(ctx, keep, false) }
		run.TunnelPublicKey = func(ctx context.Context) string {
			st, err := d.MeshStatus(ctx)
			if err != nil {
				return ""
			}
			return st.PublicKey
		}
		run.Hypervisor = qemu.New(qemu.Options{DataDir: filepath.Join(cfg.StateDir, "data"), QEMU: cfg.QEMU, Log: cfg.Log})
	default:
		return fmt.Errorf("--driver must be docker or fake, not %q", cfg.Driver)
	}
	return Run(ctx, run)
}

// Leave removes every EnclaveStack container, volume and network from this computer's engine and forgets the
// identity (`enclave agent leave`, MCH-09). It returns what it removed.
func Leave(ctx context.Context, stateDir string, log *slog.Logger) ([]string, error) {
	if stateDir == "" {
		stateDir = DefaultStateDir()
	}
	if _, err := Load(stateDir); err != nil {
		return nil, err
	}
	d, err := docker.New(docker.Options{Log: log})
	if err != nil {
		return nil, err
	}
	var drv engine.Driver = d
	if err := drv.Ping(ctx); err != nil {
		return nil, fmt.Errorf("the container engine isn't reachable, so nothing was removed: %w", err)
	}
	removed, err := d.Sweep(ctx, nil, true)
	if err != nil {
		return removed, err
	}
	return removed, Remove(stateDir)
}
