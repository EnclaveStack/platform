package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/enclavestack/platform/internal/server"
	"github.com/enclavestack/platform/internal/version"
)

func (a *app) serverCmd() *cobra.Command {
	var cfg server.Config
	var logFormat, setupCodeFile string
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Run the EnclaveStack control plane and console",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			switch logFormat {
			case "json":
			case "text":
				cfg.LogText = true
			case "auto":
				cfg.LogText = term.IsTerminal(int(os.Stderr.Fd()))
			default:
				return usagef("--log-format must be json, text or auto")
			}
			if cfg.ConsoleDir == "" {
				if st, err := os.Stat("console/dist"); err == nil && st.IsDir() {
					cfg.ConsoleDir = "console/dist"
				}
			}
			if cfg.FakeFaults == "" {
				cfg.FakeFaults = a.io.Getenv("ENCLAVE_FAKE_FAULTS")
			}
			running, err := server.Start(cmd.Context(), cfg, a.io.Err)
			if err != nil {
				return err
			}
			// The banner goes to stdout, not the log stream: the setup code must never be logged (L3-006).
			fmt.Fprintf(a.io.Out, "EnclaveStack %s\nConsole: %s\n", version.Version, running.URL)
			if running.SetupCode != "" && setupCodeFile != "" {
				// For automation (e2e tests): a private file, never the log stream.
				if err := os.WriteFile(setupCodeFile, []byte(running.SetupCode+"\n"), 0o600); err != nil {
					_ = running.Stop()
					return err
				}
			}
			if running.SetupCode != "" {
				fmt.Fprintf(a.io.Out, "\nFirst run — open the console and enter this setup code:\n\n    %s\n\nIt works once, and a restart replaces it until setup is done.\n", running.SetupCode)
			}
			select {
			case <-cmd.Context().Done():
				return running.Stop()
			case <-running.Done():
				return running.Err()
			}
		},
	}
	f := cmd.Flags()
	f.StringVar(&cfg.Listen, "listen", "127.0.0.1:7070", "address to listen on; anything other than loopback needs TLS in front (L3-006)")
	f.StringVar(&cfg.DataDir, "data-dir", server.DefaultDataDir(), "where EnclaveStack keeps its database and master key")
	f.StringVar(&cfg.Driver, "driver", "docker", "engine driver: docker, or fake for demos and tests")
	f.StringVar(&cfg.PublishAddress, "publish-address", "127.0.0.1", "address published ports bind to")
	f.StringVar(&cfg.EdgeAddress, "edge-address", "127.0.0.1", "address the edge proxy binds ports 80/443 to; 0.0.0.0 serves domains publicly")
	f.StringVar(&cfg.QEMU, "qemu", "", "QEMU binary for VMs (qemu-system-aarch64 or qemu-system-x86_64); default: found on PATH")
	f.IntVar(&cfg.EdgeHTTPPort, "edge-http-port", 80, "edge proxy HTTP port")
	f.IntVar(&cfg.EdgeHTTPSPort, "edge-https-port", 443, "edge proxy HTTPS port")
	f.StringVar(&cfg.ConsoleDir, "console-dir", "", "serve the console from this directory (default: embedded, or ./console/dist)")
	f.StringVar(&cfg.LogLevel, "log-level", "info", "debug, info, warn or error")
	f.StringVar(&logFormat, "log-format", "auto", "json, text, or auto (text on a terminal)")
	f.Float64Var(&cfg.RateLimit, "rate-limit", 100, "API requests per second per client")
	f.BoolVar(&cfg.SecureCookies, "secure-cookies", false, "mark session cookies Secure (when served over HTTPS)")
	f.DurationVar(&cfg.FakeStepDelay, "fake-step-delay", 300*time.Millisecond, "fake driver: time each provisioning step takes")
	f.StringVar(&cfg.FakeFaults, "fake-faults", "", "fake driver: inject faults, e.g. 'web:transient=2;db:permanent' (env ENCLAVE_FAKE_FAULTS)")
	f.DurationVar(&cfg.DriftEvery, "drift-interval", 0, "how often to check for drift (default 60s)")
	f.DurationVar(&cfg.PollInterval, "poll-interval", 0, "how often the reconciler polls for due work (default 2s)")
	f.DurationVar(&cfg.MetricInterval, "metric-interval", 0, "how often metrics are sampled and alerts evaluated (default 30s)")
	f.StringVar(&cfg.ConsoleURL, "console-url", "", "the address people open the console at (e.g. https://enclave.example.com), used for links in alerts and invites")
	f.StringVar(&cfg.TelegramAPI, "telegram-api", "", "Telegram Bot API base URL for alert channels (default https://api.telegram.org)")
	_ = f.MarkHidden("metric-interval")
	_ = f.MarkHidden("telegram-api")
	_ = f.MarkHidden("fake-step-delay")
	_ = f.MarkHidden("fake-faults")
	f.StringVar(&setupCodeFile, "setup-code-file", "", "also write the first-run setup code to this file (mode 0600), for automation")
	_ = f.MarkHidden("setup-code-file")
	_ = f.MarkHidden("drift-interval")
	_ = f.MarkHidden("poll-interval")
	return cmd
}
