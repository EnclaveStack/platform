package cli_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/enclavestack/platform/internal/cli"
)

// Public access from the CLI (L2-002 CLI-56 – CLI-58, L2-014).

func TestTunnelsFromTheCLI(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.ok("app", "create", "web", "--image", "nginx:1.27-alpine", "--port", "80", "--wait")

	if r := h.run("tunnel", "create", "demo", "--app", "web"); r.code != cli.ExitUsage || !strings.Contains(r.err, "--provider") {
		t.Errorf("no provider: %d %q", r.code, r.err)
	}
	r := h.ok("tunnel", "create", "demo", "--provider", "quick", "--app", "web", "--wait")
	if !regexp.MustCompile(`https://fake-\S+\.trycloudflare\.com`).MatchString(r.out) {
		t.Fatalf("quick link address: %q", r.out)
	}
	if r := h.ok("get", "tunnel", "demo", "-o", "json"); !strings.Contains(r.out, `"gate": "signin"`) || !strings.Contains(r.out, `"connection": "connected"`) {
		t.Errorf("get tunnel: %s", r.out)
	}
	if r := h.ok("tunnels", "list"); !strings.Contains(r.out, "demo") || !strings.Contains(r.out, ".trycloudflare.com") {
		t.Errorf("tunnel ls: %s", r.out)
	}

	// Opening it to everyone asks first; off a terminal it needs --yes (GATE-05).
	if r := h.run("tunnel", "update", "demo", "--gate", "none"); r.code != cli.ExitUsage || !strings.Contains(r.err, "--yes") {
		t.Errorf("--gate none without --yes: %d %q", r.code, r.err)
	}
	h.interactive, h.stdin = true, "n\n"
	if r := h.run("tunnel", "update", "demo", "--gate", "none"); r.code == 0 || !strings.Contains(r.err, "cancelled") {
		t.Errorf("--gate none declined: %d %q", r.code, r.err)
	}
	h.interactive = false
	h.ok("tunnel", "update", "demo", "--gate", "none", "--gate-open-path", "/webhooks/", "--yes", "--wait")
	if r := h.ok("get", "tunnel", "demo", "-o", "json"); !strings.Contains(r.out, `"gate": "none"`) || !strings.Contains(r.out, "/webhooks/") {
		t.Errorf("gate after update: %s", r.out)
	}

	// The password gate names a secret; the server checks it exists.
	if r := h.run("tunnel", "create", "pw-link", "--provider", "quick", "--app", "web", "--gate", "password"); r.code == 0 || !strings.Contains(r.err, "passwordSecret") {
		t.Errorf("password gate without a secret: %d %q", r.code, r.err)
	}
	h.stdin = `{"username":"team","password":"correct horse"}` + "\n"
	h.ok("secret", "create", "stage-pass", "--from-stdin", "--wait")
	h.ok("tunnel", "create", "pw-link", "--provider", "quick", "--app", "web", "--gate-password-secret", "stage-pass", "--wait")
	if r := h.ok("get", "tunnel", "pw-link", "-o", "json"); !strings.Contains(r.out, `"gate": "password"`) || strings.Contains(r.out, "correct horse") {
		t.Errorf("--gate-password-secret alone picks the password gate, and never shows it: %s", r.out)
	}

	// Domain tunnels carry no gate of their own; the domain does.
	if r := h.run("tunnel", "create", "edge", "--provider", "relay", "--machine", "vps1", "--gate", "signin"); r.code != cli.ExitUsage || !strings.Contains(r.err, "app links") {
		t.Errorf("--gate on a domain tunnel: %d %q", r.code, r.err)
	}
	h.env.DNS.AddZone("example.com")
	h.env.Cloudflare.AddZone("example.com", "0123456789abcdef0123456789abcdef")
	h.stdin = `{"apiToken":"t"}` + "\n"
	h.ok("secret", "create", "cf-token", "--from-stdin", "--wait")
	h.ok("zone", "create", "example", "--provider", "cloudflare", "--zone", "example.com", "--credentials", "cf-token", "--wait")
	h.ok("tunnel", "create", "home", "--provider", "cloudflare", "--credentials", "cf-token", "--account-id", "0123456789abcdef0123456789abcdef", "--wait")
	r = h.ok("domain", "create", "stage", "--hostname", "stage.example.com", "--app", "web", "--zone", "example", "--tunnel", "home",
		"--access-email", "sunny@example.com", "--wait")
	if !strings.Contains(r.out, "https://stage.example.com") {
		t.Fatalf("public domain: %q", r.out)
	}
	if _, ok := h.env.Cloudflare.AccessApps()["stage.example.com"]; !ok {
		t.Errorf("no Cloudflare Access application: %+v", h.env.Cloudflare.AccessApps())
	}
	h.ok("domain", "update", "stage", "--access-email", "", "--gate", "password", "--gate-password-secret", "stage-pass", "--wait")
	if len(h.env.Cloudflare.AccessApps()) != 0 {
		t.Errorf("Cloudflare Access left on: %+v", h.env.Cloudflare.AccessApps())
	}
	if r := h.ok("get", "domain", "stage", "-o", "json"); !strings.Contains(r.out, `"mode": "password"`) {
		t.Errorf("domain gate: %s", r.out)
	}
	if r := h.run("domain", "create", "local", "--hostname", "web.localhost", "--app", "web", "--gate", "signin"); r.code == 0 || !strings.Contains(r.err, "gate") {
		t.Errorf("a gate on a domain that isn't public: %d %q", r.code, r.err)
	}
	if r := h.run("delete", "tunnel", "home", "--yes"); r.code != cli.ExitConflict || !strings.Contains(r.err, "domain/stage") {
		t.Errorf("deleting a used tunnel: %d %q", r.code, r.err)
	}
	h.ok("delete", "tunnel", "demo", "--yes", "--wait")
}

func TestEdgeMachineFromTheCLI(t *testing.T) {
	h := newHarness(t)
	h.login()
	if r := h.run("machine", "add", "vps1", "--address", "203.0.113.9"); r.code != cli.ExitUsage || !strings.Contains(r.err, "--edge") {
		t.Errorf("--address without --edge: %d %q", r.code, r.err)
	}
	r := h.ok("machine", "add", "vps1", "--edge", "--address", "203.0.113.9")
	if !regexp.MustCompile(`enclave agent join --edge --code \S+`).MatchString(r.out) || !strings.Contains(r.out, "443/tcp") {
		t.Fatalf("edge join command: %q", r.out)
	}
	if r := h.ok("machine", "get", "vps1"); !strings.Contains(r.out, "edge (only relays visitors") {
		t.Errorf("machine get: %s", r.out)
	}
	if r := h.ok("machine", "ls"); !strings.Contains(r.out, "(edge)") {
		t.Errorf("machine ls: %s", r.out)
	}
	if r := h.run("agent", "join", "https://main.example:7070", "--edge", "--code", "x"); r.code != cli.ExitUsage || !strings.Contains(r.err, "drop") {
		t.Errorf("an edge machine names no server: %d %q", r.code, r.err)
	}
}

// The catalogue lists the ways in with their category and sign-up page, and one installs as a stack whose
// member is the tunnel (L2-014 TUN-15).
func TestWaysInFromTheCatalogue(t *testing.T) {
	h := newHarness(t)
	h.login()
	if r := h.ok("catalogue", "ls"); !regexp.MustCompile(`quick-link\s+Quick link\s+public-access`).MatchString(r.out) || !regexp.MustCompile(`wordpress\s+WordPress\s+apps`).MatchString(r.out) {
		t.Errorf("catalogue ls: %s", r.out)
	}
	if r := h.ok("catalogue", "show", "tailscale"); !strings.Contains(r.out, "sign up at https://login.tailscale.com/start") || !strings.Contains(r.out, "--secret-param authKey=") {
		t.Errorf("catalogue show tailscale: %s", r.out)
	}
	h.ok("app", "create", "web", "--image", "nginx:1.27-alpine", "--port", "80", "--wait")
	h.ok("stack", "create", "share", "--template", "quick-link", "--param", "app=web", "--wait")
	if r := h.ok("tunnels", "list"); !strings.Contains(r.out, ".trycloudflare.com") {
		t.Errorf("the stack's tunnel: %s", r.out)
	}
}
