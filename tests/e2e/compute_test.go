//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// v0.5 through the built binaries (L5-002 §8): OpenTofu applies a filesystem volume served by an app, a
// snapshot of it and a volume restored from it; when this machine can run VMs, also a VM in a network
// with a data disk and a first-boot web server. Everything is checked from outside: the engine, SSH, a
// container in the network, and a second plan.
func TestComputeStackForRealWithOpenTofu(t *testing.T) {
	requireTools(t, "docker", "tofu", "ssh", "ssh-keygen")
	s := startServer(t, t.TempDir(), freePort(t))
	token, c := setupAndToken(t, s)
	m, err := c.Machine(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	withVMs := m.VMs.Available
	if !withVMs {
		t.Logf("VMs can't run here (%s): checking volumes and snapshots only", m.VMs.Message)
	}
	bin := build(t)
	work := t.TempDir()
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "e2e", "-f", filepath.Join(work, "key")).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v %s", err, out)
	}
	pub, _ := os.ReadFile(filepath.Join(work, "key.pub"))
	rc := filepath.Join(work, "dev.tofurc")
	_ = os.WriteFile(rc, []byte(fmt.Sprintf("provider_installation {\n  dev_overrides { \"enclavestack/enclave\" = %q }\n  direct {}\n}\n", bin)), 0o600)
	id, octet := suffix(), 100+rand.IntN(150)
	main := fmt.Sprintf(`
terraform {
  required_providers {
    enclave = { source = "enclavestack/enclave" }
  }
}
provider "enclave" {}
variable "vms" { type = bool }

resource "enclave_volume" "files" {
  name = "e2e-files-%[1]s"
  type = "filesystem"
}
resource "enclave_app" "web" {
  name       = "e2e-web-%[1]s"
  image      = "busybox:1.37"
  memory_mib = 64
  command    = ["sh", "-c", "echo kept > /data/index.html && sleep 3600"]
  volumes    = [{ volume = enclave_volume.files.name, path = "/data" }]
}
resource "enclave_snapshot" "files" {
  name       = "e2e-snap-%[1]s"
  source     = "volume/${enclave_volume.files.name}"
  depends_on = [enclave_app.web]
}
resource "enclave_volume" "restored" {
  name     = "e2e-restored-%[1]s"
  type     = "filesystem"
  snapshot = enclave_snapshot.files.name
}

resource "enclave_network" "net" {
  count = var.vms ? 1 : 0
  name  = "e2e-vmnet-%[1]s"
  cidr  = "10.%[2]d.0.0/24"
}
resource "enclave_volume" "disk" {
  count    = var.vms ? 1 : 0
  name     = "e2e-disk-%[1]s"
  type     = "disk"
  size_gib = 2
}
resource "enclave_vm" "worker" {
  count     = var.vms ? 1 : 0
  name      = "e2e-vm-%[1]s"
  image     = "debian-12"
  ssh_keys  = [%[3]q]
  memory_mib = 1024
  network   = enclave_network.net[0].name
  disks     = [enclave_volume.disk[0].name]
  user_data = "#!/bin/sh\nmkdir -p /srv && hostname > /srv/index.html && cd /srv && nohup python3 -m http.server 9000 >/dev/null 2>&1 &\n"
  timeouts  = { create = "15m" }
}
resource "enclave_app" "client" {
  count      = var.vms ? 1 : 0
  name       = "e2e-client-%[1]s"
  image      = "busybox:1.37"
  memory_mib = 64
  network    = enclave_network.net[0].name
  command    = ["sleep", "3600"]
}
output "ssh" { value = var.vms ? enclave_vm.worker[0].ssh_address : "" }
`, id, octet, strings.TrimSpace(string(pub)))
	_ = os.WriteFile(filepath.Join(work, "main.tf"), []byte(main), 0o600)
	env := append(os.Environ(), "TF_CLI_CONFIG_FILE="+rc, "ENCLAVE_SERVER="+s.url, "ENCLAVE_TOKEN="+token, "TF_IN_AUTOMATION=1", fmt.Sprintf("TF_VAR_vms=%t", withVMs))
	tofu := func(args ...string) string {
		cmd := exec.Command("tofu", args...)
		cmd.Dir, cmd.Env = work, env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("tofu %s: %v\n%s\nserver log:\n%s", strings.Join(args, " "), err, out, tail(s.logs.String(), 40))
		}
		return string(out)
	}
	t.Cleanup(func() {
		cmd := exec.Command("tofu", "destroy", "-auto-approve", "-no-color")
		cmd.Dir, cmd.Env = work, env
		_ = cmd.Run()
	})
	tofu("apply", "-auto-approve", "-no-color")

	// The restored volume holds what the app wrote before the snapshot (VOL-08, SNP-01).
	restored := getJSON(t, s, token, "volume", "e2e-restored-"+id)
	out, err := exec.Command("docker", "run", "--rm", "-v", "es-vol-"+restored.ID+":/r:ro", "busybox:1.37", "cat", "/r/index.html").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "kept" {
		t.Errorf("restored volume reads %q (%v)", out, err)
	}

	if withVMs {
		vm := getJSON(t, s, token, "vm", "e2e-vm-"+id)
		addr := strings.Trim(strings.TrimSpace(tofu("output", "-raw", "ssh")), `"`)
		host, port, _ := strings.Cut(addr, ":")
		ssh := func(cmd string) (string, error) {
			out, err := exec.Command("ssh", "-i", filepath.Join(work, "key"), "-p", port, "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null",
				"-o", "LogLevel=ERROR", "-o", "ConnectTimeout=5", "-o", "BatchMode=yes", "enclave@"+host, cmd).CombinedOutput()
			return strings.TrimSpace(string(out)), err
		}
		var hostname string
		deadline := time.Now().Add(3 * time.Minute)
		for time.Now().Before(deadline) {
			if hostname, err = ssh("hostname"); err == nil {
				break
			}
			time.Sleep(3 * time.Second)
		}
		if hostname != "e2e-vm-"+id {
			t.Fatalf("ssh hostname %q (%v)\nconsole:\n%s", hostname, err, mustCLI(t, s, token, "", "vm", "console", "e2e-vm-"+id))
		}
		if out, err := ssh("ls /dev/disk/by-id/ | grep -c virtio-e2e-disk"); err != nil || out != "1" {
			t.Errorf("data disk inside the VM: %q %v", out, err)
		}
		// A container in the VM's network reaches it by its private name (VMN-02).
		deadline = time.Now().Add(time.Minute)
		var body []byte
		for time.Now().Before(deadline) {
			body, err = exec.Command("docker", "exec", "es-app-e2e-client-"+id+"-r1", "wget", "-qO-", "-T", "3", "http://e2e-vm-"+id+".vm.internal:9000/").CombinedOutput()
			if err == nil {
				break
			}
			time.Sleep(2 * time.Second)
		}
		if strings.TrimSpace(string(body)) != "e2e-vm-"+id {
			t.Errorf("client → VM over private DNS: %q %v", body, err)
		}
		if vm.Outputs["power"] != api.PowerRunning || vm.Outputs["privateDns"] != "e2e-vm-"+id+".vm.internal" {
			t.Errorf("VM outputs %v", vm.Outputs)
		}
	}

	// A second plan is empty; destroy leaves nothing behind.
	if again := tofu("plan", "-no-color"); !strings.Contains(again, "No changes") {
		t.Errorf("second plan is not empty:\n%s", again)
	}
	tofu("destroy", "-auto-approve", "-no-color")
	if _, err := c.FindResource(context.Background(), api.KindVolume, "e2e-files-"+id); !client.IsNotFound(err) {
		t.Errorf("volume survived destroy: %v", err)
	}
	if exec.Command("docker", "volume", "inspect", "es-vol-"+restored.ID).Run() == nil {
		t.Error("engine volume left behind")
	}
	if withVMs {
		if exec.Command("docker", "inspect", "es-vm-e2e-vm-"+id).Run() == nil {
			t.Error("VM link container left behind")
		}
		if entries, _ := os.ReadDir(filepath.Join(s.dataDir, "vm")); len(entries) != 0 {
			t.Errorf("VM directories left behind: %v", entries)
		}
	}
}
