//go:build docker

package docker

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// Zero-downtime deploys on real Docker (ADR-016, L2-005 DEP-01 – DEP-06, L5-002 §6).

type narrator struct {
	steps
	notes  []string
	events []string
}

func (n *narrator) Note(m string)       { n.notes = append(n.notes, m) }
func (n *narrator) Event(typ, m string) { n.events = append(n.events, typ+": "+m) }

func fastDriver(t *testing.T) *Driver {
	t.Helper()
	d := newDriver(t)
	d.opts.Drain = 500 * time.Millisecond
	return d
}

func deploy(t *testing.T, d *Driver, r *domain.Resource, spec api.AppSpec, revision int64) (engine.Outputs, *narrator, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	n := &narrator{}
	out, err := d.Ensure(ctx, engine.EnsureInput{Resource: r, Spec: spec, Revision: revision}, n)
	return out, n, err
}

// probeGet fetches a URL from inside the enclave network, as other containers would.
func probeGet(ctx context.Context, d *Driver, url string) (string, error) {
	res, err := d.exec(ctx, engine.ProbeName, []string{"wget", "-q", "-T", "2", "-O", "-", url}, nil)
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("exit %d: %s", res.ExitCode, res.Output)
	}
	return res.Output, nil
}

func revisionsOf(t *testing.T, d *Driver, r *domain.Resource) []string {
	t.Helper()
	revs, err := d.revisions(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, rev := range revs {
		names = append(names, rev.name)
	}
	return names
}

// statesOf is what Docker itself reports for each container of the resource, for a failure message that
// says why the driver and the daemon disagree (on CI the daemon answered "running" right after a stop).
func statesOf(d *Driver, r *domain.Resource) string {
	list, err := d.cli.ContainerList(context.Background(), container.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", LabelResourceID+"="+r.ID))})
	if err != nil {
		return "listing containers: " + err.Error()
	}
	var out []string
	for _, c := range list {
		name := c.ID[:12]
		if len(c.Names) > 0 {
			name = c.Names[0][1:]
		}
		out = append(out, fmt.Sprintf("%s=%s (%s)", name, c.State, c.Status))
	}
	return strings.Join(out, ", ")
}

func TestRollingDeploySwitchesToAHealthyRevisionAndKeepsServing(t *testing.T) {
	d := fastDriver(t)
	r := resource(api.KindApp, "it-roll-"+suffix())
	cleanup(t, d, r, engine.ObjectStore{})
	hc := &api.HealthCheck{Type: api.HealthHTTP, Path: "/", IntervalSeconds: 1, TimeoutSeconds: 1, Retries: 30}
	spec := api.AppSpec{Image: "nginx:1.27-alpine", Port: 80, MemoryMiB: 64, Strategy: api.StrategyRolling, HealthCheck: hc}
	out, n, err := deploy(t, d, r, spec, 1)
	if err != nil {
		t.Fatalf("first deploy: %v (%v)", err, n.events)
	}
	if out[engine.OutputServingRevision] != int64(1) || out["internalHost"] != engine.ContainerName(r) {
		t.Fatalf("outputs %v", out)
	}
	r.ServingRevision = 1

	// Keep requesting the app's stable internal host during the deploy.
	ctx, stop := context.WithCancel(context.Background())
	var ok, failed atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			if _, err := probeGet(ctx, d, "http://"+engine.ContainerName(r)+"/"); err == nil {
				ok.Add(1)
			} else if ctx.Err() == nil {
				failed.Add(1)
			}
		}
	}()

	r.Generation = 2
	spec.Image = "nginx:1.28-alpine"
	out, n, err = deploy(t, d, r, spec, 2)
	stop()
	<-done
	if err != nil {
		t.Fatalf("rolling deploy: %v (%v)", err, n.events)
	}
	if out[engine.OutputServingRevision] != int64(2) {
		t.Fatalf("serving %v", out)
	}
	if got := revisionsOf(t, d, r); len(got) != 1 || got[0] != engine.RevisionName(r, 2) {
		t.Errorf("old revision not removed: %v", got)
	}
	joined := strings.Join(n.events, " | ")
	for _, want := range []string{"revision_started: Revision 2 started.", "health_check_passed", "traffic_switched: Revision 2 is serving.", "revision_stopped: Revision 1 stopped."} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing event %q in %s", want, joined)
		}
	}
	// Internal clients that resolved the old revision just before its removal may see a refusal
	// (L2-005 DEP-06 promises zero drops for domain traffic, which switches explicitly); the app must
	// still have answered throughout.
	t.Logf("internal requests during deploy: %d ok, %d failed", ok.Load(), failed.Load())
	if ok.Load() == 0 || failed.Load() > ok.Load()/10 {
		t.Errorf("app unavailable during the rolling deploy: %d ok, %d failed", ok.Load(), failed.Load())
	}
	r.ServingRevision = 2

	// A revision that never passes its check is discarded; revision 2 keeps serving.
	r.Generation = 3
	spec.HealthCheck = &api.HealthCheck{Type: api.HealthHTTP, Path: "/missing", IntervalSeconds: 1, TimeoutSeconds: 1, Retries: 3}
	_, n, err = deploy(t, d, r, spec, 3)
	if err == nil || !engine.IsPermanent(err) {
		t.Fatalf("unhealthy revision: want permanent error, got %v", err)
	}
	for _, want := range []string{"Revision 3 failed its health check", "404", "Revision 2 is still serving"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}
	if !strings.Contains(strings.Join(n.notes, " | "), "attempt 3 of 3") {
		t.Errorf("attempts not narrated: %v", n.notes)
	}
	if got := revisionsOf(t, d, r); len(got) != 1 || got[0] != engine.RevisionName(r, 2) {
		t.Errorf("failed revision not discarded or serving one touched: %v", got)
	}
	if body, err := probeGet(context.Background(), d, "http://"+engine.ContainerName(r)+"/"); err != nil || !strings.Contains(body, "nginx") {
		t.Errorf("revision 2 stopped answering: %v", err)
	}

	// Drift: the serving revision stopped outside EnclaveStack is reported and restarted in place.
	name := engine.RevisionName(r, 2)
	// This step tests EnclaveStack's drift detection, not Docker's own restart policy, which on some
	// daemons brings a stopped container straight back: take it out of the picture.
	if _, err := d.cli.ContainerUpdate(context.Background(), name, container.UpdateConfig{RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyDisabled}}); err != nil {
		t.Fatal(err)
	}
	if err := d.cli.ContainerStop(context.Background(), name, container.StopOptions{}); err != nil {
		t.Fatal(err)
	}
	// Docker finishes a stop a moment after the call returns on some daemons: wait for it to show.
	h := engine.Healthy
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if h, _ = d.Health(context.Background(), r, engine.ObjectStore{}); h == engine.Stopped {
			break
		}
	}
	if h != engine.Stopped {
		detail := ""
		if c, err := d.cli.ContainerInspect(context.Background(), name); err == nil && c.State != nil {
			detail = fmt.Sprintf("; %s restarted %d times, finished %s, exit code %d", name, c.RestartCount, c.State.FinishedAt, c.State.ExitCode)
		}
		t.Errorf("health of stopped serving revision = %v (want Stopped); serving revision %d; docker reports: %s%s", h, r.ServingRevision, statesOf(d, r), detail)
	}
	r.Generation = 2
	spec.HealthCheck = hc
	if out, _, err := deploy(t, d, r, spec, 4); err != nil || out[engine.OutputServingRevision] != int64(2) {
		t.Errorf("repair should restart revision 2 in place: %v %v", out, err)
	}
}

func TestPublishedAppRecreatesAndKeepsItsHostPort(t *testing.T) {
	d := fastDriver(t)
	r := resource(api.KindApp, "it-recreate-"+suffix())
	cleanup(t, d, r, engine.ObjectStore{})
	spec := api.AppSpec{Image: "nginx:1.27-alpine", Port: 80, Publish: true, MemoryMiB: 64, Strategy: api.StrategyRolling}
	out, _, err := deploy(t, d, r, spec, 1)
	if err != nil {
		t.Fatal(err)
	}
	port := out["port"].(int)
	r.Generation, r.ServingRevision = 2, 1
	out, n, err := deploy(t, d, r, spec, 2)
	if err != nil {
		t.Fatal(err)
	}
	if out["port"].(int) != port || out[engine.OutputServingRevision] != int64(2) {
		t.Errorf("host port changed or revision wrong: %v (was %d)", out, port)
	}
	if !strings.Contains(strings.Join(n.events, " | "), "deploy_recreate") {
		t.Errorf("recreate not explained: %v", n.events)
	}
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("published port: %v %v", err, resp)
	}
	resp.Body.Close()
}

func TestAV02ContainerIsAdoptedThenReplacedByARevision(t *testing.T) {
	d := fastDriver(t)
	ctx := context.Background()
	r := resource(api.KindApp, "it-legacy-"+suffix())
	cleanup(t, d, r, engine.ObjectStore{})
	if err := d.ensureNetwork(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.ensureImage(ctx, "nginx:1.27-alpine", false); err != nil {
		t.Fatal(err)
	}
	legacy := engine.ContainerName(r)
	cfg := &container.Config{Image: "nginx:1.27-alpine", Labels: map[string]string{LabelManaged: "true", LabelResourceID: r.ID, LabelKind: "app", LabelGeneration: "1"}}
	netCfg := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{engine.NetworkName: {Aliases: []string{legacy}}}}
	if _, err := d.cli.ContainerCreate(ctx, cfg, &container.HostConfig{}, netCfg, nil, legacy); err != nil {
		t.Fatal(err)
	}
	if err := d.start(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	spec := api.AppSpec{Image: "nginx:1.27-alpine", Port: 80, MemoryMiB: 64, Strategy: api.StrategyRolling}
	if out, _, err := deploy(t, d, r, spec, 1); err != nil || out[engine.OutputServingRevision] != int64(0) {
		t.Fatalf("same generation: the v0.2 container is adopted as revision 0: %v %v", out, err)
	}
	r.Generation = 2
	out, _, err := deploy(t, d, r, spec, 1)
	if err != nil || out[engine.OutputServingRevision] != int64(1) {
		t.Fatalf("next deploy: %v %v", out, err)
	}
	if got := revisionsOf(t, d, r); len(got) != 1 || got[0] != engine.RevisionName(r, 1) {
		t.Errorf("legacy container not replaced: %v", got)
	}
}
