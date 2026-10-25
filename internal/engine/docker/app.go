package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// LabelRevision numbers an app's revisions (ADR-016). v0.2 containers have no label: revision 0.
// LabelReplica numbers a revision's containers from 1 (ADR-022); containers before v0.4 are replica 1.
const (
	LabelRevision = "enclave.revision"
	LabelReplica  = "enclave.replica"
)

// DefaultDrain is how long an old revision keeps running after traffic moves away from it.
const DefaultDrain = 10 * time.Second

// stopGrace is the SIGTERM grace period when stopping an old revision.
const stopGrace = 10

// revision is one container of an app: one replica of one revision.
type revision struct {
	name       string
	number     int64
	generation int64
	replica    int
	running    bool
}

// group is one revision: its replicas, in replica order.
type group struct {
	number     int64
	generation int64
	members    []revision
}

func (g *group) names() []string {
	out := make([]string, len(g.members))
	for i, m := range g.members {
		out[i] = m.name
	}
	return out
}

// complete reports whether the group has replicas 1…n and nothing else.
func (g *group) complete(n int) bool {
	if len(g.members) != n {
		return false
	}
	for i, m := range g.members {
		if m.replica != i+1 {
			return false
		}
	}
	return true
}

func (g *group) anyRunning() bool {
	return slices.ContainsFunc(g.members, func(m revision) bool { return m.running })
}

func (g *group) allRunning() bool {
	return !slices.ContainsFunc(g.members, func(m revision) bool { return !m.running })
}

// revisions lists every container of an app, newest revision first, replicas in order.
func (d *Driver) revisions(ctx context.Context, r *domain.Resource) ([]revision, error) {
	lctx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	list, err := d.cli.ContainerList(lctx, container.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", LabelResourceID+"="+r.ID))})
	if err != nil {
		return nil, d.classify(fmt.Errorf("list revisions of %s: %w", r.Name, err))
	}
	out := make([]revision, 0, len(list))
	for _, c := range list {
		rev := revision{running: c.State == "running", replica: 1}
		if len(c.Names) > 0 {
			rev.name = c.Names[0][1:]
		}
		rev.number, _ = strconv.ParseInt(c.Labels[LabelRevision], 10, 64)
		rev.generation, _ = strconv.ParseInt(c.Labels[LabelGeneration], 10, 64)
		if n, err := strconv.Atoi(c.Labels[LabelReplica]); err == nil && n > 0 {
			rev.replica = n
		}
		out = append(out, rev)
	}
	slices.SortFunc(out, func(a, b revision) int {
		if a.number != b.number {
			return int(b.number - a.number)
		}
		return a.replica - b.replica
	})
	return out, nil
}

// groups gathers containers by revision, newest first.
func groups(revs []revision) []group {
	var out []group
	for _, rev := range revs {
		if n := len(out); n > 0 && out[n-1].number == rev.number {
			out[n-1].members = append(out[n-1].members, rev)
			continue
		}
		out = append(out, group{number: rev.number, generation: rev.generation, members: []revision{rev}})
	}
	return out
}

// servingOf picks the revision receiving traffic: the one the store recorded, else (after a crash
// between the switch and saving it, or for a v0.2 app) the newest one with a running replica.
func servingOf(revs []revision, recorded int64) *group {
	gs := groups(revs)
	for i := range gs {
		if recorded > 0 && gs[i].number == recorded {
			return &gs[i]
		}
	}
	for i := range gs {
		if gs[i].anyRunning() {
			return &gs[i]
		}
	}
	return nil
}

// replicaName is the container name of one replica: the revision's name for the first (as before
// v0.4), with "-<i>" for the others (ADR-022).
func replicaName(r *domain.Resource, revision int64, replica int) string {
	if replica <= 1 {
		return engine.RevisionName(r, revision)
	}
	return engine.RevisionName(r, revision) + "-" + strconv.Itoa(replica)
}

// replicasOf reads an app's replica count from its stored spec (1 when unset).
func replicasOf(r *domain.Resource) int {
	var s api.AppSpec
	if json.Unmarshal(r.Spec, &s) == nil && s.Replicas > 0 {
		return s.Replicas
	}
	return 1
}

// ensureApp converges an app with zero-downtime revisions of 1–10 replicas (ADR-016, ADR-022).
func (d *Driver) ensureApp(ctx context.Context, in engine.EnsureInput, report engine.Reporter) (engine.Outputs, error) {
	r := in.Resource
	spec, _ := in.Spec.(api.AppSpec)
	replicas := max(spec.Replicas, 1)
	p, err := d.planFor(in)
	if err != nil {
		return nil, err
	}
	if err := d.ensureNetwork(ctx); err != nil {
		return nil, err
	}
	revs, err := d.revisions(ctx, r)
	if err != nil {
		return nil, err
	}
	serving := servingOf(revs, r.ServingRevision)

	// Steady state: the serving revision already runs this generation with every replica — make sure
	// they are up, drop any leftovers of an interrupted deploy, and report.
	if serving != nil && serving.generation == r.Generation && !in.Recreate && serving.complete(replicas) {
		if err := d.removeOthers(ctx, revs, serving.names()...); err != nil {
			return nil, err
		}
		report.Step("pull", api.StepDone)
		report.Step("start", api.StepActive)
		wasRunning := serving.allRunning()
		for _, m := range serving.members {
			if !m.running {
				if err := d.start(ctx, m.name); err != nil {
					return nil, err
				}
			}
		}
		// A restarted container has a fresh namespace: load its rules before anything else (ADR-021).
		if err := d.applyToNew(ctx, in, serving.names()...); err != nil {
			return nil, err
		}
		report.Step("start", api.StepDone)
		report.Step("health", api.StepActive)
		if !wasRunning || r.ServingRevision != serving.number {
			for _, m := range serving.members {
				if err := d.probeRevision(ctx, m.name, spec, report); err != nil {
					return nil, fmt.Errorf("revision %d is not healthy: %w", serving.number, err)
				}
			}
		}
		report.Step("health", api.StepDone)
		report.Step("switch", api.StepDone)
		return d.appOutputs(ctx, r, p, serving.members[0].name, serving.number, len(serving.members))
	}

	// Leftovers of an interrupted deploy are discarded; the deploy starts again from the top.
	var keep []string
	if serving != nil {
		keep = serving.names()
	}
	if err := d.removeOthers(ctx, revs, keep...); err != nil {
		return nil, err
	}

	report.Step("pull", api.StepActive)
	if err := d.ensureImage(ctx, p.image, in.Recreate); err != nil {
		return nil, err
	}
	report.Step("pull", api.StepDone)

	next := in.Revision
	if len(revs) > 0 && revs[0].number >= next {
		next = revs[0].number + 1
	}
	if next < 1 {
		next = 1
	}
	recreate := spec.Strategy == api.StrategyRecreate || spec.Publish
	keepPort := 0
	if recreate && serving != nil {
		if spec.Publish && spec.Strategy != api.StrategyRecreate {
			engine.Event(report, "deploy_recreate", "This app publishes a host port, so the old revision stops before the new one starts.")
		}
		if spec.Publish {
			if c, err := d.inspect(ctx, serving.members[0].name); err == nil && c != nil {
				keepPort = hostPort(c, p.port)
			}
		}
		for _, n := range serving.names() {
			if err := d.remove(ctx, n); err != nil {
				return nil, err
			}
		}
		engine.Event(report, "revision_stopped", fmt.Sprintf("Revision %d stopped.", serving.number))
	}

	report.Step("start", api.StepActive)
	names := make([]string, replicas)
	for i := range replicas {
		names[i] = replicaName(r, next, i+1)
		labels := map[string]string{LabelManaged: "true", LabelResourceID: r.ID, LabelKind: string(r.Kind),
			LabelGeneration: strconv.FormatInt(r.Generation, 10), LabelRevision: strconv.FormatInt(next, 10), LabelReplica: strconv.Itoa(i + 1)}
		if err := d.create(ctx, r, names[i], engine.ContainerName(r), p, labels, map[int]int{p.port: keepPort}); err != nil {
			return nil, d.discard(ctx, names[:i], err)
		}
		if err := d.start(ctx, names[i]); err != nil {
			return nil, d.discard(ctx, names[:i+1], err)
		}
	}
	// The new revision is protected before it takes traffic, and firewalls naming this app as a source
	// learn its new addresses (ADR-021).
	if err := d.applyToNew(ctx, in, names...); err != nil {
		return nil, d.discard(ctx, names, err)
	}
	if err := started(ctx, in); err != nil {
		return nil, d.discard(ctx, names, err)
	}
	engine.Event(report, "revision_started", fmt.Sprintf("Revision %d started%s.", next, replicaWords(replicas)))
	report.Step("start", api.StepDone)

	report.Step("health", api.StepActive)
	for i, name := range names {
		if replicas > 1 {
			engine.Note(report, fmt.Sprintf("Checking replica %d of %d.", i+1, replicas))
		}
		if err := d.probeRevision(ctx, name, spec, report); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			var setup *probeSetupError
			if errors.As(err, &setup) {
				_ = d.discard(ctx, names, nil)
				return nil, err
			}
			tail := d.tail(context.WithoutCancel(ctx), name, 5)
			_ = d.discard(ctx, names, nil)
			which := ""
			if replicas > 1 {
				which = fmt.Sprintf(" (replica %d of %d)", i+1, replicas)
			}
			msg := fmt.Sprintf("Revision %d failed its health check%s: %v. Last output: %s.", next, which, err, tail)
			if !recreate && serving != nil {
				msg += fmt.Sprintf(" Revision %d is still serving.", serving.number)
			} else {
				msg += " The app is not running."
			}
			engine.Event(report, "health_check_failed", fmt.Sprintf("Revision %d failed its health check and was discarded.", next))
			return nil, engine.Permanent(errors.New(msg))
		}
	}
	engine.Event(report, "health_check_passed", fmt.Sprintf("Revision %d passed its health check.", next))
	report.Step("health", api.StepDone)

	report.Step("switch", api.StepActive)
	engine.Event(report, "traffic_switched", fmt.Sprintf("Revision %d is serving.", next))
	if !recreate && serving != nil {
		engine.Note(report, fmt.Sprintf("Draining revision %d.", serving.number))
		if err := sleepCtx(ctx, d.opts.drain()); err != nil {
			return nil, err
		}
		for _, n := range serving.names() {
			if err := d.stopAndRemove(ctx, n); err != nil {
				return nil, err
			}
		}
		engine.Event(report, "revision_stopped", fmt.Sprintf("Revision %d stopped.", serving.number))
	}
	report.Step("switch", api.StepDone)
	return d.appOutputs(ctx, r, p, names[0], next, replicas)
}

func replicaWords(n int) string {
	if n == 1 {
		return ""
	}
	return fmt.Sprintf(" with %d replicas", n)
}

// discard removes a revision's containers after a failed start and returns cause.
func (d *Driver) discard(ctx context.Context, names []string, cause error) error {
	for _, n := range names {
		if err := d.remove(context.WithoutCancel(ctx), n); err != nil {
			d.log.Warn("remove failed revision", "container", n, "error", err)
		}
	}
	return cause
}

func (d *Driver) appOutputs(ctx context.Context, r *domain.Resource, p plan, name string, number int64, replicas int) (engine.Outputs, error) {
	c, err := d.inspect(ctx, name)
	if err != nil {
		return nil, err
	}
	out := d.outputs(r, p, c, engine.EnsureInput{})
	out[engine.OutputServingRevision] = number
	out[engine.OutputRunningReplicas] = replicas
	return out, nil
}

func (d *Driver) removeOthers(ctx context.Context, revs []revision, keep ...string) error {
	for _, rev := range revs {
		if !slices.Contains(keep, rev.name) {
			if err := d.remove(ctx, rev.name); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *Driver) start(ctx context.Context, name string) error {
	sctx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	if err := d.cli.ContainerStart(sctx, name, container.StartOptions{}); err != nil {
		return d.classify(fmt.Errorf("start %s: %w", name, err))
	}
	return nil
}

// stopAndRemove stops a container gracefully (SIGTERM, then SIGKILL after the grace period) and removes it.
func (d *Driver) stopAndRemove(ctx context.Context, name string) error {
	sctx, cancel := context.WithTimeout(ctx, stepTimeout)
	grace := stopGrace
	err := d.cli.ContainerStop(sctx, name, container.StopOptions{Timeout: &grace})
	cancel()
	if err != nil && !isNotFound(err) {
		return d.classify(fmt.Errorf("stop %s: %w", name, err))
	}
	return d.remove(ctx, name)
}

// appContainers are the containers that answer for an app now: the serving revision's replicas.
func (d *Driver) appContainers(ctx context.Context, r *domain.Resource) ([]revision, error) {
	revs, err := d.revisions(ctx, r)
	if err != nil {
		return nil, err
	}
	if g := servingOf(revs, r.ServingRevision); g != nil {
		return g.members, nil
	}
	if len(revs) > 0 {
		return revs[:1], nil
	}
	return nil, nil
}

// appHealth is Missing when the serving revision lacks a replica (a new revision repairs it), Stopped
// when one isn't running.
func (d *Driver) appHealth(ctx context.Context, r *domain.Resource) (engine.Health, error) {
	members, err := d.appContainers(ctx, r)
	if err != nil {
		return engine.Missing, err
	}
	if len(members) < replicasOf(r) {
		return engine.Missing, nil
	}
	for _, m := range members {
		if !m.running {
			return engine.Stopped, nil
		}
	}
	return engine.Healthy, nil
}

func (o Options) drain() time.Duration {
	switch {
	case o.Drain < 0:
		return 0
	case o.Drain == 0:
		return DefaultDrain
	}
	return o.Drain
}
