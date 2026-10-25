package cli_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
)

// Machines from the CLI (L2-002 CLI-46 – CLI-48).

func TestMachinesFromTheCLI(t *testing.T) {
	h := newHarness(t)
	h.login()
	add := h.ok("machine", "add", "rack-1", "--label", "disk=ssd")
	// The test server listens on loopback, which another computer can't reach: the command says so.
	m := regexp.MustCompile(`enclave agent join <this machine's address> --code (\S+)`).FindStringSubmatch(add.out)
	if m == nil || !strings.Contains(add.out, "--listen 0.0.0.0:7070") || !strings.Contains(add.out, "51820/udp") {
		t.Fatalf("join command: %q", add.out)
	}
	if r := h.run("machine", "add", "rack-1"); r.code == 0 || !strings.Contains(r.err, "already exists") {
		t.Errorf("a duplicate: %d %q", r.code, r.err)
	}
	if r := h.run("machine", "add", "rack-2", "--label", "nokey"); r.code == 0 {
		t.Error("a label without = accepted")
	}

	a := h.env.JoinAgent(t, "rack-1", m[1])
	admin, _ := h.env.Token(t, "")
	h.env.WaitMachine(t, admin, "rack-1", api.MachineOnline)
	if r := h.ok("machine", "ls"); !strings.Contains(r.out, "rack-1") || !strings.Contains(r.out, "online") || !strings.Contains(r.out, "disk=ssd") {
		t.Errorf("machine ls: %s", r.out)
	}
	if r := h.ok("machine", "get", "rack-1"); !strings.Contains(r.out, "127.0.0.1") || !strings.Contains(r.out, "fake fake-1.0") {
		t.Errorf("machine get: %s", r.out)
	}

	// --machine places, and ls says where (CLI-48).
	h.ok("db", "create", "orders", "--machine", "rack-1", "--wait")
	h.ok("cache", "create", "sessions", "--machine-label", "disk=ssd", "--wait")
	h.ok("app", "create", "web", "--image", "nginx", "--port", "80", "--machine", testenv.MainName(), "--wait")
	if a.Driver.EnsureCalls("orders") == 0 || a.Driver.EnsureCalls("sessions") == 0 || a.Driver.EnsureCalls("web") != 0 {
		t.Errorf("rack-1 ran orders %d, sessions %d, web %d times", a.Driver.EnsureCalls("orders"), a.Driver.EnsureCalls("sessions"), a.Driver.EnsureCalls("web"))
	}
	r := h.ok("ls")
	if !strings.Contains(r.out, "MACHINE") || !regexp.MustCompile(`orders\s+\S+\s+rack-1`).MatchString(r.out) || !regexp.MustCompile(`web\s+\S+\s+`+regexp.QuoteMeta(testenv.MainName())).MatchString(r.out) {
		t.Errorf("ls: %s", r.out)
	}
	if r := h.ok("ls", "--machine", "rack-1"); !strings.Contains(r.out, "orders") || !strings.Contains(r.out, "sessions") || strings.Contains(r.out, "web") {
		t.Errorf("ls --machine: %s", r.out)
	}
	if r := h.run("db", "create", "lost", "--machine", "nowhere"); r.code == 0 || !strings.Contains(r.err, "no machine named nowhere") {
		t.Errorf("an unknown machine: %d %q", r.code, r.err)
	}

	h.ok("machine", "update", "rack-1", "--schedulable=false")
	if r := h.ok("machine", "ls"); !strings.Contains(r.out, "not schedulable") {
		t.Errorf("after cordon: %s", r.out)
	}
	if r := h.ok("machine", "events", "rack-1"); !strings.Contains(r.out, "joined") || !strings.Contains(r.out, "not schedulable") {
		t.Errorf("events: %s", r.out)
	}
	if r := h.run("machine", "remove", "rack-1", "--yes"); r.code == 0 || !strings.Contains(r.err, "still runs 2 resources") || !strings.Contains(r.err, "database/orders") {
		t.Errorf("remove with resources: %d %q", r.code, r.err)
	}
	if r := h.ok("agent", "status", "--state-dir", a.StateDir); !strings.Contains(r.out, "rack-1") || !strings.Contains(r.out, a.ID) {
		t.Errorf("agent status: %s", r.out)
	}
	if r := h.run("agent", "status", "--state-dir", t.TempDir()); r.code == 0 || !strings.Contains(r.err, "enclave agent join") {
		t.Errorf("status without joining: %d %q", r.code, r.err)
	}
}
