// Package cli is the `enclave` command (L2-002, ADR-008): `enclave server` runs the control plane;
// every other command is a client of its API through pkg/client.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// Exit codes (L2-002 CLI-06).
const (
	ExitOK          = 0
	ExitError       = 1
	ExitUsage       = 2
	ExitNotFound    = 3
	ExitAuth        = 4
	ExitConflict    = 5
	ExitWaitTimeout = 6
)

// DefaultServer is used when nothing else names one.
const DefaultServer = "http://127.0.0.1:7070"

// IO is the command's environment; tests supply their own.
type IO struct {
	In     io.Reader
	Out    io.Writer
	Err    io.Writer
	Getenv func(string) string
	// Interactive reports whether In is a terminal (prompts and confirmations).
	Interactive bool
	// ReadPassword reads a hidden line; nil reads a visible line from In.
	ReadPassword func() (string, error)
	// Exec runs another program attached to In, Out and Err (vm ssh); nil runs it for real.
	Exec func(name string, args []string) error
}

// ExitStatus is another program's exit code, passed through silently (vm ssh); IO.Exec returns it.
type ExitStatus int

func (e ExitStatus) Error() string { return "exit status " + strconv.Itoa(int(e)) }

// SystemIO is the real terminal.
func SystemIO() IO {
	interactive := term.IsTerminal(int(os.Stdin.Fd()))
	io := IO{In: os.Stdin, Out: os.Stdout, Err: os.Stderr, Getenv: os.Getenv, Interactive: interactive}
	if interactive {
		io.ReadPassword = func() (string, error) {
			b, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			return string(b), err
		}
	}
	return io
}

// usageError marks mistakes in how the command was called (exit 2).
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error { return &usageError{msg: fmt.Sprintf(format, a...)} }

// waitTimeout marks --wait running out of time (exit 6).
type waitTimeout struct{ what string }

func (e *waitTimeout) Error() string {
	return "Timed out waiting for " + e.what + ". It is still in progress; check with enclave get."
}

// app holds per-invocation state.
type app struct {
	io      IO
	server  string
	token   string
	output  string
	timeout time.Duration
	project string
	cfgPath string
	reader  *bufio.Reader
	// newClient is replaceable in tests.
	newClient func(server, token string) *client.Client
}

// Run executes args and returns the exit code.
func Run(ctx context.Context, args []string, io IO) int {
	a := &app{io: io, newClient: client.New, reader: bufio.NewReader(io.In)}
	root := a.root()
	root.SetArgs(args)
	root.SetIn(io.In)
	root.SetOut(io.Out)
	root.SetErr(io.Err)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return ExitOK
	}
	return a.report(err)
}

// Main runs with the real terminal and signal handling, then exits.
func Main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := Run(ctx, os.Args[1:], SystemIO())
	stop()
	os.Exit(code)
}

func (a *app) report(err error) int {
	var ue *usageError
	var wt *waitTimeout
	var ae *api.Error
	var ne *client.NetworkError
	var fe *client.FailedError
	var ec ExitStatus
	switch {
	case errors.As(err, &ec):
		return int(ec)
	case errors.As(err, &ue):
		fmt.Fprintln(a.io.Err, "Error:", ue.msg)
		return ExitUsage
	case errors.As(err, &wt):
		fmt.Fprintln(a.io.Err, "Error:", wt.Error())
		return ExitWaitTimeout
	case errors.As(err, &fe):
		fmt.Fprintln(a.io.Err, "Error:", fe.Error())
		return ExitError
	case errors.As(err, &ne):
		fmt.Fprintf(a.io.Err, "Error: can't reach EnclaveStack at %s. Is `enclave server` running?\n", ne.URL)
		return ExitError
	case errors.As(err, &ae):
		msg := ae.Message
		if ae.Code == api.CodeUnauthenticated {
			msg += " Run enclave login."
		}
		fmt.Fprintln(a.io.Err, "Error:", msg)
		for _, d := range ae.Details {
			fmt.Fprintf(a.io.Err, "  %s: %s\n", d.Field, d.Message)
		}
		if ae.RequestID != "" {
			fmt.Fprintln(a.io.Err, "Request ID:", ae.RequestID)
		}
		// Exit codes follow the code's HTTP status (pkg/api ErrorCodes), so new codes can't fall through (CLI-06).
		switch api.ErrorCodes[ae.Code] {
		case http.StatusNotFound:
			return ExitNotFound
		case http.StatusUnauthorized, http.StatusForbidden:
			return ExitAuth
		case http.StatusConflict, http.StatusPreconditionFailed:
			return ExitConflict
		}
		return ExitError
	case strings.HasPrefix(err.Error(), "unknown command"), strings.HasPrefix(err.Error(), "unknown flag"), strings.Contains(err.Error(), "accepts "),
		strings.Contains(err.Error(), "required flag"), strings.HasPrefix(err.Error(), "invalid argument"), strings.Contains(err.Error(), "flag needs an argument"):
		fmt.Fprintln(a.io.Err, "Error:", err.Error())
		return ExitUsage
	case errors.Is(err, context.Canceled):
		fmt.Fprintln(a.io.Err, "Interrupted.")
		return ExitError
	}
	fmt.Fprintln(a.io.Err, "Error:", err.Error())
	return ExitError
}

// Config is the saved login (CLI-01) and the project chosen with `enclave project use` (CLI-37).
type Config struct {
	Server  string `json:"server"`
	Token   string `json:"token"`
	Project string `json:"project,omitempty"`
}

func (a *app) configPath() string {
	if a.cfgPath != "" {
		return a.cfgPath
	}
	if p := a.io.Getenv("ENCLAVE_CONFIG"); p != "" {
		return p
	}
	dir := a.io.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "enclave", "config.json")
}

func (a *app) loadConfig() Config {
	var c Config
	b, err := os.ReadFile(a.configPath())
	if err == nil {
		_ = json.Unmarshal(b, &c)
	}
	return c
}

func (a *app) saveConfig(c Config) error {
	path := a.configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// resolved returns server and token by precedence: flag > environment > config file (CLI-02).
func (a *app) resolved() (string, string) {
	cfg := a.loadConfig()
	server, token := a.server, a.token
	if server == "" {
		server = a.io.Getenv("ENCLAVE_SERVER")
	}
	if server == "" {
		server = cfg.Server
	}
	if server == "" {
		server = DefaultServer
	}
	if token == "" {
		token = a.io.Getenv("ENCLAVE_TOKEN")
	}
	if token == "" && (cfg.Server == "" || strings.TrimRight(cfg.Server, "/") == strings.TrimRight(server, "/")) {
		token = cfg.Token
	}
	return server, token
}

func (a *app) client() (*client.Client, error) {
	server, token := a.resolved()
	if token == "" {
		return nil, &api.Error{Code: api.CodeUnauthenticated, Message: "You're not signed in to " + server + "."}
	}
	c := a.newClient(server, token)
	c.UserAgent = "enclave-cli"
	c.Project = a.resolvedProject()
	return c, nil
}

// resolvedProject is the project by precedence: flag > environment > config file > default (CLI-37).
func (a *app) resolvedProject() string {
	for _, p := range []string{a.project, a.io.Getenv("ENCLAVE_PROJECT"), a.loadConfig().Project} {
		if p != "" {
			return p
		}
	}
	return api.DefaultProject
}

func (a *app) root() *cobra.Command {
	root := &cobra.Command{
		Use:           "enclave",
		Short:         "Your own cloud, on your own machine",
		Long:          "enclave runs the EnclaveStack control plane (enclave server) and manages its apps, databases, caches, buckets, secrets, networks, VMs, queues, topics, search, CDNs, email and more.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&a.server, "server", "", "control-plane URL (env ENCLAVE_SERVER; default "+DefaultServer+")")
	root.PersistentFlags().StringVar(&a.token, "token", "", "API token (env ENCLAVE_TOKEN)")
	root.PersistentFlags().StringVarP(&a.output, "output", "o", "table", "output format: table or json")
	root.PersistentFlags().DurationVar(&a.timeout, "timeout", 10*time.Minute, "how long --wait waits")
	root.PersistentFlags().StringVarP(&a.project, "project", "p", "", "project to work in (env ENCLAVE_PROJECT; enclave project use; default \"default\")")
	root.PersistentFlags().StringVar(&a.cfgPath, "config", "", "config file (env ENCLAVE_CONFIG)")
	_ = root.PersistentFlags().MarkHidden("config")
	root.PersistentPreRunE = func(*cobra.Command, []string) error {
		if a.output != "table" && a.output != "json" {
			return usagef("--output must be table or json")
		}
		return nil
	}
	root.AddCommand(a.serverCmd(), a.loginCmd(), a.logoutCmd(), a.whoamiCmd(), a.versionCmd(), a.machineCmd(),
		a.lsCmd(), a.getCmd(), a.deleteCmd(), a.logsCmd(), a.eventsCmd(), a.retryCmd(),
		a.dbCmd(), a.cacheCmd(), a.bucketCmd(), a.appCmd(), a.secretCmd(), a.domainCmd(), a.tunnelCmd(), a.jobCmd(), a.functionCmd(), a.backupCmd(), a.tokenCmd(),
		a.networkCmd(), a.firewallCmd(), a.lbCmd(), a.zoneCmd(), a.recordCmd(), a.vmCmd(), a.volumeCmd(), a.snapshotCmd(),
		a.queueCmd(), a.topicCmd(), a.searchCmd(), a.transcoderCmd(), a.cdnCmd(), a.emailCmd(),
		a.projectCmd(), a.userCmd(), a.passwordCmd(), a.auditCmd(),
		a.metricsCmd(), a.dashboardCmd(), a.alertCmd(), a.channelCmd(), a.logexportCmd(), a.secretstoreCmd(),
		a.agentCmd(), a.standbyCmd(), a.moveCmd(), a.catalogueCmd(), a.stackCmd())
	return root
}

// prompt reads a visible line.
func (a *app) prompt(label string) (string, error) {
	fmt.Fprint(a.io.Err, label)
	line, err := a.reader.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// promptSecret reads a hidden line when on a terminal.
func (a *app) promptSecret(label string) (string, error) {
	fmt.Fprint(a.io.Err, label)
	if a.io.ReadPassword != nil {
		return a.io.ReadPassword()
	}
	line, err := a.reader.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// confirm asks before destructive actions; non-interactive callers must pass --yes (CLI-07).
func (a *app) confirm(question string, yes bool) error {
	if yes {
		return nil
	}
	if !a.io.Interactive {
		return usagef("this deletes data; re-run with --yes to confirm when not on a terminal")
	}
	answer, err := a.prompt(question + " [y/N] ")
	if err != nil {
		return err
	}
	if a := strings.ToLower(answer); a != "y" && a != "yes" {
		return errors.New("cancelled")
	}
	return nil
}

func (a *app) json(v any) error {
	enc := json.NewEncoder(a.io.Out)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func (a *app) table(header []string, rows [][]string) {
	w := tabwriter.NewWriter(a.io.Out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, strings.Join(header, "\t"))
	for _, r := range rows {
		fmt.Fprintln(w, strings.Join(r, "\t"))
	}
	_ = w.Flush()
}
