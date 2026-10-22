package monitor

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// The alert evaluator (ADR-038): after each sample, every enabled ready alert judges each resource it
// watches (a subject). State is saved before notifications are queued, in one transaction, so a crash
// re-checks rather than double-sends, and a restart doesn't announce a firing alert again (ALR-08).

// IncidentsKept is how long resolved incidents stay (ALR-05).
const IncidentsKept = 90 * 24 * time.Hour

// verdict is one subject's judgement.
type verdict struct {
	key     string // subject ID, or "kind/name" for a resource that doesn't exist
	ref     string // "kind/name" or "machine"
	state   string
	value   *float64
	message string
	// once marks a run failure: it fires and resolves in one step, with one notification.
	once bool
}

// EvaluateOnce judges every alert once.
func (m *Monitor) EvaluateOnce(ctx context.Context) error {
	alerts, err := m.Store.ResourcesOfKind(ctx, api.KindAlert)
	if err != nil {
		return err
	}
	for _, a := range alerts {
		if a.Phase != api.PhaseReady && a.Phase != api.PhaseUpdating {
			continue
		}
		if err := m.evaluate(ctx, a); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			m.Log.Warn("evaluate alert failed", "resource_id", a.ID, "error", err)
		}
	}
	return nil
}

func (m *Monitor) evaluate(ctx context.Context, a *domain.Resource) error {
	raw, _, err := api.NormalizeSpec(api.KindAlert, a.Spec)
	if err != nil {
		return err
	}
	spec := raw.(api.AlertSpec)
	now := m.Now()
	if !spec.IsEnabled() {
		return m.setAlertOutputs(ctx, a, api.AlertDisabled, 0, now)
	}
	prev, err := m.Store.AlertStates(ctx, a.ID)
	if err != nil {
		return err
	}
	var verdicts []verdict
	for _, ref := range spec.Condition.Resources {
		v, err := m.judge(ctx, a, spec.Condition, ref, prev, now)
		if err != nil {
			return err
		}
		verdicts = append(verdicts, v)
	}
	worst, firing := api.AlertOK, 0
	err = m.Store.WithTx(ctx, func(q *store.Queries) error {
		keep := make([]string, 0, len(verdicts))
		for _, v := range verdicts {
			keep = append(keep, v.key)
			if err := m.transition(ctx, q, a, spec, v, prev[v.key], now); err != nil {
				return err
			}
			if v.state == api.AlertFiring && !v.once {
				firing++
			}
			if rank(v.state) > rank(worst) && !v.once {
				worst = v.state
			}
		}
		return q.DeleteAlertStatesExcept(ctx, a.ID, keep)
	})
	if err != nil {
		return err
	}
	return m.setAlertOutputs(ctx, a, worst, firing, now)
}

func rank(state string) int {
	return map[string]int{api.AlertOK: 0, api.AlertNoData: 1, api.AlertFiring: 2}[state]
}

// judge decides one subject's state.
func (m *Monitor) judge(ctx context.Context, a *domain.Resource, c api.AlertCondition, ref string, prev map[string]store.AlertState, now time.Time) (verdict, error) {
	if ref == api.MachineRef {
		return m.judgeMetric(ctx, c, store.MachineSeries, ref, now)
	}
	if c.Type == api.ConditionMachineOffline {
		return m.judgeMachine(ctx, c, ref, now)
	}
	kind, name, _ := api.ParseRef(ref)
	sub, err := m.Store.GetResourceByName(ctx, a.ProjectName(), kind, name)
	if errors.Is(err, store.ErrNotFound) {
		return verdict{key: ref, ref: ref, state: api.AlertNoData, message: ref + " doesn't exist any more."}, nil
	}
	if err != nil {
		return verdict{}, err
	}
	switch c.Type {
	case api.ConditionMetric:
		v, err := m.judgeMetric(ctx, c, sub.ID, ref, now)
		v.key = sub.ID
		return v, err
	case api.ConditionCrashLoop:
		pts, err := m.Store.RecentPoints(ctx, sub.ID, api.MetricRestarts, now.Add(-time.Duration(c.WindowMinutes)*time.Minute).Unix())
		if err != nil {
			return verdict{}, err
		}
		total := 0.0
		for _, p := range pts {
			total += p.Value
		}
		v := verdict{key: sub.ID, ref: ref, state: api.AlertOK, value: &total}
		if len(pts) == 0 && sub.Phase != api.PhaseReady {
			v.state = api.AlertNoData
		}
		if total >= float64(c.Restarts) {
			v.state, v.message = api.AlertFiring, fmt.Sprintf("%s restarted %s in the last %s.", ref, times(int(total)), api.Minutes(c.WindowMinutes))
		}
		return v, nil
	case api.ConditionFailed:
		v := verdict{key: sub.ID, ref: ref, state: api.AlertOK}
		if sub.Phase == api.PhaseFailed {
			v.state, v.message = api.AlertFiring, fmt.Sprintf("%s failed: %s", ref, sub.Message)
		}
		return v, nil
	case api.ConditionRunFailed:
		since := now.Add(-m.interval())
		if p, ok := prev[sub.ID]; ok {
			since = p.LastCheckedAt
		}
		n, _, err := m.Store.FailedRunsSince(ctx, sub.ID, since)
		if err != nil {
			return verdict{}, err
		}
		v := verdict{key: sub.ID, ref: ref, state: api.AlertOK}
		if n > 0 {
			count := float64(n)
			v.state, v.value, v.once = api.AlertFiring, &count, true
			v.message = fmt.Sprintf("%s of %s failed. %s", runs(n), ref, sub.Message)
		}
		return v, nil
	}
	return verdict{key: sub.ID, ref: ref, state: api.AlertNoData}, nil
}

// judgeMetric fires when every point of the last forMinutes crosses the threshold, and those points
// cover the window; no points at all is no data.
func (m *Monitor) judgeMetric(ctx context.Context, c api.AlertCondition, seriesOf, ref string, now time.Time) (verdict, error) {
	window := time.Duration(c.ForMinutes) * time.Minute
	pts, err := m.Store.RecentPoints(ctx, seriesOf, c.Metric, now.Add(-window).Unix())
	if err != nil {
		return verdict{}, err
	}
	v := verdict{key: seriesOf, ref: ref, state: api.AlertOK}
	title, unit := metricWords(c.Metric, ref)
	if len(pts) == 0 {
		v.state, v.message = api.AlertNoData, fmt.Sprintf("No %s data for %s in the last %s.", strings.ToLower(title), api.RefText(ref), api.Minutes(c.ForMinutes))
		return v, nil
	}
	last := pts[len(pts)-1].Value
	v.value = &last
	covered := now.Unix()-pts[0].TS >= int64((window-m.interval())/time.Second)
	crosses := func(x float64) bool {
		if c.Operator == api.OperatorBelow {
			return x < *c.Threshold
		}
		return x > *c.Threshold
	}
	if covered && !slices.ContainsFunc(pts, func(p store.SeriesPoint) bool { return !crosses(p.Value) }) {
		v.state = api.AlertFiring
		v.message = fmt.Sprintf("%s of %s has been %s %s for %s (now %s).", title, api.RefText(ref), c.Operator, api.FormatMetric(*c.Threshold, unit),
			api.Minutes(c.ForMinutes), api.FormatMetric(last, unit))
	}
	return v, nil
}

// metricWords are a metric's title and unit as the catalogue names them ("Disk full", percent).
func metricWords(metric, ref string) (string, string) {
	info, ok := api.MachineMetric(metric)
	if kind, _, isRef := api.ParseRef(ref); isRef {
		info, ok = api.MetricFor(kind, metric)
	}
	if !ok {
		return metric, ""
	}
	return info.Title, info.Unit
}

func times(n int) string {
	if n == 1 {
		return "once"
	}
	return fmt.Sprintf("%d times", n)
}

func runs(n int) string {
	if n == 1 {
		return "A run"
	}
	return fmt.Sprintf("%d runs", n)
}

// transition saves a subject's state and queues what its change announces.
func (m *Monitor) transition(ctx context.Context, q *store.Queries, a *domain.Resource, spec api.AlertSpec, v verdict, prev store.AlertState, now time.Time) error {
	had := prev.State != ""
	next := store.AlertState{AlertID: a.ID, SubjectID: v.key, State: v.state, Since: prev.Since, LastNotifiedAt: prev.LastNotifiedAt,
		IncidentID: prev.IncidentID, LastCheckedAt: now}
	if !had || prev.State != v.state {
		next.Since = now
	}
	payload := Payload{Alert: a.Name, Project: a.ProjectName(), Resource: v.ref, State: v.state, Value: v.value, Threshold: spec.Condition.Threshold,
		Message: v.message, StartedAt: now}
	if m.ConsoleURL != nil {
		if base := m.ConsoleURL(ctx); base != "" {
			payload.URL = base + "/alerts/" + a.ID
		}
	}
	switch {
	case v.once && v.state == api.AlertFiring:
		// A failed run fires and resolves at once: one incident, one notification.
		id, err := m.openIncident(ctx, q, a, v, now)
		if err != nil {
			return err
		}
		resolved := now
		if err := q.ResolveIncident(ctx, id, resolved); err != nil {
			return err
		}
		payload.Event = api.EventFiring
		if err := m.queueAll(ctx, q, a, spec, id, payload, now); err != nil {
			return err
		}
		next.State, next.IncidentID, next.LastNotifiedAt = api.AlertOK, "", &now
		m.Metrics.AlertTransitions.WithLabelValues(api.AlertFiring).Inc()
	case v.state == api.AlertFiring && prev.State != api.AlertFiring:
		id, err := m.openIncident(ctx, q, a, v, now)
		if err != nil {
			return err
		}
		payload.Event = api.EventFiring
		if err := m.queueAll(ctx, q, a, spec, id, payload, now); err != nil {
			return err
		}
		next.IncidentID, next.LastNotifiedAt = id, &now
		m.Metrics.AlertTransitions.WithLabelValues(api.AlertFiring).Inc()
		if err := q.AddEvent(ctx, a.ID, "alert_firing", truncate(v.message), now); err != nil {
			return err
		}
	case v.state != api.AlertFiring && prev.State == api.AlertFiring:
		if prev.IncidentID != "" {
			if err := q.ResolveIncident(ctx, prev.IncidentID, now); err != nil {
				return err
			}
		}
		payload.Event, payload.StartedAt, payload.ResolvedAt = api.EventResolved, prev.Since, &now
		if payload.Message == "" {
			payload.Message = upperFirst(api.RefText(v.ref)) + " is back to normal."
		}
		if err := m.queueAll(ctx, q, a, spec, prev.IncidentID, payload, now); err != nil {
			return err
		}
		next.IncidentID, next.LastNotifiedAt = "", &now
		m.Metrics.AlertTransitions.WithLabelValues(v.state).Inc()
		if err := q.AddEvent(ctx, a.ID, "alert_resolved", truncate(payload.Message), now); err != nil {
			return err
		}
	case v.state == api.AlertFiring && spec.RepeatMinutes > 0 && prev.LastNotifiedAt != nil &&
		now.Sub(*prev.LastNotifiedAt) >= time.Duration(spec.RepeatMinutes)*time.Minute:
		payload.Event, payload.StartedAt = api.EventRepeat, prev.Since
		if err := m.queueAll(ctx, q, a, spec, prev.IncidentID, payload, now); err != nil {
			return err
		}
		next.LastNotifiedAt = &now
	case had && prev.State != v.state:
		m.Metrics.AlertTransitions.WithLabelValues(v.state).Inc()
	}
	return q.SaveAlertState(ctx, next)
}

func (m *Monitor) openIncident(ctx context.Context, q *store.Queries, a *domain.Resource, v verdict, now time.Time) (string, error) {
	in := store.Incident{ID: domain.NewID("inc"), AlertID: a.ID, Subject: v.ref, StartedAt: now, Value: v.value, Message: v.message}
	return in.ID, q.CreateIncident(ctx, in)
}

// queueAll queues a notification for each of the alert's channels that exists.
func (m *Monitor) queueAll(ctx context.Context, q *store.Queries, a *domain.Resource, spec api.AlertSpec, incidentID string, p Payload, now time.Time) error {
	for _, name := range spec.Channels {
		ch, err := q.GetResourceByName(ctx, a.ProjectName(), api.KindChannel, name)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if _, err := Queue(ctx, q, ch.ID, incidentID, p, now); err != nil {
			return err
		}
	}
	return nil
}

// setAlertOutputs reports the worst subject state when it changed (ALR-04).
func (m *Monitor) setAlertOutputs(ctx context.Context, a *domain.Resource, state string, firing int, now time.Time) error {
	if a.Outputs["state"] == state && fmt.Sprint(a.Outputs["firing"]) == fmt.Sprint(firing) {
		return nil
	}
	since := now.UTC().Format(time.RFC3339)
	if a.Outputs["state"] == state {
		if s, ok := a.Outputs["since"].(string); ok {
			since = s
		}
	}
	return m.Store.MergeOutputs(ctx, a.ID, map[string]any{"state": state, "since": since, "firing": firing})
}

// judgeMachine fires when a machine has been offline for forMinutes (MCH-06); a machine that is gone is no
// data.
func (m *Monitor) judgeMachine(ctx context.Context, c api.AlertCondition, ref string, now time.Time) (verdict, error) {
	name := strings.TrimPrefix(ref, api.MachineRefPrefix)
	v := verdict{key: ref, ref: ref, state: api.AlertOK}
	mc, err := m.Store.GetMachineByName(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		v.state, v.message = api.AlertNoData, "The machine "+name+" doesn't exist any more."
		return v, nil
	}
	if err != nil {
		return v, err
	}
	if mc.State == api.MachineOffline && now.Sub(mc.StateSince) >= time.Duration(c.ForMinutes)*time.Minute {
		minutes := now.Sub(mc.StateSince).Minutes()
		v.state, v.value = api.AlertFiring, &minutes
		v.message = fmt.Sprintf("The machine %s has been offline since %s.", name, mc.StateSince.Local().Format("15:04 on 2 Jan"))
	}
	return v, nil
}
