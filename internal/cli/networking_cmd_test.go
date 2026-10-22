package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/enclavestack/platform/internal/cli"
	"github.com/enclavestack/platform/pkg/api"
)

// v0.4 commands (L2-002 CLI-18 – CLI-23) against a real control plane on the fake engine.

func (h *harness) mustRun(args ...string) result {
	h.t.Helper()
	r := h.run(args...)
	if r.code != 0 {
		h.t.Fatalf("%v: exit %d\nout: %s\nerr: %s", args, r.code, r.out, r.err)
	}
	return r
}

func (h *harness) spec(kind, name string, v any) {
	h.t.Helper()
	r := h.mustRun("-o", "json", kind, "get", name)
	var res api.Resource
	if err := json.Unmarshal([]byte(r.out), &res); err != nil {
		h.t.Fatalf("decode %s: %v", r.out, err)
	}
	if err := json.Unmarshal(res.Spec, v); err != nil {
		h.t.Fatal(err)
	}
}

func TestNetworkingFromTheCLI(t *testing.T) {
	h := newHarness(t)
	h.login()

	// Networks and placement (CLI-18).
	h.mustRun("network", "create", "backend", "--cidr", "10.40.0.0/16", "--subnet", "apps=10.40.2.0/24", "--subnet", "data=10.40.1.0/24", "--wait")
	var ns api.NetworkSpec
	h.spec("network", "backend", &ns)
	if len(ns.Subnets) != 2 || ns.Subnets[1] != (api.Subnet{Name: "data", CIDR: "10.40.1.0/24"}) || !ns.HasInternet() {
		t.Errorf("network spec %+v", ns)
	}
	if r := h.run("network", "create", "bad", "--cidr", "10.40.0.0/16", "--subnet", "nocidr"); r.code != cli.ExitUsage {
		t.Errorf("bad --subnet: %d %s", r.code, r.err)
	}
	h.mustRun("db", "create", "orders", "--network", "backend", "--subnet", "data", "--wait")
	if r := h.mustRun("db", "get", "orders"); !strings.Contains(r.out, "orders.database.internal") || !strings.Contains(r.out, "10.40.1.") {
		t.Errorf("db get: %s", r.out)
	}
	h.mustRun("app", "create", "api", "--image", "nginx:1.27-alpine", "--port", "8080", "--network", "backend", "--replicas", "3", "--wait")
	h.mustRun("app", "create", "web", "--image", "nginx:1.27-alpine", "--port", "80", "--wait")
	h.mustRun("job", "create", "tick", "--schedule", "@daily", "--image", "busybox:1.37", "--network", "backend", "--wait", "--", "true")
	var js api.JobSpec
	h.spec("job", "tick", &js)
	if js.Network != "backend" {
		t.Errorf("job spec %+v", js)
	}

	// Firewalls (CLI-19).
	h.mustRun("firewall", "create", "db-access", "--target", "database/orders", "--allow", "tcp 5432 from app/api,10.9.0.0/16", "--allow", "icmp from any", "--wait")
	var fw api.FirewallSpec
	h.spec("fw", "db-access", &fw)
	if len(fw.Ingress) != 2 || fw.Ingress[0].Protocol != "tcp" || fw.Ingress[0].Ports[0] != "5432" || len(fw.Ingress[0].From) != 2 || fw.Ingress[1].Protocol != "icmp" {
		t.Errorf("firewall spec %+v", fw)
	}
	h.mustRun("firewall", "update", "db-access", "--egress", "deny", "--allow-out", "tcp 443 to any", "--wait")
	h.spec("fw", "db-access", &fw)
	if fw.EgressDefault != "deny" || len(fw.Egress) != 1 || fw.Egress[0].To[0] != "any" || len(fw.Ingress) != 2 {
		t.Errorf("updated firewall %+v", fw)
	}
	if r := h.run("firewall", "create", "bad", "--target", "app/web", "--allow", "tcp 80 app/api"); r.code != cli.ExitUsage {
		t.Errorf("malformed rule: %d %s", r.code, r.err)
	}

	// Load balancers (CLI-20).
	h.mustRun("lb", "create", "api-lb", "--port", "80", "--target", "api:8080@3", "--algorithm", "least_conn", "--health", "http:/healthz", "--publish", "--wait")
	var lb api.LoadBalancerSpec
	h.spec("lb", "api-lb", &lb)
	if lb.Targets[0] != (api.LoadBalancerTarget{App: "api", Port: 8080, Weight: 3}) || lb.HealthCheck.Path != "/healthz" || !lb.Publish || lb.Algorithm != "least_conn" {
		t.Errorf("lb spec %+v", lb)
	}
	if r := h.mustRun("lb", "get", "api-lb"); !strings.Contains(r.out, "healthyTargets") {
		t.Errorf("lb get: %s", r.out)
	}
	if r := h.run("app", "update", "web", "--replicas", "2", "--publish"); r.code == 0 {
		t.Error("publish with replicas accepted")
	}

	// Zones and records (CLI-21), domains through DNS (CLI-22).
	h.stdin = `{"apiToken":"t"}` + "\n"
	h.mustRun("secret", "create", "cf-token", "--from-stdin", "--wait")
	h.mustRun("zone", "create", "example", "--provider", "cloudflare", "--zone", "example.com", "--credentials", "cf-token", "--wait")
	h.mustRun("machine", "set", "--public-address", "203.0.113.7")
	h.mustRun("record", "create", "apex", "--zone", "example", "--name", "@", "--type", "A", "--machine", "--wait")
	h.mustRun("record", "create", "txt", "--zone", "example", "--name", "_note", "--type", "TXT", "--value", "hello", "--ttl", "120", "--wait")
	if got := h.env.DNS.Lookup("example.com", "A"); got == nil || got.Values[0] != "203.0.113.7" {
		t.Errorf("apex record at the provider: %+v", got)
	}
	h.mustRun("record", "update", "txt", "--value", "hello", "--value", "world", "--wait")
	if got := h.env.DNS.Lookup("_note.example.com", "TXT"); got == nil || len(got.Values) != 2 {
		t.Errorf("updated TXT %+v", got)
	}
	h.mustRun("domain", "create", "wild", "--hostname", "*.example.com", "--app", "web", "--zone", "example", "--wait")
	if r := h.mustRun("domain", "get", "wild"); !strings.Contains(r.out, "issued") {
		t.Errorf("wildcard domain: %s", r.out)
	}

	// ls and delete cover the new kinds (CLI-23).
	for _, kind := range []string{"network", "firewall", "loadbalancer", "zone", "record"} {
		if r := h.mustRun("ls", "--kind", kind); strings.Count(r.out, "\n") < 2 {
			t.Errorf("ls --kind %s: %s", kind, r.out)
		}
	}
	if r := h.run("network", "delete", "backend", "--yes"); r.code != cli.ExitConflict || !strings.Contains(r.err, "orders") {
		t.Errorf("deleting a network in use: %d %s", r.code, r.err)
	}
	h.mustRun("fw", "delete", "db-access", "--yes", "--wait")
}
