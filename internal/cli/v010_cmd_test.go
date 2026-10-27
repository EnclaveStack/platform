package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enclavestack/platform/internal/cli"
	"github.com/enclavestack/platform/pkg/api"
)

// v0.10 commands (L2-016 CLI-59 – CLI-64) against a real control plane on the fake engine.

func TestFunctionsFromTheCLI(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.mustRun("queue", "create", "jobs", "--visibility-timeout", "120s", "--wait")
	h.mustRun("bucket", "create", "uploads", "--wait")
	h.mustRun("function", "create", "echo", "--image", "alpine:3.22", "--queue", "jobs", "--on-bucket", "uploads/in/", "--schedule", "@hourly",
		"--concurrency", "2", "--run-timeout", "30s", "--env", "MODE=test", "--wait", "--", "exit", "0")
	var fs api.FunctionSpec
	h.spec("fn", "echo", &fs)
	if fs.Image != "alpine:3.22" || fs.Concurrency != 2 || fs.TimeoutSeconds != 30 || len(fs.Triggers) != 3 || fs.Triggers[2].Bucket != "uploads" ||
		fs.Triggers[2].Prefix != "in/" || strings.Join(fs.Command, " ") != "exit 0" || fs.Env["MODE"] != "test" {
		t.Errorf("spec %+v", fs)
	}
	h.mustRun("function", "update", "echo", "--queue", "jobs", "--wait") // trigger flags replace them all
	h.spec("fn", "echo", &fs)
	if len(fs.Triggers) != 1 || fs.Triggers[0].Queue != "jobs" {
		t.Errorf("triggers %+v", fs.Triggers)
	}
	r := h.mustRun("-o", "json", "function", "invoke", "echo", "--data", `{"hello":"world"}`, "--wait")
	var d api.RunDetail
	if err := json.Unmarshal([]byte(r.out), &d); err != nil || d.Status != api.RunSucceeded || d.Trigger != api.TriggerInvoke {
		t.Errorf("invoke: %s %v", r.out, err)
	}
	if r := h.run("function", "invoke", "echo", "--data", "not json"); r.code != cli.ExitUsage {
		t.Errorf("a body that isn't JSON: %d %s", r.code, r.err)
	}
	if r := h.mustRun("function", "runs", "echo"); !strings.Contains(r.out, api.TriggerInvoke) {
		t.Errorf("runs: %s", r.out)
	}
	h.mustRun("topic", "create", "news", "--function", "echo", "--wait")
	var ts api.TopicSpec
	h.spec("topic", "news", &ts)
	if len(ts.Subscriptions) != 1 || ts.Subscriptions[0].Function != "echo" {
		t.Errorf("topic %+v", ts)
	}
}

func TestPreviewsFromTheCLI(t *testing.T) {
	h := newHarness(t)
	h.login()
	const repo = "https://git.example.com/acme/web.git"
	h.env.Git.Push(repo, "main", map[string]string{"Dockerfile": "FROM busybox:1.37\n"})
	h.env.Git.Push(repo, "feature/x", map[string]string{"Dockerfile": "FROM busybox:1.37\n"})
	h.stdin = "s3cret"
	h.mustRun("secret", "create", "web-hook", "--from-stdin")
	h.mustRun("app", "create", "web", "--git", repo, "--ref", "main", "--port", "8080", "--webhook-secret", "web-hook", "--deploy-on-push",
		"--previews", "--preview-max", "3", "--preview-domain", "preview.example.com", "--wait")
	var as api.AppSpec
	h.spec("app", "web", &as)
	if as.Source.WebhookSecret != "web-hook" || !as.Source.DeployOnPush || !as.Previews.On() || as.Previews.MaxActive != 3 || as.Previews.Domain.Suffix != "preview.example.com" {
		t.Errorf("spec %+v %+v", as.Source, as.Previews)
	}
	if r := h.mustRun("app", "get", "web"); !strings.Contains(r.out, api.HookPath) {
		t.Errorf("no webhook URL: %s", r.out)
	}
	h.mustRun("app", "preview", "create", "web", "--ref", "feature/x", "--wait")
	if r := h.mustRun("app", "preview", "ls", "web"); !strings.Contains(r.out, "web-feature-x") || !strings.Contains(r.out, "feature/x") {
		t.Errorf("ls: %s", r.out)
	}
	h.mustRun("app", "update", "web", "--preview-ttl", "24")
	h.spec("app", "web", &as)
	if as.Previews.TTLHours != 24 || as.Previews.MaxActive != 3 {
		t.Errorf("update kept the rest? %+v", as.Previews)
	}
	h.mustRun("app", "preview", "rm", "web", "web-feature-x", "--yes")
	if r := h.run("app", "preview", "rm", "web", "nope", "--yes"); r.code == 0 {
		t.Error("removed a preview that doesn't exist")
	}
	if r := h.run("app", "preview", "create", "web"); r.code != cli.ExitUsage {
		t.Errorf("no ref: %d", r.code)
	}
}

func TestDatabaseOptionsBackupsAndAlertsFromTheCLI(t *testing.T) {
	h := newHarness(t)
	h.login()
	script := filepath.Join(t.TempDir(), "setup.sql")
	if err := os.WriteFile(script, []byte("CREATE TABLE t (id int);"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.mustRun("db", "create", "orders", "--tls", "--exposed", "--init-script-file", script, "--wait")
	var ds api.DatabaseSpec
	h.spec("db", "orders", &ds)
	if !ds.TLS || !ds.Exposed || ds.InitScript != "CREATE TABLE t (id int);" {
		t.Errorf("spec %+v", ds)
	}
	if r := h.mustRun("db", "get", "orders"); !strings.Contains(r.out, "sslmode=require") || !strings.Contains(r.out, "BEGIN CERTIFICATE") {
		t.Errorf("db get: %s", r.out)
	}
	if r := h.run("db", "create", "open", "--exposed"); r.code != cli.ExitError {
		t.Errorf("exposed without TLS: %d %s", r.code, r.err)
	}

	h.mustRun("volume", "create", "files", "--type", "filesystem", "--wait")
	h.mustRun("bucket", "create", "backups", "--wait")
	h.mustRun("backup", "create", "files-bak", "--volume", "files", "--bucket", "backups", "--wait")
	var bs api.BackupSpec
	h.spec("backup", "files-bak", &bs)
	if bs.Source.Volume != "files" {
		t.Errorf("backup %+v", bs.Source)
	}
	if r := h.run("backup", "create", "x-bak", "--volume", "files", "--source", "database/orders", "--bucket", "backups"); r.code != cli.ExitUsage {
		t.Errorf("both sources: %d", r.code)
	}

	h.mustRun("channel", "create", "hook", "--webhook", "http://127.0.0.1:1/hook", "--wait")
	h.mustRun("alert", "create", "deploys", "--event", "ready", "--event", "failed", "--resource", "database/orders", "--channel", "hook", "--wait")
	var al api.AlertSpec
	h.spec("alert", "deploys", &al)
	if al.Condition.Type != api.ConditionEvent || strings.Join(al.Condition.Events, ",") != "ready,failed" || al.Condition.Resources[0] != "database/orders" {
		t.Errorf("alert %+v", al.Condition)
	}
	if r := h.run("alert", "create", "bad", "--event", "nope", "--resource", "database/orders"); r.code != cli.ExitError {
		t.Errorf("unknown event: %d", r.code)
	}

	if r := h.mustRun("machine", "add", "spare", "--role", "standby"); !strings.Contains(r.out, "--standby") {
		t.Errorf("standby join: %s", r.out)
	}
	if r := h.run("machines", "add", "spare-2", "--role", "standby"); r.code != cli.ExitConflict || !strings.Contains(r.err, "already the standby") {
		t.Errorf("second standby: %d %s", r.code, r.err)
	}
}
