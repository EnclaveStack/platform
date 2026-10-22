package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/enclavestack/platform/pkg/api"
)

// v0.5 commands (L2-007, L2-002 CLI-24 – CLI-28): VMs, volumes, snapshots, volume mounts.

// parseMounts reads --volume NAME:PATH[:ro]; a single empty value means no mounts (update clears them).
func parseMounts(list []string) ([]api.VolumeMount, error) {
	out := []api.VolumeMount{}
	if len(list) == 1 && list[0] == "" {
		return out, nil
	}
	for _, s := range list {
		parts := strings.Split(s, ":")
		if len(parts) < 2 || len(parts) > 3 || parts[0] == "" || parts[1] == "" || (len(parts) == 3 && parts[2] != "ro" && parts[2] != "rw") {
			return nil, usagef("--volume needs NAME:PATH or NAME:PATH:ro, like uploads:/data; got %q", s)
		}
		out = append(out, api.VolumeMount{Volume: parts[0], Path: parts[1], ReadOnly: len(parts) == 3 && parts[2] == "ro"})
	}
	return out, nil
}

// readKeys takes --ssh-key values: a public key, or the path of a .pub file (VM-03).
func readKeys(values []string) ([]string, error) {
	var keys []string
	for _, v := range values {
		if strings.HasPrefix(v, "ssh-") || strings.HasPrefix(v, "ecdsa-") || strings.HasPrefix(v, "sk-") {
			keys = append(keys, strings.TrimSpace(v))
			continue
		}
		path := v
		if rest, ok := strings.CutPrefix(v, "~/"); ok {
			home, _ := os.UserHomeDir()
			path = home + "/" + rest
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, usagef("--ssh-key %s: %v (give a public key or the path of a .pub file)", v, err)
		}
		keys = append(keys, api.SplitKeys(string(b))...)
	}
	return keys, nil
}

// readUserData reads --user-data FILE, or standard input for "-".
func (a *app) readUserData(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if path == "-" {
		b, err := io.ReadAll(a.reader)
		return string(b), err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", usagef("--user-data: %v", err)
	}
	return string(b), nil
}

type vmFlags struct {
	image, sha256, keySecret, userData string
	keys, disks                        []string
	cpus, memory, disk                 int
	publish                            []int
	stopped, wait                      bool
	place                              placementFlags
}

func (f *vmFlags) bindShared(cmd *cobra.Command) {
	cmd.Flags().IntVar(&f.cpus, "cpus", 0, "CPUs (1–16, default 1); a change restarts the VM")
	cmd.Flags().IntVar(&f.memory, "memory", 0, "memory in MiB (512–65536 in steps of 128, default 1024); a change restarts the VM")
	cmd.Flags().IntVar(&f.disk, "disk", 0, "root disk in GiB (5–2048, default 10); it only grows")
	cmd.Flags().StringArrayVar(&f.disks, "disk-volume", nil, `disk volume to attach (repeatable; on update the list replaces the old one, "" for none); a change restarts the VM`)
	cmd.Flags().IntSliceVar(&f.publish, "publish", nil, "VM port to publish on the same host port (repeatable; not 22)")
	cmd.Flags().BoolVar(&f.wait, "wait", false, "wait until ready")
	f.place.bind(cmd)
}

func (a *app) vmCmd() *cobra.Command {
	cmd := a.kindCmd(api.KindVM, "vm", []string{"vms"}, "VMs: whole machines run by QEMU")
	var cf vmFlags
	create := &cobra.Command{
		Use: "create NAME --image IMAGE --ssh-key KEY_OR_FILE", Short: "Create a VM (image, keys and first-boot script can't change later)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			keys, err := readKeys(cf.keys)
			if err != nil {
				return err
			}
			user, err := a.readUserData(cf.userData)
			if err != nil {
				return err
			}
			spec := api.VMSpec{Image: cf.image, ImageSHA256: cf.sha256, SSHKeys: keys, SSHKeySecret: cf.keySecret, UserData: user,
				CPUs: cf.cpus, MemoryMiB: cf.memory, DiskGiB: cf.disk, Network: cf.place.network, Subnet: cf.place.subnet, Disks: cf.disks, Publish: cf.publish, Placed: cf.place.placed()}
			if cf.stopped {
				spec.Power = api.PowerStopped
			}
			return a.create(cmd.Context(), api.KindVM, args[0], spec, cf.wait)
		},
	}
	create.Flags().StringVar(&cf.image, "image", "", "ubuntu-24.04, debian-12, an https:// URL of a cloud image, or snapshot/NAME")
	create.Flags().StringVar(&cf.sha256, "sha256", "", "SHA-256 of a URL image")
	create.Flags().StringArrayVar(&cf.keys, "ssh-key", nil, "SSH public key, or the path of a .pub file (repeatable)")
	create.Flags().StringVar(&cf.keySecret, "ssh-key-secret", "", "secret holding SSH public keys, one per line (instead of --ssh-key)")
	create.Flags().StringVar(&cf.userData, "user-data", "", `first-boot script file (#cloud-config or #!...), "-" for standard input`)
	create.Flags().BoolVar(&cf.stopped, "stopped", false, "create it without starting it")
	cf.bindShared(create)
	_ = create.MarkFlagRequired("image")

	var uf vmFlags
	update := &cobra.Command{
		Use: "update NAME", Short: "Change a VM's size, disks, network or published ports (only the flags you pass)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ch := cmd.Flags().Changed
			patch := map[string]any{}
			if ch("cpus") {
				patch["cpus"] = uf.cpus
			}
			if ch("memory") {
				patch["memoryMiB"] = uf.memory
			}
			if ch("disk") {
				patch["diskGiB"] = uf.disk
			}
			if ch("disk-volume") {
				disks := []string{}
				if !(len(uf.disks) == 1 && uf.disks[0] == "") {
					disks = uf.disks
				}
				patch["disks"] = disks
			}
			if ch("publish") {
				patch["publish"] = uf.publish
			}
			uf.place.patch(ch, patch)
			if err := a.warnVMRestart(cmd.Context(), args[0], patch); err != nil {
				return err
			}
			return a.update(cmd.Context(), api.KindVM, args[0], patch, uf.wait)
		},
	}
	uf.bindShared(update)

	power := func(action, short string) *cobra.Command {
		var wait bool
		c := &cobra.Command{Use: action + " NAME", Short: short, Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return a.action(cmd.Context(), api.KindVM, args[0], action, wait)
			}}
		c.Flags().BoolVar(&wait, "wait", false, "wait until done")
		return c
	}
	cmd.AddCommand(create, update, power(api.ActionStart, "Start a stopped VM"), power(api.ActionStop, "Shut a VM down (forced off after 60 s)"),
		power(api.ActionRestart, "Restart a VM"), a.vmSSHCmd(), a.vmConsoleCmd())
	return cmd
}

// warnVMRestart says, before sending, when an update restarts a running VM (CON-33's CLI form).
func (a *app) warnVMRestart(ctx context.Context, name string, patch map[string]any) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	cur, err := a.find(ctx, c, api.KindVM, name)
	if err != nil {
		return err
	}
	var before api.VMSpec
	if json.Unmarshal(cur.Spec, &before) != nil || cur.Outputs["power"] != api.PowerRunning {
		return nil
	}
	merged, err := api.MergeSpec(cur.Spec, mustJSON(patch))
	if err != nil {
		return nil
	}
	var after api.VMSpec
	if json.Unmarshal(merged, &after) != nil {
		return nil
	}
	if why := api.VMRestarts(before, after); len(why) > 0 {
		fmt.Fprintf(a.io.Err, "%s will restart to apply the new %s.\n", name, strings.Join(why, ", "))
	}
	return nil
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func (a *app) vmSSHCmd() *cobra.Command {
	return &cobra.Command{
		Use: "ssh NAME [-- COMMAND...]", Short: "Open an SSH session to a VM (runs your ssh)", Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			r, err := a.find(cmd.Context(), c, api.KindVM, args[0])
			if err != nil {
				return err
			}
			addr, _ := r.Outputs["sshAddress"].(string)
			user, _ := r.Outputs["sshUser"].(string)
			host, port, err := net.SplitHostPort(addr)
			if err != nil || user == "" {
				return fmt.Errorf("%s has no SSH address yet; wait until it is ready", args[0])
			}
			if r.Outputs["power"] != api.PowerRunning {
				return fmt.Errorf("%s is stopped; start it with: enclave vm start %s", args[0], args[0])
			}
			// Loopback ports are reused by other VMs over time, so the host key is filed under the VM's ID.
			sshArgs := []string{"-p", port, "-o", "HostKeyAlias=enclave-" + r.ID, "-o", "StrictHostKeyChecking=accept-new", user + "@" + host}
			return a.exec("ssh", append(sshArgs, args[1:]...))
		},
	}
}

// exec runs a program attached to the CLI's streams (IO.Exec in tests).
func (a *app) exec(name string, args []string) error {
	if a.io.Exec != nil {
		return a.io.Exec(name, args)
	}
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = a.io.In, a.io.Out, a.io.Err
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ExitStatus(ee.ExitCode())
		}
		return fmt.Errorf("run %s: %w", name, err)
	}
	return nil
}

func (a *app) vmConsoleCmd() *cobra.Command {
	var tail int
	var follow bool
	cmd := &cobra.Command{
		Use: "console NAME", Short: "Print a VM's boot console (serial output)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			r, err := a.find(cmd.Context(), c, api.KindVM, args[0])
			if err != nil {
				return err
			}
			var last []string
			for {
				lines, err := c.Logs(cmd.Context(), r.ID, 1000)
				if err != nil {
					return err
				}
				msgs := make([]string, len(lines))
				for i, l := range lines {
					msgs[i] = l.Message
				}
				start := newLines(last, msgs)
				if last == nil && tail > 0 && len(msgs)-start > tail {
					start = len(msgs) - tail
				}
				for _, m := range msgs[start:] {
					fmt.Fprintln(a.io.Out, m)
				}
				last = msgs
				if !follow {
					return nil
				}
				select {
				case <-cmd.Context().Done():
					return nil
				case <-time.After(time.Second):
				}
			}
		},
	}
	cmd.Flags().IntVar(&tail, "tail", 200, "how many lines to start with")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new lines")
	return cmd
}

// newLines returns where cur's unseen lines start, given the lines printed last time: the longest
// suffix of prev that is a prefix of cur marks what was printed. A rotated console starts over.
func newLines(prev, cur []string) int {
	for k := min(len(prev), len(cur)); k > 0; k-- {
		match := true
		for i := 0; i < k; i++ {
			if prev[len(prev)-k+i] != cur[i] {
				match = false
				break
			}
		}
		if match {
			return k
		}
	}
	return 0
}

func (a *app) volumeCmd() *cobra.Command {
	cmd := a.kindCmd(api.KindVolume, "volume", []string{"volumes", "vol"}, "Volumes: disks for VMs, folders for apps and jobs")
	var typ, snapshot string
	var size int
	var wait bool
	var cm machineFlags
	create := &cobra.Command{
		Use: "create NAME --type disk|filesystem", Short: "Create a volume, empty or from a snapshot", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.create(cmd.Context(), api.KindVolume, args[0], api.VolumeSpec{Type: typ, SizeGiB: size, Snapshot: snapshot, Placed: cm.placed()}, wait)
		},
	}
	cm.bind(create)
	create.Flags().StringVar(&typ, "type", "", "disk (attached to one VM) or filesystem (mounted into apps and jobs)")
	create.Flags().IntVar(&size, "size", 0, "a disk's size in GiB (1–2048, default 10)")
	create.Flags().StringVar(&snapshot, "snapshot", "", "start from this snapshot")
	create.Flags().BoolVar(&wait, "wait", false, "wait until ready")
	_ = create.MarkFlagRequired("type")
	var usize int
	var uwait bool
	var um machineFlags
	update := &cobra.Command{
		Use: "update NAME [--size GIB] [--machine M]", Short: "Grow a disk (live, while its VM runs), or move the volume and what uses it", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			patch := map[string]any{}
			if cmd.Flags().Changed("size") {
				patch["sizeGiB"] = usize
			}
			um.patch(cmd.Flags().Changed, patch)
			return a.update(cmd.Context(), api.KindVolume, args[0], patch, uwait)
		},
	}
	update.Flags().IntVar(&usize, "size", 0, "new size in GiB; it only grows")
	um.bind(update)
	update.Flags().BoolVar(&uwait, "wait", false, "wait until ready")
	cmd.AddCommand(create, update)
	return cmd
}

func (a *app) snapshotCmd() *cobra.Command {
	cmd := a.kindCmd(api.KindSnapshot, "snapshot", []string{"snapshots", "snap"}, "Snapshots: copies of volumes and VM disks")
	var from string
	var wait bool
	create := &cobra.Command{
		Use: "create NAME --from volume/NAME|vm/NAME", Short: "Copy a volume or a VM's root disk, while in use", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.create(cmd.Context(), api.KindSnapshot, args[0], api.SnapshotSpec{Source: from}, wait)
		},
	}
	create.Flags().StringVar(&from, "from", "", "what to copy: volume/NAME or vm/NAME")
	create.Flags().BoolVar(&wait, "wait", false, "wait until the copy is done")
	_ = create.MarkFlagRequired("from")
	cmd.AddCommand(create)
	return cmd
}

// vmRows are the machine command's VM lines (CLI-28).
func vmRows(m api.Machine) [][]string {
	vms := "available: QEMU " + m.VMs.QEMUVersion + " with " + strings.ToUpper(m.VMs.Accelerator)
	if !m.VMs.Available {
		vms = "UNAVAILABLE — " + m.VMs.Message
	}
	rows := [][]string{{"VMs", vms}, {"CPUs reserved by VMs", strconv.Itoa(m.ReservedCPUs)}}
	var imgs []string
	for _, i := range m.Images {
		imgs = append(imgs, fmt.Sprintf("%s (%s, used by %d)", i.Name, mib(i.SizeBytes), i.UsedBy))
	}
	if len(imgs) == 0 {
		imgs = []string{"none downloaded"}
	}
	return append(rows, []string{"VM images", strings.Join(imgs, "; ")}, []string{"Snapshots", mib(m.SnapshotBytes)})
}
