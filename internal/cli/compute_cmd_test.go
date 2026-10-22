package cli_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/enclavestack/platform/internal/cli"
	"github.com/enclavestack/platform/pkg/api"
)

// v0.5 commands (L2-002 CLI-24 – CLI-28) against a real control plane on the fake engine and hypervisor.

const cliKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEXTqZbAPryM1c8TBIUjg6rtRL1Dfr2uR7E954v/Icd4 test@enclave"

func TestVolumesAndSnapshotsFromTheCLI(t *testing.T) {
	h := newHarness(t)
	h.login()

	h.mustRun("volume", "create", "uploads", "--type", "filesystem", "--wait")
	h.mustRun("app", "create", "web", "--image", "nginx:1.27", "--port", "80", "--volume", "uploads:/usr/share/nginx/html", "--volume", "uploads:/ro:ro", "--wait")
	var as api.AppSpec
	h.spec("app", "web", &as)
	if len(as.Volumes) != 2 || as.Volumes[1] != (api.VolumeMount{Volume: "uploads", Path: "/ro", ReadOnly: true}) {
		t.Errorf("app volumes %+v", as.Volumes)
	}
	if r := h.run("app", "update", "web", "--volume", "uploads"); r.code != cli.ExitUsage {
		t.Errorf("a mount without a path: %d %s", r.code, r.err)
	}
	h.mustRun("app", "update", "web", "--volume", "", "--wait")
	var cleared api.AppSpec
	h.spec("app", "web", &cleared)
	if len(cleared.Volumes) != 0 {
		t.Errorf("--volume \"\" should clear: %+v", cleared.Volumes)
	}
	h.mustRun("job", "create", "thumbs", "--schedule", "@daily", "--image", "busybox:1.37", "--volume", "uploads:/in:ro", "--wait", "--", "ls", "/in")
	var js api.JobSpec
	h.spec("job", "thumbs", &js)
	if len(js.Volumes) != 1 || !js.Volumes[0].ReadOnly {
		t.Errorf("job volumes %+v", js.Volumes)
	}

	h.mustRun("snapshot", "create", "before", "--from", "volume/uploads", "--wait")
	if r := h.mustRun("snap", "get", "before"); !strings.Contains(r.out, "filesystem") {
		t.Errorf("snapshot get: %s", r.out)
	}
	h.mustRun("volume", "create", "restored", "--type", "filesystem", "--snapshot", "before", "--wait")
	if r := h.mustRun("vol", "ls"); !strings.Contains(r.out, "restored") || !strings.Contains(r.out, "uploads") {
		t.Errorf("volume ls: %s", r.out)
	}

	h.mustRun("volume", "create", "data", "--type", "disk", "--size", "20", "--wait")
	h.mustRun("volume", "update", "data", "--size", "30", "--wait")
	if r := h.run("volume", "update", "data", "--size", "10"); r.code == 0 || !strings.Contains(r.err, "only grow") {
		t.Errorf("shrinking: %d %s", r.code, r.err)
	}
	if r := h.run("volume", "delete", "uploads", "--yes"); r.code != cli.ExitConflict {
		t.Errorf("deleting a mounted volume: %d %s", r.code, r.err)
	}
}

func TestVMsFromTheCLI(t *testing.T) {
	h := newHarness(t)
	h.login()
	keyFile := filepath.Join(t.TempDir(), "id.pub")
	if err := os.WriteFile(keyFile, []byte(cliKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "init.sh")
	_ = os.WriteFile(script, []byte("#!/bin/sh\necho ready\n"), 0o600)

	if r := h.mustRun("machine"); !strings.Contains(r.out, "VMs") || !strings.Contains(r.out, "available") {
		t.Errorf("machine: %s", r.out)
	}
	h.mustRun("volume", "create", "data", "--type", "disk", "--wait")
	h.mustRun("vm", "create", "worker", "--image", "debian-12", "--ssh-key", keyFile, "--cpus", "2", "--memory", "2048",
		"--user-data", script, "--disk-volume", "data", "--publish", "8080", "--wait")
	var vs api.VMSpec
	h.spec("vm", "worker", &vs)
	if vs.CPUs != 2 || vs.SSHKeys[0] != cliKey || vs.UserData != "#!/bin/sh\necho ready\n" || !slices.Equal(vs.Disks, []string{"data"}) || !slices.Equal(vs.Publish, []int{8080}) {
		t.Errorf("VM spec %+v", vs)
	}
	if r := h.mustRun("vm", "get", "worker"); !strings.Contains(r.out, "worker.vm.internal") || !strings.Contains(r.out, "127.0.0.1:") {
		t.Errorf("vm get: %s", r.out)
	}

	// ssh runs the system ssh against the VM's address, its user, and a host key filed under the VM.
	var ran []string
	h.exec = func(name string, args []string) error {
		ran = append([]string{name}, args...)
		return nil
	}
	h.mustRun("vm", "ssh", "worker", "--", "uname", "-a")
	line := strings.Join(ran, " ")
	if !strings.HasPrefix(line, "ssh -p ") || !strings.Contains(line, "enclave@127.0.0.1 uname -a") || !strings.Contains(line, "HostKeyAlias=enclave-vm_") {
		t.Errorf("ssh command: %s", line)
	}
	if r := h.mustRun("vm", "console", "worker"); !strings.Contains(r.out, "login:") {
		t.Errorf("console: %s", r.out)
	}

	// An update that restarts says so first; keys can't change.
	r := h.mustRun("vm", "update", "worker", "--memory", "4096", "--wait")
	if !strings.Contains(r.err, "worker will restart to apply the new memory") {
		t.Errorf("restart warning: %q", r.err)
	}
	if r := h.mustRun("vm", "update", "worker", "--disk", "20", "--wait"); strings.Contains(r.err, "restart") {
		t.Errorf("growing the root disk warned of a restart: %q", r.err)
	}
	h.mustRun("vm", "stop", "worker", "--wait")
	if r := h.run("vm", "ssh", "worker"); r.code == 0 || !strings.Contains(r.err, "vm start worker") {
		t.Errorf("ssh to a stopped VM: %d %s", r.code, r.err)
	}
	h.mustRun("vm", "start", "worker", "--wait")
	h.mustRun("vm", "restart", "worker", "--wait")

	// ssh's own exit code comes back.
	h.exec = func(string, []string) error { return cli.ExitStatus(255) }
	if r := h.run("vm", "ssh", "worker"); r.code != 255 {
		t.Errorf("ssh exit code %d", r.code)
	}

	h.mustRun("snapshot", "create", "worker-1", "--from", "vm/worker", "--wait")
	if r := h.run("vm", "create", "small", "--image", "snapshot/worker-1", "--ssh-key", cliKey); r.code == 0 || !strings.Contains(r.err, "at least that big") {
		t.Errorf("a root disk smaller than its snapshot: %d %s", r.code, r.err)
	}
	h.mustRun("vm", "create", "clone", "--image", "snapshot/worker-1", "--ssh-key", cliKey, "--disk", "20", "--wait")
	if r := h.run("vm", "create", "other", "--image", "debian-12", "--ssh-key", cliKey, "--disk-volume", "data"); r.code != cli.ExitConflict {
		t.Errorf("a second VM on the disk: %d %s", r.code, r.err)
	}
	if r := h.run("vm", "create", "nokey", "--image", "debian-12", "--ssh-key", "/nonexistent.pub"); r.code != cli.ExitUsage {
		t.Errorf("a missing key file: %d %s", r.code, r.err)
	}
	h.mustRun("vm", "delete", "worker", "--yes", "--wait")
	h.mustRun("volume", "delete", "data", "--yes", "--wait")
}
