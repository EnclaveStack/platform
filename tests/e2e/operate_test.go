//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enclavestack/platform/pkg/api"
)

// TestOperateStackForRealWithOpenTofu: v0.7 on the real engine (L2-009). Two projects hold an app of the
// same name in their own containers; metrics are sampled from Docker; an alert on a failing app fires to
// a webhook channel; a log export sends the app's lines out; and a secret is read from a Vault.
func TestOperateStackForRealWithOpenTofu(t *testing.T) {
	requireTools(t, "docker", "tofu")
	ctx := context.Background()
	var mu sync.Mutex
	var alerts, logs []string
	hooks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/logs" {
			logs = append(logs, string(b))
		} else {
			alerts = append(alerts, string(b))
		}
	}))
	defer hooks.Close()
	vault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Header.Get("X-Vault-Token") != "s.e2e":
			w.WriteHeader(http.StatusForbidden)
		case r.URL.Path == "/v1/auth/token/lookup-self":
			_, _ = w.Write([]byte(`{"data":{}}`))
		case r.URL.Path == "/v1/secret/data/shop/stripe":
			_, _ = w.Write([]byte(`{"data":{"data":{"key":"sk_from_vault"}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer vault.Close()

	s := startServer(t, t.TempDir(), freePort(t), "--metric-interval", "2s")
	token, c := setupAndToken(t, s)
	bin := build(t)
	work := t.TempDir()
	rc := filepath.Join(work, "dev.tofurc")
	_ = os.WriteFile(rc, []byte(fmt.Sprintf("provider_installation {\n  dev_overrides { \"enclavestack/enclave\" = %q }\n  direct {}\n}\n", bin)), 0o600)
	id := suffix()
	shop := "e2e-shop-" + id
	web := "e2e-web-" + id
	main := fmt.Sprintf(`
terraform {
  required_providers {
    enclave = { source = "enclavestack/enclave" }
  }
}
provider "enclave" {}

resource "enclave_project" "shop" {
  name = %[1]q
}
resource "enclave_app" "web_default" {
  name  = %[2]q
  image = "nginx:1.27-alpine"
  port  = 80
}
resource "enclave_app" "web_shop" {
  name    = %[2]q
  project = enclave_project.shop.name
  image   = "nginx:1.27-alpine"
  port    = 80
}
resource "enclave_channel" "ops" {
  name    = "ops"
  project = enclave_project.shop.name
  type    = "webhook"
  webhook = { url = "%[3]s/alerts" }
}
resource "enclave_log_export" "out" {
  name          = "out"
  project       = enclave_project.shop.name
  sources       = ["app/${enclave_app.web_shop.name}"]
  flush_seconds = 5
  http          = { url = "%[3]s/logs" }
}
resource "enclave_dashboard" "health" {
  name    = "health"
  project = enclave_project.shop.name
  widgets = [{ title = "CPU", metric = "cpu", resources = ["app/${enclave_app.web_shop.name}"] }]
}
resource "enclave_secret" "vault_token" {
  name    = "vault-token"
  project = enclave_project.shop.name
  value   = "s.e2e"
}
resource "enclave_secret_store" "vault" {
  name            = "vault"
  project         = enclave_project.shop.name
  secret_provider = "vault"
  settings        = { address = %[4]q, tokenSecret = enclave_secret.vault_token.name }
}
resource "enclave_secret" "stripe" {
  name    = "stripe"
  project = enclave_project.shop.name
  source  = { store = enclave_secret_store.vault.name, key = "shop/stripe", field = "key" }
}
`, shop, web, hooks.URL, vault.URL)
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
		_, _, _ = cli(t, s, token, "", "--project", shop, "alert", "delete", "bad-fails", "--yes", "--wait")
		cmd := exec.Command("tofu", "destroy", "-auto-approve", "-no-color")
		cmd.Dir, cmd.Env = work, env
		_ = cmd.Run()
	})
	tofu("apply", "-auto-approve", "-no-color")

	// The same name in two projects: two containers, two private DNS names (ADR-035).
	// App containers are named per revision (ADR-016): es-app-<name>[.<project>]-r<n>.
	for _, name := range []string{"es-app-" + web + "-r1", "es-app-" + web + "." + shop + "-r1"} {
		if out, err := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", name).CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "true" {
			t.Errorf("container %s: %v %s", name, err, out)
		}
	}
	shopApp := getJSON(t, s, token, "app", web) // default project
	if shopApp.Project != api.DefaultProject {
		t.Errorf("project: %q", shopApp.Project)
	}
	out := mustCLI(t, s, token, "", "--project", shop, "get", "app", web, "-o", "json")
	if !strings.Contains(out, `"privateDns": "`+web+".app."+shop+`.internal"`) {
		t.Errorf("private DNS in shop: %s", out)
	}

	// A secret read from Vault (EXT-03).
	stripe := mustCLI(t, s, token, "", "--project", shop, "secret", "get", "stripe", "--reveal")
	if strings.TrimSpace(stripe) != "sk_from_vault" {
		t.Errorf("synced secret: %q", stripe)
	}

	// Metrics sampled from Docker (MET-01).
	waitUntil(t, 60*time.Second, func() bool {
		o, _, code := cli(t, s, token, "", "--project", shop, "metrics", "app/"+web, "--metric", "memory", "--since", "5m")
		return code == 0 && strings.Contains(o, "MiB")
	}, "no memory metric for the app")
	if o := mustCLI(t, s, token, "", "metrics", "machine", "--metric", "diskFree"); !strings.Contains(o, "diskFree") {
		t.Errorf("machine metrics: %s", o)
	}

	// The app's logs leave through the export (LOG-01).
	// An export reads from its creation on (LOG-02); a request now makes nginx print an access line.
	waitUntil(t, 30*time.Second, func() bool {
		// The path doesn't exist; nginx answers 404 and logs it, which is all this needs.
		out, _ := exec.Command("docker", "exec", "es-app-"+web+"."+shop+"-r1", "sh", "-c", "wget -qO- http://127.0.0.1/e2e-log-probe 2>&1; true").CombinedOutput()
		return strings.Contains(string(out), "404")
	}, "nginx in shop didn't answer")
	waitUntil(t, 60*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(strings.Join(logs, "\n"), "e2e-log-probe")
	}, "no log lines reached the HTTP destination")

	// A metric alert that is always true fires to the webhook channel once its minute has passed
	// (ALR-02, NCH-02). (A failing app would take its whole retry schedule to reach Failed.)
	mustCLI(t, s, token, "", "--project", shop, "alert", "create", "bad-fails", "--metric", "diskPercentFull", "--resource", "machine", "--below", "101", "--for", "1m",
		"--channel", "ops", "--wait")
	fired := func() bool {
		mu.Lock()
		defer mu.Unlock()
		all := strings.Join(alerts, "\n")
		return strings.Contains(all, "bad-fails") && strings.Contains(all, "firing")
	}
	for deadline := time.Now().Add(120 * time.Second); !fired(); time.Sleep(time.Second) {
		if time.Now().After(deadline) {
			bad := ""
			al, _, _ := cli(t, s, token, "", "--project", shop, "alert", "get", "bad-fails")
			dl, _, _ := cli(t, s, token, "", "--project", shop, "channel", "deliveries", "ops")
			t.Fatalf("the alert never reached the webhook%s\nalert:\n%s\ndeliveries:\n%s\nserver log:\n%s", bad, al, dl,
				grepLines(s.logs.String(), "alert")+"\n"+grepLines(s.logs.String(), "notif"))
		}
	}
	if o := mustCLI(t, s, token, "", "--project", shop, "alert", "incidents", "bad-fails"); !strings.Contains(o, "machine") {
		t.Errorf("incidents: %s", o)
	}

	// Scoped tokens: a viewer token of shop reads shop and nothing else (TOK-02).
	tok, err := c.CreateToken(ctx, api.CreateTokenRequest{Name: "viewer", Role: api.RoleViewer, Projects: []string{shop}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, code := cli(t, s, tok.Secret, "", "--project", shop, "get", "app", web); code != 0 {
		t.Errorf("a shop viewer can't read shop")
	}
	if _, _, code := cli(t, s, tok.Secret, "", "get", "app", web); code == 0 {
		t.Errorf("a shop viewer read the default project")
	}
	if _, _, code := cli(t, s, tok.Secret, "", "--project", shop, "secret", "create", "nope", "--value", "x"); code == 0 {
		t.Errorf("a viewer created a secret")
	}
	if strings.Contains(s.logs.String(), `"level":"ERROR"`) {
		t.Logf("server errors:\n%s", grepLines(s.logs.String(), `"level":"ERROR"`))
	}
}

func grepLines(s, needle string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, needle) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}
