// Package monitor watches what runs (ADR-037 – ADR-040): it samples metrics, evaluates alerts, delivers
// notifications and ships logs. It reads the engine through read-only methods only; changing runtime state
// is the reconciler's (internal/archtest).
package monitor

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/enclavestack/platform/internal/backup"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/mail"
	"github.com/enclavestack/platform/internal/messaging"
	"github.com/enclavestack/platform/internal/observability"
	"github.com/enclavestack/platform/internal/secretbox"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// Monitor runs the sampler, the alert evaluator, the notifier and the log shipper.
type Monitor struct {
	Store      *store.Store
	Box        *secretbox.Box
	Driver     engine.Driver     // read-only use: Stats, EdgeRequests, LogsSince
	Hypervisor engine.Hypervisor // read-only use: VMStats
	Broker     *messaging.Pool   // data use: QueueStats
	Mail       mail.Client
	Opener     backup.Opener // log exports to buckets
	HTTP       *http.Client
	Metrics    *observability.Metrics
	Now        func() time.Time
	Log        *slog.Logger
	// DataDir is where EnclaveStack keeps its data: the machine's disk metrics are of this filesystem.
	DataDir string
	// Interval is how often samples are taken and alerts evaluated (api.MetricInterval when zero).
	Interval time.Duration
	// TelegramAPI is the Telegram Bot API base URL (ADR-039); tests point it at a fake.
	TelegramAPI string
	// ConsoleURL is the base of links in notifications ("" leaves them out).
	ConsoleURL func(ctx context.Context) string

	mu   sync.Mutex
	prev map[string]counters // previous cumulative counters, by series key
}

// counters are what a rate is computed from.
type counters struct {
	at     time.Time
	values map[string]float64
}

func (m *Monitor) interval() time.Duration {
	if m.Interval > 0 {
		return m.Interval
	}
	return api.MetricInterval
}

// Run samples and evaluates every interval and sweeps old points every rollup period until ctx ends.
func (m *Monitor) Run(ctx context.Context) {
	tick := time.NewTicker(m.interval())
	defer tick.Stop()
	sweep := time.NewTicker(api.MetricRollup)
	defer sweep.Stop()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); m.notifyLoop(ctx) }()
	go func() { defer wg.Done(); m.shipLoop(ctx) }()
	m.cycle(ctx)
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-tick.C:
			m.cycle(ctx)
		case <-sweep.C:
			m.Sweep(ctx)
		}
	}
}

// cycle is one sample followed by one alert evaluation (ADR-038).
func (m *Monitor) cycle(ctx context.Context) {
	start := time.Now()
	if err := m.SampleOnce(ctx); err != nil {
		m.Metrics.Samples.WithLabelValues("skipped").Inc()
		if ctx.Err() == nil {
			m.Log.Warn("metric sample skipped", "error", err)
		}
	} else {
		m.Metrics.Samples.WithLabelValues("ok").Inc()
	}
	m.Metrics.SampleDuration.Observe(time.Since(start).Seconds())
	if err := m.EvaluateOnce(ctx); err != nil && ctx.Err() == nil {
		m.Log.Warn("alert evaluation failed", "error", err)
	}
}

// Sweep rolls finished 15-minute buckets up, deletes points and rollups past their retention, and drops
// the series of deleted resources (MET-04).
func (m *Monitor) Sweep(ctx context.Context) {
	now := m.Now()
	bucket := int64(api.MetricRollup / time.Second)
	end := now.Unix() / bucket * bucket
	// Roll up from where the last sweep stopped, however long ago: points are never deleted unrolled,
	// even after EnclaveStack was stopped for days.
	var from int64
	if v, err := m.Store.GetSetting(ctx, store.SettingMetricsRolledUpTo); err == nil {
		from, _ = strconv.ParseInt(v, 10, 64)
	}
	if err := m.Store.RollUp(ctx, bucket, from, end); err != nil {
		m.Log.Warn("metric rollup failed", "error", err)
		return
	}
	if err := m.Store.SetSetting(ctx, store.SettingMetricsRolledUpTo, strconv.FormatInt(end, 10)); err != nil {
		m.Log.Warn("record the rollup mark failed", "error", err)
		return
	}
	for {
		more, err := m.Store.SweepMetrics(ctx, now.Add(-api.MetricPointsKept).Unix(), now.Add(-api.MetricRollupsKept).Unix(), 10000)
		if err != nil {
			m.Log.Warn("metric sweep failed", "error", err)
			return
		}
		if !more {
			break
		}
	}
	if _, err := m.Store.DeleteSeriesOfGone(ctx); err != nil {
		m.Log.Warn("drop series of deleted resources failed", "error", err)
	}
	if err := m.Store.SweepIncidents(ctx, now.Add(-IncidentsKept)); err != nil {
		m.Log.Warn("sweep incidents failed", "error", err)
	}
}
