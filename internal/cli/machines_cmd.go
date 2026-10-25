package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/enclavestack/platform/internal/agent"
	"github.com/enclavestack/platform/internal/observability"
	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// Machines and the agent (L2-002 CLI-46, CLI-47).

// machineSubcommands adds the commands for other machines to `enclave machine`, which on its own still
// shows the main machine.
func (a *app) machineSubcommands(cmd *cobra.Command) {
	var labels []string
	var edge bool
	var address string
	var agentPort int
	add := &cobra.Command{
		Use: "add NAME [--label k=v]… | add NAME --edge --address A", Short: "Add a machine and print the command that joins it (machine admins)", Args: cobra.ExactArgs(1),
		Long: "Add a machine and print the command that joins it.\n\nWith --edge, add an edge machine: a server with a public address (a rented VPS) that only relays\nvisitors to this machine for relay tunnels (L2-014 EDG-01). This machine joins it, so the command run there names no server.",
		RunE: func(cmd *cobra.Command, args []string) error {
			l, err := parseLabels(labels)
			if err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			req := api.AddMachine{Name: args[0], Labels: l}
			if edge {
				req.Role, req.Address, req.AgentPort = api.MachineEdge, address, agentPort
			} else if address != "" || agentPort != 0 {
				return usagef("--address and --agent-port go with --edge")
			}
			out, err := c.AddMachine(cmd.Context(), req)
			if err != nil {
				return err
			}
			return a.printJoin(out)
		},
	}
	add.Flags().StringArrayVar(&labels, "label", nil, "a label placement can ask for, as key=value (repeat)")
	add.Flags().BoolVar(&edge, "edge", false, "add an edge machine that only relays visitors (L2-014)")
	add.Flags().StringVar(&address, "address", "", "the edge machine's public IPv4 address or host name")
	add.Flags().IntVar(&agentPort, "agent-port", 0, "where the edge machine's agent waits (default 7071)")
	list := &cobra.Command{
		Use: "ls", Aliases: []string{"list"}, Short: "List the machines", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			items, err := c.ListMachines(cmd.Context())
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(api.MachineList{Items: items})
			}
			rows := [][]string{}
			for _, m := range items {
				state := m.State
				if m.Role == api.MachineEdge {
					state += " (edge)"
				} else if !m.Schedulable {
					state += " (not schedulable)"
				}
				rows = append(rows, []string{m.Name, state, deref(m.Address, "-"), strconv.Itoa(m.CPUs),
					mib(m.MemoryReservedBytes) + " of " + mib(m.MemoryTotalBytes), strconv.Itoa(m.Resources), labelText(m.Labels)})
			}
			a.table([]string{"NAME", "STATE", "ADDRESS", "CPUS", "MEMORY RESERVED", "RESOURCES", "LABELS"}, rows)
			return nil
		},
	}
	get := &cobra.Command{
		Use: "get NAME", Short: "Show a machine", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			m, err := c.GetMachineByName(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(m)
			}
			rows := [][]string{{"Name", m.Name}, {"Title", m.Title}, {"Role", roleText(m.Role)}, {"State", m.State + " since " + ago(&m.Since)},
				{"Schedulable", map[bool]string{true: "yes", false: "no"}[m.Schedulable]}, {"Address", deref(m.Address, "-")},
				{"OS / arch", m.OS + " / " + m.Arch}, {"Agent version", m.AgentVersion}, {"Engine", m.Engine.Driver + " " + m.Engine.Version},
				{"CPUs", strconv.Itoa(m.CPUs)}, {"Memory reserved", mib(m.MemoryReservedBytes) + " of " + mib(m.MemoryTotalBytes)},
				{"VMs", vmSupportText(m.VMs)}, {"Resources", strconv.Itoa(m.Resources)}, {"Labels", labelText(m.Labels)}, {"Last seen", ago(m.LastSeenAt)}}
			for _, l := range m.Links {
				state := "not connected"
				if l.Connected {
					state = "connected, last heard " + ago(l.LastHandshakeAt)
				}
				rows = append(rows, []string{"Link to " + l.Machine, state})
			}
			if m.Message != "" {
				rows = append(rows, []string{"Note", m.Message})
			}
			a.table([]string{"FIELD", "VALUE"}, rows)
			return nil
		},
	}
	var newLabels []string
	var schedulable bool
	var title string
	update := &cobra.Command{
		Use: "update NAME [--label k=v]… [--schedulable=false] [--title TEXT]", Short: "Change a machine's labels, whether it takes new resources, or the main machine's display name (machine admins)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ch := cmd.Flags().Changed
			if !ch("label") && !ch("schedulable") && !ch("title") {
				return usagef("pass --label, --schedulable or --title")
			}
			req := api.UpdateMachine{}
			if ch("label") {
				l, err := parseLabels(newLabels)
				if err != nil {
					return err
				}
				req.Labels = l
			}
			if ch("schedulable") {
				req.Schedulable = &schedulable
			}
			if ch("title") {
				req.Title = &title
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			m, err := c.UpdateMachineByName(cmd.Context(), args[0], req)
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(m)
			}
			fmt.Fprintf(a.io.Out, "Updated %s: labels %s, %s.\n", m.Name, labelText(m.Labels), map[bool]string{true: "schedulable", false: "not schedulable"}[m.Schedulable])
			return nil
		},
	}
	update.Flags().StringArrayVar(&newLabels, "label", nil, "the machine's labels, replacing the old ones, as key=value (repeat)")
	update.Flags().BoolVar(&schedulable, "schedulable", true, "whether new resources may be placed here")
	update.Flags().StringVar(&title, "title", "", "the main machine's display name (1–64 characters)")
	joinCode := &cobra.Command{
		Use: "join-code NAME", Short: "Make a new join code for a machine still waiting for its agent", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			out, err := c.NewJoinCode(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return a.printJoin(out)
		},
	}
	var force, yes bool
	remove := &cobra.Command{
		Use: "remove NAME [--force]", Short: "Remove a machine with nothing on it; --force removes an offline one anyway (machine admins)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			q := "Remove the machine " + args[0] + "?"
			if force {
				q = "Remove " + args[0] + " for good? Everything that ran there becomes failed, for you to restore from backups."
			}
			if err := a.confirm(q, yes); err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			if err := c.RemoveMachine(cmd.Context(), args[0], force); err != nil {
				return err
			}
			fmt.Fprintf(a.io.Out, "Removed the machine %s. On that computer, `enclave agent leave` removes EnclaveStack's containers and the agent's identity.\n", args[0])
			return nil
		},
	}
	remove.Flags().BoolVar(&force, "force", false, "remove an offline machine that still has resources; they become failed")
	remove.Flags().BoolVar(&yes, "yes", false, "don't ask")
	events := &cobra.Command{
		Use: "events NAME", Short: "Show a machine's events", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			items, err := c.MachineEvents(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(api.EventList{Items: items})
			}
			rows := [][]string{}
			for _, e := range items {
				rows = append(rows, []string{ago(&e.CreatedAt), e.Type, e.Message})
			}
			a.table([]string{"WHEN", "TYPE", "MESSAGE"}, rows)
			return nil
		},
	}
	cmd.AddCommand(add, list, get, update, joinCode, remove, events, a.drainCmd())
}

func (a *app) printJoin(out api.AddedMachine) error {
	if a.output == "json" {
		return a.json(out)
	}
	if out.Machine.Role == api.MachineEdge {
		fmt.Fprintf(a.io.Out, "Added the edge machine %s. On that server (Linux with Docker), run this within the hour:\n\n    %s\n\n"+
			"It waits for this machine, which joins it within a minute. Its firewall must let this machine in on the agent's port\n"+
			"(7071/tcp unless you chose another) and 51820/udp, and everyone in on 80/tcp and 443/tcp. The code works once.\n", out.Machine.Name, out.JoinCommand)
		return nil
	}
	fmt.Fprintf(a.io.Out, "Added the machine %s. On that computer (Linux or macOS with Docker), run this within the hour:\n\n    %s\n\n"+
		"The main machine must reach it on 7071/tcp, and the machines each other on 51820/udp. The code works once.\n", out.Machine.Name, out.JoinCommand)
	return nil
}

func roleText(role string) string {
	if role == api.MachineEdge {
		return "edge (only relays visitors for relay tunnels)"
	}
	return "worker"
}

func parseLabels(pairs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return nil, usagef("a label is key=value, not %q", p)
		}
		out[k] = v
	}
	return out, nil
}

func labelText(labels map[string]string) string {
	if len(labels) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + labels[k]
	}
	return strings.Join(parts, ",")
}

func vmSupportText(v api.VMSupport) string {
	if v.Available {
		return "yes (" + strings.TrimSpace(v.QEMUVersion+" "+v.Accelerator) + ")"
	}
	if v.Message == "" {
		return "no"
	}
	return "no: " + v.Message
}

// agentCmd runs on the other computer (CLI-47).
func (a *app) agentCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "agent", Short: "Run this computer as a machine of an EnclaveStack (on the other computer)"}
	var opts agent.JoinOptions
	var run agentRunFlags
	var install, edge bool
	join := &cobra.Command{
		Use: "join SERVER --code CODE | join --edge --code CODE", Short: "Join an EnclaveStack with a code from `enclave machine add`, then run the agent", Args: cobra.MaximumNArgs(1),
		Long: "Join an EnclaveStack with a code from `enclave machine add`, then run the agent.\n\nWith --edge, this computer is an edge machine: it waits on --listen for the main machine to join it\n(the main machine can reach it; the reverse often isn't true), then runs the agent (L2-014 EDG-02).",
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.Code == "" {
				return usagef("pass --code, from `enclave machine add` on the main machine")
			}
			var id agent.Identity
			var err error
			if edge {
				if len(args) > 0 {
					return usagef("an edge machine waits for the main machine: drop %s", args[0])
				}
				fmt.Fprintf(a.io.Out, "Waiting on %s for the main machine to join this edge machine…\n", opts.Listen)
				id, err = agent.WaitForMain(cmd.Context(), agent.EdgeJoinOptions{Code: opts.Code, Listen: opts.Listen, TunnelPort: opts.TunnelPort, StateDir: run.stateDir})
			} else {
				if len(args) == 0 {
					return usagef("name the main machine's address, as `enclave machine add` printed it")
				}
				opts.Server, opts.StateDir = args[0], run.stateDir
				id, err = agent.Join(cmd.Context(), opts)
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(a.io.Out, "Joined as the machine %s (%s). The agent listens on %s.\n", id.Name, id.MachineID, id.Listen)
			if install {
				return a.installAgentService(id)
			}
			return a.runAgent(cmd, run)
		},
	}
	join.Flags().StringVar(&opts.Code, "code", "", "the join code")
	join.Flags().StringVar(&opts.Advertise, "advertise", "", "the address the main machine and the others reach this one at (default: the one that reaches SERVER)")
	join.Flags().StringVar(&opts.Listen, "listen", ":7071", "where the agent listens")
	join.Flags().IntVar(&opts.TunnelPort, "tunnel-port", api.TunnelPortDefault, "the UDP port of the tunnel between machines")
	join.Flags().BoolVar(&install, "install", false, "install the agent as a service that starts with the computer (systemd or launchd) instead of running it here")
	join.Flags().BoolVar(&edge, "edge", false, "join as an edge machine: wait for the main machine to come to this one")
	run.register(join)
	runCmd := &cobra.Command{
		Use: "run", Short: "Run the agent of a computer that joined", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return a.runAgent(cmd, run) },
	}
	run.register(runCmd)
	status := &cobra.Command{
		Use: "status", Short: "Show whether this computer joined, and as what", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			id, err := agent.Load(run.dir())
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(id)
			}
			a.table([]string{"FIELD", "VALUE"}, [][]string{{"Machine", id.Name}, {"Machine ID", id.MachineID}, {"Server", id.Server},
				{"Advertised", id.Advertise}, {"Listens", id.Listen}, {"Tunnel port", strconv.Itoa(id.TunnelPort)}, {"State", run.dir()}})
			return nil
		},
	}
	var yes bool
	leave := &cobra.Command{
		Use: "leave", Short: "Remove EnclaveStack's containers from this computer and forget its identity", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			id, err := agent.Load(run.dir())
			if err != nil {
				return err
			}
			if err := a.confirm("Remove every EnclaveStack container, volume and network from this computer, and leave "+id.Server+"?", yes); err != nil {
				return err
			}
			removed, err := agent.Leave(cmd.Context(), run.dir(), slog.New(slog.DiscardHandler))
			if err != nil {
				return err
			}
			fmt.Fprintf(a.io.Out, "Removed %d containers and the agent's identity. Remove the machine %s on the main machine too, if you haven't.\n", len(removed), id.Name)
			return nil
		},
	}
	leave.Flags().BoolVar(&yes, "yes", false, "don't ask")
	run.registerState(status)
	run.registerState(leave)
	cmd.AddCommand(join, runCmd, status, leave)
	return cmd
}

// agentRunFlags are the flags of `agent run` (and of `agent join`, which runs it).
type agentRunFlags struct {
	stateDir, listen, driver, publish, qemu, logLevel, logFormat string
}

func (f *agentRunFlags) dir() string {
	if f.stateDir == "" {
		return agent.DefaultStateDir()
	}
	return f.stateDir
}

func (f *agentRunFlags) registerState(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.stateDir, "state-dir", agent.DefaultStateDir(), "where the agent keeps its identity")
}

func (f *agentRunFlags) register(cmd *cobra.Command) {
	f.registerState(cmd)
	fl := cmd.Flags()
	if fl.Lookup("listen") == nil {
		fl.StringVar(&f.listen, "listen", "", "where the agent listens (default: as joined)")
	}
	fl.StringVar(&f.driver, "driver", "docker", "engine driver: docker, or fake for demos and tests")
	fl.StringVar(&f.publish, "publish-address", "0.0.0.0", "address published ports bind to on this machine")
	fl.StringVar(&f.qemu, "qemu", "", "QEMU binary for VMs; default: found on PATH")
	fl.StringVar(&f.logLevel, "log-level", "info", "debug, info, warn or error")
	fl.StringVar(&f.logFormat, "log-format", "auto", "json, text, or auto (text on a terminal)")
	_ = fl.MarkHidden("driver")
}

func (a *app) runAgent(cmd *cobra.Command, f agentRunFlags) error {
	text := f.logFormat == "text" || f.logFormat == "auto" && term.IsTerminal(int(os.Stderr.Fd()))
	log := observability.NewLogger(a.io.Err, f.logLevel, text)
	return agent.RunHost(cmd.Context(), agent.HostConfig{StateDir: f.dir(), Listen: f.listen, Driver: f.driver, PublishAddress: f.publish, QEMU: f.qemu, Log: log})
}

// installAgentService writes the agent's service and starts it (MCH-12).
func (a *app) installAgentService(id agent.Identity) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	u, err := agent.Unit(runtime.GOOS, home, agent.StableExecutable(exe), id.Dir())
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(u.Path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(u.Path, []byte(u.Content), 0o644); err != nil {
		return err
	}
	out, err := exec.Command(u.Enable[0], u.Enable[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("wrote %s, but `%s` failed: %v: %s", u.Path, strings.Join(u.Enable, " "), err, strings.TrimSpace(string(out)))
	}
	fmt.Fprintf(a.io.Out, "Installed the agent as a service (%s); it starts now and with the computer.\n", u.Path)
	return nil
}

// manyMachines reports whether this EnclaveStack has more than one machine: only then do lists show where
// each resource runs (PLC-08: one machine shows what it showed before).
func (a *app) manyMachines(ctx context.Context, c *client.Client) (bool, error) {
	ms, err := c.ListMachines(ctx)
	return err == nil && len(ms) > 1, err
}

// machineOf is the machine a resource runs on, "-" for kinds with none.
func machineOf(r api.Resource) string {
	if m, ok := r.Outputs["machine"].(string); ok && m != "" {
		return m
	}
	return "-"
}
