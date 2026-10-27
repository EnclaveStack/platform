package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/enclavestack/platform/internal/engine"
)

// RunConfig is everything `enclave agent run` can be told.
type RunConfig struct {
	StateDir string // default ~/.enclave-agent
	// Listen overrides the address saved at join (":7071").
	Listen string
	// Driver and Hypervisor are this machine's engines (the Docker driver and QEMU, or fakes in tests).
	Driver     engine.Driver
	Hypervisor engine.Hypervisor
	// TunnelPublicKey and Sweep are the mesh's and the driver's (ADR-045, ADR-046); nil in tests.
	TunnelPublicKey func(context.Context) string
	Sweep           func(ctx context.Context, keep []string) ([]string, error)
	Log             *slog.Logger
	// Ready, when set, receives the bound address once the agent listens.
	Ready chan<- string
	// Started, when set, is given the server before it listens (auto-promotion watches its last contact).
	Started func(*Server)
}

// Run serves the agent until ctx ends (ADR-042).
func Run(ctx context.Context, cfg RunConfig) error {
	if cfg.StateDir == "" {
		cfg.StateDir = DefaultStateDir()
	}
	id, err := Load(cfg.StateDir)
	if err != nil {
		return err
	}
	listen := cfg.Listen
	if listen == "" {
		listen = id.Listen
	}
	if listen == "" {
		listen = fmt.Sprintf(":%d", 7071)
	}
	tlsCfg, certs, err := id.serverTLS()
	if err != nil {
		return fmt.Errorf("load the agent's identity from %s: %w", cfg.StateDir, err)
	}
	files := filepath.Join(cfg.StateDir, "files")
	if err := os.MkdirAll(files, 0o700); err != nil {
		return err
	}
	s := &Server{Driver: cfg.Driver, Hypervisor: cfg.Hypervisor, MachineID: id.MachineID, FilesDir: files,
		TunnelPublicKey: cfg.TunnelPublicKey, Sweep: cfg.Sweep, Log: cfg.Log, certs: certs, StateDir: cfg.StateDir, Standby: id.Standby}
	if cfg.Started != nil {
		cfg.Started(s)
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", listen, err)
	}
	srv := &http.Server{Handler: s.Handler(), TLSConfig: tlsCfg, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second,
		ErrorLog: slog.NewLogLogger(cfg.Log.Handler(), slog.LevelWarn)}
	if cfg.Ready != nil {
		cfg.Ready <- ln.Addr().String()
	}
	cfg.Log.Info("agent started", "machine", id.Name, "machine_id", id.MachineID, "listen", ln.Addr().String(), "server", id.Server)
	errc := make(chan error, 1)
	go func() { errc <- srv.ServeTLS(ln, "", "") }()
	select {
	case <-ctx.Done():
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
		return nil
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
