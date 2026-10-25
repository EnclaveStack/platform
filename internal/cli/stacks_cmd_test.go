package cli_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/engine/fake"
	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
)

// Moving, the catalogue and stacks from the CLI (L2-002 CLI-49 – CLI-52).

func TestCatalogueAndStacksFromTheCLI(t *testing.T) {
	h := newHarness(t)
	h.login()
	if r := h.ok("catalogue", "ls"); !strings.Contains(r.out, "wordpress") || !strings.Contains(r.out, "built in") {
		t.Errorf("catalogue ls: %s", r.out)
	}
	if r := h.ok("catalogue", "show", "wordpress"); !strings.Contains(r.out, "--param domain=") || !strings.Contains(r.out, "database") {
		t.Errorf("catalogue show: %s", r.out)
	}

	h.ok("stack", "create", "blog", "--template", "wordpress", "--param", "domain=blog.localhost", "--wait")
	r := h.ok("stack", "get", "blog")
	if !regexp.MustCompile(`app\s+blog\s+ready`).MatchString(r.out) || !strings.Contains(r.out, "https://blog.localhost") {
		t.Errorf("stack get: %s", r.out)
	}
	if r := h.ok("stack", "ls"); !strings.Contains(r.out, "blog") {
		t.Errorf("stack ls: %s", r.out)
	}
	if r := h.run("app", "delete", "blog", "--yes"); r.code == 0 || !strings.Contains(r.err, "stack") {
		t.Errorf("deleting a member: %d %q", r.code, r.err)
	}
	// Removing the domain is slow (the edge takes its route away on a slow reload, as on a busy machine):
	// --wait still waits until it is gone (CAT-05).
	h.env.Fake.SetEdgeDelay(4 * time.Second)
	h.ok("stack", "update", "blog", "--param", "domain=", "--wait")
	h.env.Fake.SetEdgeDelay(0)
	if r := h.run("get", "domain", "blog-site"); r.code == 0 {
		t.Errorf("the domain survived clearing its parameter: %s", r.out)
	}
	if r := h.run("stack", "create", "x"); r.code == 0 || !strings.Contains(r.err, "--template") {
		t.Errorf("no template: %d %q", r.code, r.err)
	}

	// Your own template, from a file.
	file := filepath.Join(t.TempDir(), "worker.yaml")
	if err := os.WriteFile(file, []byte("name: tiny\ntitle: Tiny\nversion: 1.0.0\nparameters:\n  - { name: key, type: secret, required: true }\nresources:\n  - kind: app\n    name: \"\"\n    spec: { image: nginx, secretEnv: { KEY: \"${param:key}\" } }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := h.ok("catalogue", "add", file); !strings.Contains(r.out, "Added tiny 1.0.0") {
		t.Errorf("catalogue add: %s", r.out)
	}
	h.ok("stack", "create", "small", "--template", "tiny", "--secret-param", "key=s3cret", "--wait")
	if r := h.ok("get", "stack", "small", "-o", "json"); strings.Contains(r.out, "s3cret") {
		t.Error("the secret parameter is in the stack's JSON")
	}
	if r := h.run("catalogue", "remove", "tiny", "--yes"); r.code == 0 {
		t.Error("removed a template in use")
	}
	h.ok("stack", "delete", "small", "--yes", "--wait")
	h.ok("catalogue", "remove", "tiny", "--yes")
}

func TestMoveAndDrainFromTheCLI(t *testing.T) {
	h := newHarness(t)
	h.login()
	admin, _ := h.env.Token(t, "")
	rack1 := h.env.AddAgent(t, admin, "rack-1", map[string]string{"zone": "a"})
	h.env.WaitMachine(t, admin, "rack-1", api.MachineOnline)

	h.ok("db", "create", "orders", "--machine", testenv.MainName(), "--wait")
	if r := h.run("move", "orders", "--to", "rack-1"); r.code == 0 || !strings.Contains(r.err, "KIND/NAME") {
		t.Errorf("move without a kind: %d %q", r.code, r.err)
	}
	// Removing the old copy is slow, as on a busy machine: --wait still waits for the move, not just ready.
	h.env.Fake.SetFault("orders", fake.Fault{DeleteDelay: 2 * time.Second})
	r := h.ok("move", "database/orders", "--to", "rack-1", "--wait")
	if !strings.Contains(r.out, "Moved to rack-1") {
		t.Errorf("move: %s", r.out)
	}
	if rack1.Driver.EnsureCalls("orders") == 0 {
		t.Error("orders never ran on rack-1")
	}
	if r := h.ok("ls", "--machine", "rack-1"); !strings.Contains(r.out, "orders") {
		t.Errorf("after the move: %s", r.out)
	}

	// sessions only asks for zone=a, so a drain moves it; orders is pinned and stays.
	h.ok("cache", "create", "sessions", "--machine-label", "zone=a", "--wait")
	rack2 := h.env.AddAgent(t, admin, "rack-2", map[string]string{"zone": "a"})
	h.env.WaitMachine(t, admin, "rack-2", api.MachineOnline)
	if r := h.run("machine", "drain", testenv.MainName()); r.code == 0 {
		t.Error("drained main")
	}
	// Removing the old copy on rack-1 is slow: --wait must wait for the move to finish, not just for sessions
	// to leave rack-1's list (before, it said "Drained." while the move was still going).
	rack1.Driver.SetFault("sessions", fake.Fault{DeleteDelay: 2 * time.Second})
	r = h.ok("machine", "drain", "rack-1", "--wait")
	if !strings.Contains(r.out, "database/orders is pinned there") || !strings.Contains(r.out, "Moved to rack-2") || !strings.Contains(r.out, "Drained.") {
		t.Errorf("drain: %s", r.out)
	}
	sessions, err := admin.FindResource(context.Background(), api.KindCache, "sessions")
	if err != nil || !rack2.Driver.Running(sessions.ID) || sessions.Status.Phase != api.PhaseReady || sessions.Outputs["machine"] != "rack-2" {
		t.Errorf("sessions when drain --wait returned: running on rack-2 %v, %s on %v (%v)", rack2.Driver.Running(sessions.ID), sessions.Status.Phase, sessions.Outputs["machine"], err)
	}
	if r := h.ok("machine", "ls"); !strings.Contains(r.out, "not schedulable") {
		t.Errorf("after drain: %s", r.out)
	}
	// An alert on the machine going offline (MCH-06).
	h.ok("channel", "create", "ops", "--webhook", "https://hooks.example.com/x", "--wait")
	h.ok("alert", "create", "rack-down", "--machine-offline", "rack-1", "--for", "2m", "--channel", "ops", "--wait")
	if r := h.ok("get", "alert", "rack-down", "-o", "json"); !strings.Contains(r.out, `"machine/rack-1"`) || !strings.Contains(r.out, `"forMinutes": 2`) {
		t.Errorf("the offline alert: %s", r.out)
	}
}
