package monitor

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/backup"
	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/internal/webhook"
	"github.com/enclavestack/platform/pkg/api"
)

// The log shipper (ADR-040): each ready export, every flushSeconds, reads its sources' new lines through
// the engine's log API and sends them in one batch per source; checkpoints move only after the
// destination accepted the batch (at least once, in order per source).

const (
	shipTick       = time.Second
	shipLineLimit  = 5000
	shipTimeout    = 30 * time.Second
	shipBackoffMax = 5 * time.Minute
)

// ExportLine is one exported line (LOG-03).
type ExportLine struct {
	Time      time.Time `json:"time"`
	Project   string    `json:"project"`
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	Container string    `json:"container"`
	Stream    string    `json:"stream"`
	Message   string    `json:"message"`
}

// shipState is an export's schedule, kept in memory.
type shipState struct {
	next     time.Time
	failures int
}

func (m *Monitor) shipLoop(ctx context.Context) {
	t := time.NewTicker(shipTick)
	defer t.Stop()
	states := map[string]*shipState{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		exports, err := m.Store.ResourcesOfKind(ctx, api.KindLogExport)
		if err != nil {
			continue
		}
		now := m.Now()
		for _, x := range exports {
			if x.Phase != api.PhaseReady && x.Phase != api.PhaseUpdating {
				continue
			}
			st := states[x.ID]
			if st == nil {
				st = &shipState{}
				states[x.ID] = st
			}
			if now.Before(st.next) {
				continue
			}
			spec, err := logExportSpec(x)
			if err != nil {
				continue
			}
			if err := m.ShipOnce(ctx, x); err != nil {
				st.failures++
				st.next = now.Add(min(time.Duration(1<<min(st.failures, 9))*time.Second, shipBackoffMax))
				continue
			}
			st.failures, st.next = 0, now.Add(time.Duration(spec.FlushSeconds)*time.Second)
		}
	}
}

func logExportSpec(x *domain.Resource) (api.LogExportSpec, error) {
	raw, _, err := api.NormalizeSpec(api.KindLogExport, x.Spec)
	if err != nil {
		return api.LogExportSpec{}, err
	}
	return raw.(api.LogExportSpec), nil
}

// ShipOnce sends every source's new lines once. It returns the first destination error.
func (m *Monitor) ShipOnce(ctx context.Context, x *domain.Resource) error {
	spec, err := logExportSpec(x)
	if err != nil {
		return err
	}
	var filter *regexp.Regexp
	if spec.Filter != "" {
		filter = regexp.MustCompile(spec.Filter) // validated on write
	}
	sources, err := m.logSources(ctx, x, spec)
	if err != nil {
		return err
	}
	cursors, err := m.Store.LogCursors(ctx, x.ID)
	if err != nil {
		return err
	}
	stats, err := m.Store.GetLogExportStats(ctx, x.ID)
	if err != nil {
		return err
	}
	dest, err := m.destination(ctx, x, spec.Destination)
	if err != nil {
		m.noteExportError(ctx, x, &stats, err)
		return err
	}
	var firstErr error
	for _, src := range sources {
		since := make(map[string]time.Time, len(cursors))
		for k, c := range cursors {
			since[k] = c.At
		}
		lines, err := m.Driver.LogsSince(ctx, src, since, x.CreatedAt, shipLineLimit)
		if err != nil {
			continue // the engine is down; the next pass catches up
		}
		var batch []ExportLine
		last := map[string]store.LogCursor{}
		atCheckpoint := map[string]int{}  // lines read back at each checkpoint's own time
		gaps := map[string][2]time.Time{} // containers whose checkpoint line is gone: [checkpoint, first line still kept]
		for _, l := range lines {
			key := l.Container // container names are unique on the machine (ADR-035)
			cur, ok := cursors[key]
			if ok && l.Time.Equal(cur.At) {
				// Lines sharing the checkpoint's time: the first Skip of them were sent (ADR-040).
				if atCheckpoint[key]++; atCheckpoint[key] <= cur.Skip {
					continue
				}
			} else if ok && atCheckpoint[key] == 0 {
				if _, seen := gaps[key]; !seen {
					gaps[key] = [2]time.Time{cur.At, l.Time} // the engine no longer has the checkpoint's line (LOG-06)
				}
			}
			if prev, seen := last[key]; seen && prev.At.Equal(l.Time) {
				last[key] = store.LogCursor{At: l.Time, Skip: prev.Skip + 1}
			} else if ok && l.Time.Equal(cur.At) {
				last[key] = store.LogCursor{At: l.Time, Skip: cur.Skip + 1}
			} else {
				last[key] = store.LogCursor{At: l.Time, Skip: 1}
			}
			if filter != nil && !filter.MatchString(l.Message) {
				continue
			}
			batch = append(batch, ExportLine{Time: l.Time, Project: src.ProjectName(), Kind: string(src.Kind), Name: src.Name, Container: l.Container,
				Stream: l.Stream, Message: l.Message})
		}
		if len(last) == 0 {
			continue
		}
		n, err := dest.send(ctx, src, batch)
		if err != nil {
			m.Metrics.LogExportErrors.WithLabelValues(spec.Destination.Type()).Inc()
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		err = m.Store.WithTx(ctx, func(q *store.Queries) error {
			for key, at := range last {
				if err := q.SaveLogCursor(ctx, x.ID, key, at); err != nil {
					return err
				}
				cursors[key] = at
			}
			return nil
		})
		if err != nil {
			return err
		}
		for key, gap := range gaps {
			m.Metrics.LogExportGaps.WithLabelValues(spec.Destination.Type()).Inc()
			m.event(ctx, x, "export_gap", fmt.Sprintf("Lines of %s written between %s and %s were no longer kept by the engine (its log rotation, or the container was replaced), so they weren't sent.",
				key, gap[0].UTC().Format(time.RFC3339Nano), gap[1].UTC().Format(time.RFC3339Nano)))
		}
		now := m.Now()
		stats.LinesSent, stats.BytesSent, stats.LastSentAt = stats.LinesSent+int64(len(batch)), stats.BytesSent+int64(n), &now
		if len(batch) > 0 {
			stats.LagSeconds = max(0, now.Sub(batch[len(batch)-1].Time).Seconds())
		}
		m.Metrics.LogLinesExported.WithLabelValues(spec.Destination.Type()).Add(float64(len(batch)))
	}
	if firstErr != nil {
		m.noteExportError(ctx, x, &stats, firstErr)
		return firstErr
	}
	recovered := stats.LastError != ""
	stats.LastError = ""
	if err := m.Store.SaveLogExportStats(ctx, x.ID, stats); err != nil {
		return err
	}
	return m.exportOutputs(ctx, x, stats, recovered)
}

// logSources are the named sources of the project, or every resource of it that has logs (LOG-01).
func (m *Monitor) logSources(ctx context.Context, x *domain.Resource, spec api.LogExportSpec) ([]*domain.Resource, error) {
	all, err := m.Store.ResourcesInProject(ctx, x.ProjectName())
	if err != nil {
		return nil, err
	}
	var out []*domain.Resource
	for _, r := range all {
		if !slices.Contains(api.LogKinds, r.Kind) || r.Phase == api.PhaseDeleting {
			continue
		}
		if len(spec.Sources) > 0 && !slices.Contains(spec.Sources, r.Ref()) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func (m *Monitor) noteExportError(ctx context.Context, x *domain.Resource, stats *store.LogExportStats, err error) {
	first := stats.LastError == ""
	stats.LastError = truncate(err.Error())
	_ = m.Store.SaveLogExportStats(ctx, x.ID, *stats)
	if first {
		m.event(ctx, x, "export_failing", "Sending logs failed; retrying with backoff: "+stats.LastError)
	}
	_ = m.exportOutputs(ctx, x, *stats, false)
}

func (m *Monitor) exportOutputs(ctx context.Context, x *domain.Resource, s store.LogExportStats, recovered bool) error {
	out := map[string]any{"linesSent": s.LinesSent, "bytesSent": s.BytesSent, "lagSeconds": s.LagSeconds, "lastError": s.LastError}
	if s.LastSentAt != nil {
		out["lastSentAt"] = s.LastSentAt.UTC().Format(time.RFC3339)
	}
	if recovered {
		m.event(ctx, x, "export_recovered", "Sending logs works again.")
	}
	return m.Store.MergeOutputs(ctx, x.ID, out)
}

// destination sends one source's batch; it returns the bytes sent.
type destination interface {
	send(ctx context.Context, src *domain.Resource, lines []ExportLine) (int, error)
}

func (m *Monitor) destination(ctx context.Context, x *domain.Resource, d api.LogDestination) (destination, error) {
	headers := func(secret string) (map[string]string, error) {
		h := map[string]string{}
		if secret == "" {
			return h, nil
		}
		v, err := m.secretValue(ctx, x.ProjectName(), secret)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(v), &h); err != nil {
			return nil, fmt.Errorf("the secret %s must hold a JSON object of header names to values", secret)
		}
		return h, nil
	}
	switch {
	case d.Bucket != nil:
		t, err := m.bucketTarget(ctx, x.ProjectName(), d.Bucket.Bucket)
		if err != nil {
			return nil, err
		}
		return m.objectDest(ctx, t, d.Bucket.Prefix)
	case d.S3 != nil:
		raw, err := m.secretValue(ctx, x.ProjectName(), d.S3.CredentialsSecret)
		if err != nil {
			return nil, err
		}
		var c struct{ AccessKey, SecretKey string }
		if json.Unmarshal([]byte(raw), &c) != nil || c.AccessKey == "" {
			return nil, fmt.Errorf(`the secret %s must hold JSON {"accessKey": "…", "secretKey": "…"}`, d.S3.CredentialsSecret)
		}
		return m.objectDest(ctx, backup.Target{Endpoint: d.S3.Endpoint, Region: d.S3.Region, Bucket: d.S3.Bucket, AccessKey: c.AccessKey, SecretKey: c.SecretKey}, d.S3.Prefix)
	case d.HTTP != nil:
		h, err := headers(d.HTTP.HeadersSecret)
		return &httpDest{m: m, url: d.HTTP.URL, headers: h, format: ndjson}, err
	case d.OTLP != nil:
		h, err := headers(d.OTLP.HeadersSecret)
		return &httpDest{m: m, url: withPath(d.OTLP.Endpoint, "/v1/logs"), headers: h, format: otlp}, err
	case d.Loki != nil:
		h, err := headers(d.Loki.HeadersSecret)
		return &httpDest{m: m, url: withPath(d.Loki.URL, "/loki/api/v1/push"), headers: h, format: loki(d.Loki.Labels)}, err
	}
	return nil, errors.New("the export has no destination")
}

func withPath(base, path string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, path) {
		return base
	}
	return base + path
}

// bucketTarget reaches a EnclaveStack bucket with the object store's credentials.
func (m *Monitor) bucketTarget(ctx context.Context, project, name string) (backup.Target, error) {
	b, err := m.Store.GetResourceByName(ctx, project, api.KindBucket, name)
	if err != nil {
		return backup.Target{}, fmt.Errorf("the bucket %s doesn't exist", name)
	}
	endpoint, _ := b.Outputs["endpoint"].(string)
	if b.Phase != api.PhaseReady || endpoint == "" {
		return backup.Target{}, fmt.Errorf("the bucket %s isn't ready", name)
	}
	raw, err := m.secretValue(ctx, api.DefaultProject, domain.ObjectStoreCredentialsSecret)
	if err != nil {
		return backup.Target{}, err
	}
	var c struct{ AccessKey, SecretKey string }
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return backup.Target{}, errors.New("the object store's credentials are unreadable")
	}
	return backup.Target{Endpoint: endpoint, Bucket: b.Name, AccessKey: c.AccessKey, SecretKey: c.SecretKey}, nil
}

type objectDest struct {
	st     backup.Store
	prefix string
	now    func() time.Time
}

func (m *Monitor) objectDest(ctx context.Context, t backup.Target, prefix string) (destination, error) {
	if m.Opener == nil {
		return nil, errors.New("this control plane can't write to buckets")
	}
	st, err := m.Opener.Open(ctx, t)
	if err != nil {
		return nil, fmt.Errorf("the destination can't be reached: %w", err)
	}
	return &objectDest{st: st, prefix: prefix, now: m.Now}, nil
}

// send writes one gzip file of JSON lines: <prefix><yyyy>/<mm>/<dd>/<hh>/<kind>-<name>-<unix nanos>.ndjson.gz.
func (o *objectDest) send(ctx context.Context, src *domain.Resource, lines []ExportLine) (int, error) {
	if len(lines) == 0 {
		return 0, nil
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	body, _ := ndjson(src, lines)
	if _, err := gz.Write(body); err != nil {
		return 0, err
	}
	if err := gz.Close(); err != nil {
		return 0, err
	}
	at := lines[0].Time.UTC()
	key := fmt.Sprintf("%s%s/%s-%s-%d.ndjson.gz", o.prefix, at.Format("2006/01/02/15"), src.Kind, src.Name, o.now().UnixNano())
	n := buf.Len()
	ctx, cancel := context.WithTimeout(ctx, shipTimeout)
	defer cancel()
	if _, err := o.st.Put(ctx, key, &buf, map[string]string{"enclave-project": src.ProjectName()}); err != nil {
		return 0, fmt.Errorf("the bucket refused the upload: %w", err)
	}
	return n, nil
}

type httpDest struct {
	m       *Monitor
	url     string
	headers map[string]string
	format  func(src *domain.Resource, lines []ExportLine) ([]byte, string)
}

func (h *httpDest) send(ctx context.Context, src *domain.Resource, lines []ExportLine) (int, error) {
	if len(lines) == 0 {
		return 0, nil
	}
	body, contentType := h.format(src, lines)
	headers := map[string]string{"Content-Type": contentType}
	for k, v := range h.headers {
		headers[k] = v
	}
	if err := webhook.PostWithin(ctx, h.m.HTTP, shipTimeout, h.url, body, headers); err != nil {
		return 0, fmt.Errorf("the destination %w", err)
	}
	return len(body), nil
}

func ndjson(_ *domain.Resource, lines []ExportLine) ([]byte, string) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	for _, l := range lines {
		_ = enc.Encode(l)
	}
	return b.Bytes(), "application/x-ndjson"
}

// otlp encodes one OTLP/HTTP JSON request: the source as the resource, one record per line.
func otlp(src *domain.Resource, lines []ExportLine) ([]byte, string) {
	attr := func(k, v string) map[string]any {
		return map[string]any{"key": k, "value": map[string]any{"stringValue": v}}
	}
	records := make([]map[string]any, 0, len(lines))
	for _, l := range lines {
		severity := "INFO"
		if l.Stream == "stderr" {
			severity = "WARN"
		}
		records = append(records, map[string]any{
			"timeUnixNano": strconv.FormatInt(l.Time.UnixNano(), 10), "severityText": severity,
			"body":       map[string]any{"stringValue": l.Message},
			"attributes": []map[string]any{attr("container", l.Container), attr("stream", l.Stream)},
		})
	}
	body, _ := json.Marshal(map[string]any{"resourceLogs": []map[string]any{{
		"resource":  map[string]any{"attributes": []map[string]any{attr("service.name", src.Name), attr("enclave.project", src.ProjectName()), attr("enclave.kind", string(src.Kind))}},
		"scopeLogs": []map[string]any{{"scope": map[string]any{"name": "enclave"}, "logRecords": records}},
	}}})
	return body, "application/json"
}

// loki encodes a Loki push request: one stream per stdout and stderr.
func loki(extra map[string]string) func(*domain.Resource, []ExportLine) ([]byte, string) {
	return func(src *domain.Resource, lines []ExportLine) ([]byte, string) {
		streams := map[string][][2]string{}
		for _, l := range lines {
			streams[l.Stream] = append(streams[l.Stream], [2]string{strconv.FormatInt(l.Time.UnixNano(), 10), l.Message})
		}
		var out []map[string]any
		for _, stream := range []string{"stdout", "stderr"} {
			values := streams[stream]
			if len(values) == 0 {
				continue
			}
			labels := map[string]string{"project": src.ProjectName(), "kind": string(src.Kind), "name": src.Name, "stream": stream}
			for k, v := range extra {
				labels[k] = v
			}
			out = append(out, map[string]any{"stream": labels, "values": values})
		}
		body, _ := json.Marshal(map[string]any{"streams": out})
		return body, "application/json"
	}
}
