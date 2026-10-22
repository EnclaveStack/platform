package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Metrics are the control plane's Prometheus instruments (L3-007 §6).
type Metrics struct {
	Registry          *prometheus.Registry
	HTTPRequests      *prometheus.CounterVec
	HTTPDuration      *prometheus.HistogramVec
	ReconcileTotal    *prometheus.CounterVec
	ReconcileDuration *prometheus.HistogramVec
	Resources         *prometheus.GaugeVec
	EngineUp          prometheus.Gauge
	RunsTotal         *prometheus.CounterVec
	RunDuration       *prometheus.HistogramVec
	// v0.4 (L3-007 §9).
	FirewallApply        *prometheus.CounterVec
	FirewallApplySeconds prometheus.Histogram
	LBHealthyTargets     *prometheus.GaugeVec
	DNSCalls             *prometheus.CounterVec
	ACMEOrders           *prometheus.CounterVec
	// v0.5 (L3-007 §10).
	VMs             *prometheus.GaugeVec
	VMStarts        *prometheus.CounterVec
	ImageDownloads  *prometheus.CounterVec
	Snapshots       *prometheus.CounterVec
	SnapshotSeconds *prometheus.HistogramVec
	// v0.6 (L3-007 §11); transcoder runs count in RunsTotal and RunDuration.
	MessagesSent *prometheus.CounterVec
	Deliveries   *prometheus.CounterVec
	DeadLetters  prometheus.Counter
	Purges       *prometheus.CounterVec
	// v0.7 (L3-007 §12).
	ResourceValue    *prometheus.GaugeVec
	Samples          *prometheus.CounterVec
	SampleDuration   prometheus.Histogram
	AlertTransitions *prometheus.CounterVec
	Notifications    *prometheus.CounterVec
	LogLinesExported *prometheus.CounterVec
	LogExportErrors  *prometheus.CounterVec
	LogExportGaps    *prometheus.CounterVec
	SecretSyncs      *prometheus.CounterVec
}

// NewMetrics registers every instrument on a fresh registry (one per server, so tests don't collide).
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		Registry: reg,
		HTTPRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "enclave_http_requests_total", Help: "HTTP requests by method, route and status.",
		}, []string{"method", "route", "status"}),
		HTTPDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "enclave_http_request_duration_seconds", Help: "HTTP request latency.",
			Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5},
		}, []string{"method", "route"}),
		ReconcileTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "enclave_reconcile_total", Help: "Reconcile outcomes by kind and result.",
		}, []string{"kind", "result"}),
		ReconcileDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "enclave_reconcile_duration_seconds", Help: "Reconcile duration by kind.",
			Buckets: prometheus.ExponentialBuckets(0.01, 3, 10),
		}, []string{"kind"}),
		Resources: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "enclave_resources", Help: "Resources by kind and phase.",
		}, []string{"kind", "phase"}),
		EngineUp: prometheus.NewGauge(prometheus.GaugeOpts{Name: "enclave_engine_up", Help: "1 when the container engine answers."}),
		RunsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "enclave_runs_total", Help: "Job, backup and transcoder runs by kind and final status.",
		}, []string{"kind", "status"}),
		RunDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "enclave_run_duration_seconds", Help: "Run duration by kind.",
			Buckets: prometheus.ExponentialBuckets(0.1, 4, 10),
		}, []string{"kind"}),
		FirewallApply: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "enclave_firewall_apply_total", Help: "Firewall applications to a target by result.",
		}, []string{"result"}),
		FirewallApplySeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "enclave_firewall_apply_seconds", Help: "Time to apply a firewall policy to one target's containers.",
			Buckets: prometheus.ExponentialBuckets(0.01, 2.5, 10),
		}),
		LBHealthyTargets: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "enclave_lb_healthy_targets", Help: "Replicas passing their checks, by load balancer.",
		}, []string{"name"}),
		DNSCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "enclave_dns_calls_total", Help: "DNS provider calls by provider and result.",
		}, []string{"provider", "result"}),
		ACMEOrders: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "enclave_acme_orders_total", Help: "ACME certificate orders by challenge and result.",
		}, []string{"challenge", "result"}),
		VMs: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "enclave_vms", Help: "VMs by power state.",
		}, []string{"power"}),
		VMStarts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "enclave_vm_starts_total", Help: "VM process starts by reason (create, start, restart, config, crash, adopt_failed).",
		}, []string{"reason"}),
		ImageDownloads: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "enclave_image_downloads_total", Help: "VM image downloads by result.",
		}, []string{"result"}),
		Snapshots: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "enclave_snapshots_total", Help: "Snapshots taken by type and result.",
		}, []string{"type", "result"}),
		SnapshotSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "enclave_snapshot_seconds", Help: "Time to copy a snapshot, by type.",
			Buckets: prometheus.ExponentialBuckets(0.1, 3, 10),
		}, []string{"type"}),
		MessagesSent: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "enclave_messages_sent_total", Help: "Messages sent to queues and published to topics through the API, by kind.",
		}, []string{"kind"}),
		Deliveries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "enclave_deliveries_total", Help: "Topic deliveries by target type (queue, webhook) and result (delivered, retry, gave_up).",
		}, []string{"target_type", "result"}),
		DeadLetters: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "enclave_dead_letters_total", Help: "Queue messages that reached their maximum receives.",
		}),
		Purges: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "enclave_purges_total", Help: "Queue and CDN purges by kind.",
		}, []string{"kind"}),
		ResourceValue: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "enclave_resource_value", Help: "The latest value of each resource metric (MET-07); units as the API's metric catalogue.",
		}, []string{"project", "kind", "name", "metric"}),
		Samples: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "enclave_samples_total", Help: "Metric sampling passes by result (ok, skipped).",
		}, []string{"result"}),
		SampleDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "enclave_sample_duration_seconds", Help: "Time one sampling pass takes.", Buckets: prometheus.ExponentialBuckets(0.01, 2.5, 10),
		}),
		AlertTransitions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "enclave_alert_transitions_total", Help: "Alert subject state changes, by the state entered.",
		}, []string{"to"}),
		Notifications: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "enclave_notifications_total", Help: "Notification attempts by channel type and result (sent, retry, failed).",
		}, []string{"type", "result"}),
		LogLinesExported: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "enclave_log_lines_exported_total", Help: "Log lines accepted by export destinations, by destination type.",
		}, []string{"destination"}),
		LogExportErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "enclave_log_export_errors_total", Help: "Failed log export batches, by destination type.",
		}, []string{"destination"}),
		LogExportGaps: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "enclave_log_export_gaps_total", Help: "Lines lost to the engine's log rotation before a log export read them, counted once per container and pass.",
		}, []string{"destination"}),
		SecretSyncs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "enclave_secret_syncs_total", Help: "Reads of secrets from secret stores, by provider and result.",
		}, []string{"provider", "result"}),
	}
	reg.MustRegister(m.HTTPRequests, m.HTTPDuration, m.ReconcileTotal, m.ReconcileDuration, m.Resources, m.EngineUp, m.RunsTotal, m.RunDuration,
		m.FirewallApply, m.FirewallApplySeconds, m.LBHealthyTargets, m.DNSCalls, m.ACMEOrders,
		m.VMs, m.VMStarts, m.ImageDownloads, m.Snapshots, m.SnapshotSeconds,
		m.MessagesSent, m.Deliveries, m.DeadLetters, m.Purges,
		m.ResourceValue, m.Samples, m.SampleDuration, m.AlertTransitions, m.Notifications, m.LogLinesExported, m.LogExportErrors, m.LogExportGaps, m.SecretSyncs,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}
