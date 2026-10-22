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

// v0.4 on real Docker through the built binaries (L5-002 §7): OpenTofu applies a network with a
// firewall-protected cache used by a three-replica app behind a published load balancer; the CLI and
// the engine are checked from outside.
func TestNetworkingStackForRealWithOpenTofu(t *testing.T) {
	requireTools(t, "docker", "tofu")
	s := startServer(t, t.TempDir(), freePort(t))
	token, c := setupAndToken(t, s)
	bin := build(t)
	work := t.TempDir()
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

resource "enclave_network" "net" {
  name   = "e2e-net-%[1]s"
  cidr   = "10.%[2]d.0.0/24"
  subnet = [
    { name = "apps", cidr = "10.%[2]d.0.0/25" },
    { name = "data", cidr = "10.%[2]d.0.128/25" },
  ]
}
resource "enclave_cache" "kv" {
  name           = "e2e-kv-%[1]s"
  max_memory_mib = 64
  network        = enclave_network.net.name
  subnet         = "data"
}
resource "enclave_app" "web" {
  name       = "e2e-web-%[1]s"
  image      = "busybox:1.37"
  port       = 8080
  replicas   = 3
  memory_mib = 64
  network    = enclave_network.net.name
  command    = ["sh", "-c", "mkdir -p /www && hostname > /www/index.html && exec httpd -f -p 8080 -h /www"]
}
resource "enclave_app" "client" {
  name       = "e2e-client-%[1]s"
  image      = "busybox:1.37"
  memory_mib = 64
  network    = enclave_network.net.name
  command    = ["sleep", "3600"]
}
resource "enclave_app" "outsider" {
  name       = "e2e-out-%[1]s"
  image      = "busybox:1.37"
  memory_mib = 64
  network    = enclave_network.net.name
  command    = ["sleep", "3600"]
}
resource "enclave_firewall" "kv" {
  name    = "e2e-kv-fw-%[1]s"
  targets = ["cache/${enclave_cache.kv.name}"]
  ingress = [{ from = ["app/${enclave_app.client.name}"], ports = ["6379"] }]
}
resource "enclave_load_balancer" "web" {
  name    = "e2e-lb-%[1]s"
  port    = 80
  publish = true
  target  = [{ app = enclave_app.web.name }]
  health_check = { type = "http", path = "/" }
}
output "lb" { value = "http://127.0.0.1:${enclave_load_balancer.web.host_port}/" }
output "kv" { value = enclave_cache.kv.private_dns }
`, id, octet)
	_ = os.WriteFile(filepath.Join(work, "main.tf"), []byte(main), 0o600)
	env := append(os.Environ(), "TF_CLI_CONFIG_FILE="+rc, "ENCLAVE_SERVER="+s.url, "ENCLAVE_TOKEN="+token, "TF_IN_AUTOMATION=1")
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

	// The load balancer spreads over the replicas (LB-01 – LB-03).
	lb := strings.Trim(strings.TrimSpace(tofu("output", "-raw", "lb")), `"`)
	seen := map[string]bool{}
	deadline := time.Now().Add(30 * time.Second)
	for len(seen) < 3 && time.Now().Before(deadline) {
		if body := strings.TrimSpace(httpGetQuiet(lb)); body != "" {
			seen[body] = true
		}
	}
	if len(seen) != 3 {
		t.Errorf("the load balancer reached %d replicas, want 3: %v", len(seen), seen)
	}

	// The firewall lets the client reach the cache by its private name, and nobody else (FW-01, NET-07).
	kv := strings.Trim(strings.TrimSpace(tofu("output", "-raw", "kv")), `"`)
	reach := func(app string) bool {
		return exec.Command("docker", "exec", fmt.Sprintf("es-app-%s-r1", app), "nc", "-z", "-w", "3", kv, "6379").Run() == nil
	}
	if !reach("e2e-client-" + id) {
		t.Errorf("the allowed client can't reach %s", kv)
	}
	if reach("e2e-out-" + id) {
		t.Errorf("an app the firewall doesn't allow reached %s", kv)
	}

	// The CLI sees the same resources; a second plan is empty.
	out := mustCLI(t, s, token, "", "lb", "get", "e2e-lb-"+id)
	if !strings.Contains(out, "healthyTargets") {
		t.Errorf("lb get:\n%s", out)
	}
	if again := tofu("plan", "-no-color", "-detailed-exitcode"); !strings.Contains(again, "No changes") {
		t.Errorf("second plan is not empty:\n%s", again)
	}
	if _, err := c.FindResource(context.Background(), api.KindNetwork, "e2e-net-"+id); err != nil {
		t.Errorf("network not visible through the API: %v", err)
	}

	tofu("destroy", "-auto-approve", "-no-color")
	if _, err := c.FindResource(context.Background(), api.KindNetwork, "e2e-net-"+id); !client.IsNotFound(err) {
		t.Errorf("network survived destroy: %v", err)
	}
	if exec.Command("docker", "network", "inspect", "es-net-e2e-net-"+id).Run() == nil {
		t.Error("engine network left behind")
	}
}

// httpGetQuiet returns the body, or "" on any error (the load balancer may still be warming up).
func httpGetQuiet(url string) string {
	cmd := exec.Command("curl", "-s", "-m", "2", url)
	out, err := cmd.Output()
	if err != nil {
		time.Sleep(200 * time.Millisecond)
		return ""
	}
	return string(out)
}
