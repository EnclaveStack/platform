package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// Metrics queries (ADR-037, L3-002 §10.4).

// MetricQuery is a parsed metrics request; zero times mean the default window ending now.
type MetricQuery struct {
	Metrics  []string
	From, To time.Time
}

// ResourceMetrics returns a resource's series; no metrics means the kind's whole catalogue.
func (s *Resources) ResourceMetrics(ctx context.Context, id string, q MetricQuery) (api.MetricData, error) {
	r, err := s.Store.GetResource(ctx, id)
	if err != nil {
		return api.MetricData{}, notFound()
	}
	infos := api.MetricsOf(r.Kind)
	if len(infos) == 0 {
		return api.MetricData{}, api.NewError(api.CodeActionNotSupported, fmt.Sprintf("A %s has no metrics.", r.Kind))
	}
	if len(q.Metrics) > 0 {
		infos = nil
		for _, name := range q.Metrics {
			info, ok := api.MetricFor(r.Kind, name)
			if !ok {
				return api.MetricData{}, api.Validation([]api.FieldError{{Field: "metric", Code: api.FieldNotAllowed, Message: fmt.Sprintf("A %s has no %s metric.", r.Kind, name)}})
			}
			infos = append(infos, info)
		}
	}
	return s.metricData(ctx, r.ID, infos, q)
}

// MachineMetrics returns the main machine's own series (MET-03).
func (s *Resources) MachineMetrics(ctx context.Context, q MetricQuery) (api.MetricData, error) {
	m, err := s.Store.GetMachine(ctx, s.Store.MainID())
	if err != nil {
		return api.MetricData{}, err
	}
	return s.MachineMetricsOf(ctx, m.Name, q)
}

// MachineMetricsOf returns one machine's series, by its name (L3-002 §11.2). The disk metrics are the
// main machine's: other machines' series have no points for them.
func (s *Resources) MachineMetricsOf(ctx context.Context, name string, q MetricQuery) (api.MetricData, error) {
	m, err := s.Store.GetMachineByName(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return api.MetricData{}, machineNotFound(name)
	}
	if err != nil {
		return api.MetricData{}, err
	}
	infos := api.MachineMetrics
	if len(q.Metrics) > 0 {
		infos = nil
		for _, name := range q.Metrics {
			info, ok := api.MachineMetric(name)
			if !ok {
				return api.MetricData{}, api.Validation([]api.FieldError{{Field: "metric", Code: api.FieldNotAllowed, Message: "The machine has no " + name + " metric."}})
			}
			infos = append(infos, info)
		}
	}
	return s.metricData(ctx, store.MachineSeriesOf(m.ID, s.Store.MainID()), infos, q)
}

func (s *Resources) metricData(ctx context.Context, seriesOf string, infos []api.MetricInfo, q MetricQuery) (api.MetricData, error) {
	now := s.Now()
	to, from := q.To, q.From
	if to.IsZero() {
		to = now
	}
	if from.IsZero() {
		from = to.Add(-api.MetricDefaultWindow)
	}
	if !from.Before(to) || to.Sub(from) > api.MetricMaxRange {
		return api.MetricData{}, api.Validation([]api.FieldError{{Field: "from", Code: api.FieldOutOfRange, Message: "Use a from before to, at most 31 days apart."}})
	}
	rollups := from.Before(now.Add(-api.MetricPointsKept))
	step := StepFor(to.Sub(from), rollups)
	out := api.MetricData{From: from.UTC(), To: to.UTC(), StepSeconds: int(step / time.Second), Items: make([]api.MetricSeries, 0, len(infos))}
	for _, info := range infos {
		var pts []store.SeriesPoint
		var err error
		if rollups {
			stepS := int64(step / time.Second)
			pts, err = s.Store.QueryRollups(ctx, seriesOf, info.Name, from.Unix(), to.Unix(), stepS)
			// Rollups cover finished buckets only, and none yet on a new install: the rest comes from points.
			next := from.Unix()
			if len(pts) > 0 {
				next = pts[len(pts)-1].TS + stepS
			}
			if err == nil && next <= to.Unix() {
				var recent []store.SeriesPoint
				recent, err = s.Store.QueryPoints(ctx, seriesOf, info.Name, next, to.Unix(), stepS)
				pts = append(pts, recent...)
			}
		} else {
			pts, err = s.Store.QueryPoints(ctx, seriesOf, info.Name, from.Unix(), to.Unix(), int64(step/time.Second))
		}
		if err != nil {
			return api.MetricData{}, err
		}
		series := api.MetricSeries{Metric: info.Name, Unit: info.Unit, Points: make([]api.MetricPoint, len(pts))}
		for i, p := range pts {
			series.Points[i] = api.MetricPoint{T: p.TS, V: p.Value, Max: p.Max}
		}
		out.Items = append(out.Items, series)
	}
	return out, nil
}

// StepFor picks the step for a range: at most MetricMaxPoints points, a multiple of the sample interval
// (or of the rollup period when reading rollups) (MET-05).
func StepFor(span time.Duration, rollups bool) time.Duration {
	unit := api.MetricInterval
	if rollups {
		unit = api.MetricRollup
	}
	n := (span/api.MetricMaxPoints + unit - 1) / unit
	return max(n, 1) * unit
}
