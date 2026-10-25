package reconciler_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/server"
	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
)

// Source apps on the fake engine with an in-memory Git source (ADR-019, L2-005 BLD-01 – BLD-05).

const repoURL = "https://git.example.com/acme/api.git"

func waitPhase(t *testing.T, c interface {
	GetResource(context.Context, string) (api.Resource, error)
}, id string, phases ...api.Phase) api.Resource {
	t.Helper()
	var got api.Resource
	waitFor(t, 15*time.Second, func() bool {
		got, _ = c.GetResource(context.Background(), id)
		return slices.Contains(phases, got.Status.Phase) && got.Status.ObservedGeneration == got.Generation
	})
	return got
}

func TestSourceAppBuildsSkipsRebuildsAndKeepsTwoImages(t *testing.T) {
	env := testenv.Start(t, testenv.WithConfig(func(c *server.Config) { c.DriftEvery = 100 * time.Millisecond }))
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	first := env.Git.Push(repoURL, "main", map[string]string{"Dockerfile": "FROM busybox:1.37\nRUN echo $GREETING\nCMD [\"httpd\"]\n", ".dockerignore": "*.md\n"})

	// Exactly one of image and source.
	if _, err := c.CreateResource(ctx, api.KindApp, "both", api.AppSpec{Image: "nginx", Source: &api.GitSource{Git: repoURL, Ref: "main"}}); apiCode(err) != api.CodeValidationFailed {
		t.Errorf("image and source: %v", err)
	}
	app, err := c.CreateResource(ctx, api.KindApp, "api", api.AppSpec{Port: 8080, Source: &api.GitSource{Git: repoURL, Ref: "main", BuildArgs: map[string]string{"GREETING": "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if s := mustSpec(t, app); s.Source.Dockerfile != "Dockerfile" || s.Source.Context != "." || s.Image != "" {
		t.Errorf("defaults: %+v", s.Source)
	}
	app = waitReadyWithin(t, c, app.ID, 15*time.Second)
	img1, _ := app.Outputs["builtImage"].(string)
	if app.Outputs["builtCommit"] != first || app.Outputs["lastBuild"] != "succeeded" || !strings.HasPrefix(img1, "enclave.local/api:r1-"+first[:12]) {
		t.Fatalf("outputs %v", app.Outputs)
	}
	logs, err := c.BuildLogs(ctx, app.ID, 100)
	if err != nil || len(logs) == 0 || !strings.Contains(joinLogs(logs), "RUN echo $GREETING\n#2 hi\n") {
		t.Errorf("build log: %v %v", joinLogs(logs), err)
	}

	// A redeploy with nothing new skips the build but still deploys (BLD-02, BLD-04).
	if _, err := c.Action(ctx, app.ID, api.ActionRedeploy); err != nil {
		t.Fatal(err)
	}
	app = waitPhase(t, c, app.ID, api.PhaseReady)
	if app.Outputs["builtImage"] != img1 || app.Outputs["lastBuild"] != "skipped" || app.Outputs["servingRevision"] != float64(2) {
		t.Errorf("skip: %v", app.Outputs)
	}
	if !hasEvent(t, c, app.ID, "build_skipped") {
		t.Error("no build_skipped event")
	}
	if evs, _ := c.Events(ctx, app.ID, 20); !slices.ContainsFunc(evs, func(e api.Event) bool {
		return e.Type == "redeployed" && strings.HasPrefix(e.Message, "Nothing new since "+first[:7])
	}) {
		t.Errorf("redeployed event of a source app: %+v", evs)
	}

	// A new commit and a redeploy build it; only the serving and previous images are kept (BLD-05).
	second := env.Git.Push(repoURL, "main", map[string]string{"Dockerfile": "FROM busybox:1.37\nCMD [\"httpd\"]\n"})
	if _, err := c.Action(ctx, app.ID, api.ActionRedeploy); err != nil {
		t.Fatal(err)
	}
	app = waitPhase(t, c, app.ID, api.PhaseReady)
	img2, _ := app.Outputs["builtImage"].(string)
	if app.Outputs["builtCommit"] != second || img2 == img1 || app.Outputs["lastBuild"] != "succeeded" {
		t.Errorf("rebuild: %v", app.Outputs)
	}
	third := env.Git.Push(repoURL, "main", map[string]string{"Dockerfile": "FROM busybox:1.37\nCMD [\"httpd\", \"-v\"]\n"})
	_, _ = c.Action(ctx, app.ID, api.ActionRedeploy)
	app = waitPhase(t, c, app.ID, api.PhaseReady)
	img3, _ := app.Outputs["builtImage"].(string)
	imgs := env.Fake.BuiltImages(app.ID)
	slices.Sort(imgs)
	if app.Outputs["builtCommit"] != third || len(imgs) != 2 || !slices.Contains(imgs, img2) || !slices.Contains(imgs, img3) {
		t.Errorf("kept images %v (serving %s, previous %s)", imgs, img3, img2)
	}

	// Drift repair redeploys the serving image; it never picks up a new commit by itself.
	env.Git.Push(repoURL, "main", map[string]string{"Dockerfile": "FROM busybox:1.37\n"})
	env.Fake.Kill(app.ID)
	waitFor(t, 15*time.Second, func() bool { return hasEvent(t, c, app.ID, "drift_detected") })
	app = waitPhase(t, c, app.ID, api.PhaseReady)
	if app.Outputs["builtImage"] != img3 || app.Outputs["builtCommit"] != third {
		t.Errorf("drift repair changed the build: %v", app.Outputs)
	}

	// Deleting the app removes its built images.
	if _, err := c.DeleteResource(ctx, app.ID, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool { _, err := c.GetResource(ctx, app.ID); return apiCode(err) == api.CodeNotFound })
	if imgs := env.Fake.BuiltImages(app.ID); len(imgs) != 0 {
		t.Errorf("images outlived the app: %v", imgs)
	}
}

func TestFailedBuildKeepsTheServingRevisionAndSaysWhichStep(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	env.Git.Push(repoURL, "main", map[string]string{"Dockerfile": "FROM busybox:1.37\nCMD [\"httpd\"]\n"})
	app, _ := c.CreateResource(ctx, api.KindApp, "web", api.AppSpec{Port: 8080, Source: &api.GitSource{Git: repoURL, Ref: "main"}})
	app = waitReadyWithin(t, c, app.ID, 15*time.Second)
	serving := app.Outputs["builtImage"]

	broken := env.Git.Push(repoURL, "main", map[string]string{"Dockerfile": "FROM busybox:1.37\nRUN make test || exit 2\n"})
	_, _ = c.Action(ctx, app.ID, api.ActionRedeploy)
	failed := waitFailed(t, c, app.ID)
	msg := failed.Status.Message
	if !strings.Contains(msg, "build of "+broken[:7]+" failed") || !strings.Contains(msg, "make test || exit 2") || !strings.Contains(msg, "exit code: 2") ||
		!strings.Contains(msg, "serving revision keeps running") {
		t.Errorf("message %q", msg)
	}
	if failed.Outputs["lastBuild"] != "failed" || failed.Outputs["builtImage"] != serving || failed.Outputs["lastDeploy"] != "failed" {
		t.Errorf("outputs after a failed build: %v", failed.Outputs)
	}
	if h, _ := env.Fake.Health(ctx, &domain.Resource{ID: app.ID, Kind: api.KindApp, Name: "web"}, engine.ObjectStore{}); h != engine.Healthy {
		t.Error("the serving revision stopped")
	}
	if logs, _ := c.BuildLogs(ctx, app.ID, 50); !strings.Contains(joinLogs(logs), "ERROR") {
		t.Errorf("failed build log: %s", joinLogs(logs))
	}

	// Retry after a failed build builds again (it doesn't fall back to the old image), and a fixed commit recovers.
	fixed := env.Git.Push(repoURL, "main", map[string]string{"Dockerfile": "FROM busybox:1.37\nCMD [\"httpd\"]\n# fixed\n"})
	if _, err := c.Action(ctx, app.ID, api.ActionReconcile); err != nil {
		t.Fatal(err)
	}
	app = waitPhase(t, c, app.ID, api.PhaseReady)
	if app.Outputs["builtCommit"] != fixed {
		t.Errorf("after the fix: %v", app.Outputs)
	}
}

func TestSourceCredentialsReferencesAndJobsUseTheBuiltImage(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	src := &api.GitSource{Git: repoURL, Ref: "main", CredentialsSecret: "git-token"}
	if _, err := c.CreateResource(ctx, api.KindApp, "priv", api.AppSpec{Source: src}); apiCode(err) != api.CodeValidationFailed {
		t.Errorf("missing credentials secret: %v", err)
	}
	sec, _ := c.CreateResource(ctx, api.KindSecret, "git-token", api.SecretSpec{Value: `{"token":"ghp_example"}`})
	app, err := c.CreateResource(ctx, api.KindApp, "priv", api.AppSpec{Source: src})
	if err != nil {
		t.Fatal(err)
	}
	app = waitReadyWithin(t, c, app.ID, 15*time.Second)
	if _, err := c.DeleteResource(ctx, sec.ID, nil); apiCode(err) != api.CodeResourceInUse {
		t.Errorf("delete the credentials secret: %v", err)
	}

	job, err := c.CreateResource(ctx, api.KindJob, "migrate", api.JobSpec{Schedule: "@daily", App: "priv", Command: []string{"./migrate"}})
	if err != nil {
		t.Fatal(err)
	}
	waitReadyWithin(t, c, job.ID, 10*time.Second)
	run, err := c.RunNow(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, c, job.ID, run.ID)
	if got := env.Fake.JobImage(run.ID); got != app.Outputs["builtImage"] {
		t.Errorf("job ran %q, want the app's built image %v", got, app.Outputs["builtImage"])
	}

	// Rejected credentials are permanent and named.
	env.Git.Refuse(repoURL, "the repository refused access: check the credentials secret")
	_, _ = c.Action(ctx, app.ID, api.ActionRedeploy)
	failed := waitFailed(t, c, app.ID)
	if !strings.Contains(failed.Status.Message, "refused access") || failed.Status.Attempts > 1 {
		t.Errorf("refused: %q after %d attempts", failed.Status.Message, failed.Status.Attempts)
	}
	if logs, err := c.BuildLogs(ctx, job.ID, 10); apiCode(err) != api.CodeActionNotSupported {
		t.Errorf("build logs of a job: %v %v", logs, err)
	}
}

func mustSpec(t *testing.T, r api.Resource) api.AppSpec {
	t.Helper()
	s, _, err := api.NormalizeSpec(api.KindApp, r.Spec)
	if err != nil {
		t.Fatal(err)
	}
	return s.(api.AppSpec)
}

func joinLogs(lines []api.LogLine) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.Message + "\n")
	}
	return b.String()
}

func hasEvent(t *testing.T, c interface {
	Events(context.Context, string, int) ([]api.Event, error)
}, id, typ string) bool {
	t.Helper()
	evs, _ := c.Events(context.Background(), id, 100)
	for _, e := range evs {
		if e.Type == typ {
			return true
		}
	}
	return false
}
