package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
)

// v0.7 read-only methods (ADR-037, ADR-040).

// Stats implements engine.Driver: one-shot stats of each running container answering for r, with its
// restart count from inspect.
func (d *Driver) Stats(ctx context.Context, r *domain.Resource) ([]engine.ContainerStats, error) {
	names, err := d.containersFor(ctx, r)
	if err != nil {
		return nil, err
	}
	var out []engine.ContainerStats
	for _, name := range names {
		sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		s, ok, err := d.containerStats(sctx, name)
		cancel()
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, s)
		}
	}
	return out, nil
}

func (d *Driver) containerStats(ctx context.Context, name string) (engine.ContainerStats, bool, error) {
	c, err := d.cli.ContainerInspect(ctx, name)
	if cerrdefs.IsNotFound(err) {
		return engine.ContainerStats{}, false, nil
	}
	if err != nil {
		return engine.ContainerStats{}, false, d.classify(err)
	}
	if c.State == nil || !c.State.Running {
		return engine.ContainerStats{}, false, nil
	}
	resp, err := d.cli.ContainerStatsOneShot(ctx, name)
	if cerrdefs.IsNotFound(err) {
		return engine.ContainerStats{}, false, nil
	}
	if err != nil {
		return engine.ContainerStats{}, false, d.classify(err)
	}
	defer resp.Body.Close()
	var st container.StatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return engine.ContainerStats{}, false, fmt.Errorf("read stats of %s: %w", name, err)
	}
	started, _ := time.Parse(time.RFC3339Nano, c.State.StartedAt)
	out := engine.ContainerStats{Container: name, ID: c.ID, StartedAt: started, Restarts: c.RestartCount,
		CPUSeconds: float64(st.CPUStats.CPUUsage.TotalUsage) / 1e9, MemoryBytes: memoryUsed(st.MemoryStats), MemoryLimit: int64(st.MemoryStats.Limit)}
	for _, n := range st.Networks {
		out.NetRx += n.RxBytes
		out.NetTx += n.TxBytes
	}
	for _, e := range st.BlkioStats.IoServiceBytesRecursive {
		switch strings.ToLower(e.Op) {
		case "read":
			out.DiskRead += e.Value
		case "write":
			out.DiskWrite += e.Value
		}
	}
	return out, true, nil
}

// memoryUsed is the usage the docker CLI shows: without the page cache that can be reclaimed.
func memoryUsed(m container.MemoryStats) int64 {
	used := m.Usage
	for _, k := range []string{"inactive_file", "total_inactive_file"} {
		if v, ok := m.Stats[k]; ok && v < used {
			return int64(used - v)
		}
	}
	return int64(used)
}

// EdgeRequests implements engine.Driver: Caddy's per-host request histogram (metrics.per_host, ADR-037),
// read from its admin endpoint inside the edge container.
func (d *Driver) EdgeRequests(ctx context.Context) (map[string]engine.RequestCounters, error) {
	c, err := d.inspect(ctx, engine.EdgeName)
	if err != nil || c == nil || c.State == nil || !c.State.Running {
		return map[string]engine.RequestCounters{}, err
	}
	ectx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// Filtering in the container keeps the output small: only the duration histogram's count and sum.
	res, err := d.exec(ectx, engine.EdgeName, []string{"sh", "-c",
		`wget -qO- http://127.0.0.1:2019/metrics | grep -E '^caddy_http_request_duration_seconds_(count|sum)\{'`}, nil)
	if err != nil {
		return nil, err
	}
	return parseEdgeMetrics(res.Output), nil
}

// parseEdgeMetrics sums caddy_http_request_duration_seconds_count and _sum by host; codes 5xx count as
// errors.
func parseEdgeMetrics(text string) map[string]engine.RequestCounters {
	out := map[string]engine.RequestCounters{}
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := sc.Text()
		name, rest, ok := strings.Cut(line, "{")
		if !ok {
			continue
		}
		labels, value, ok := strings.Cut(rest, "} ")
		if !ok {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			continue
		}
		l := parseLabels(labels)
		host := l["host"]
		if host == "" {
			continue
		}
		c := out[host]
		switch name {
		case "caddy_http_request_duration_seconds_count":
			c.Requests += v
			if strings.HasPrefix(l["code"], "5") {
				c.Errors5xx += v
			}
		case "caddy_http_request_duration_seconds_sum":
			c.DurationSeconds += v
		}
		out[host] = c
	}
	return out
}

func parseLabels(s string) map[string]string {
	out := map[string]string{}
	for len(s) > 0 {
		k, rest, ok := strings.Cut(s, `="`)
		if !ok {
			break
		}
		v, rest, _ := strings.Cut(rest, `"`)
		out[strings.TrimLeft(k, ", ")] = v
		s = rest
	}
	return out
}

// LogsSince implements engine.Driver: every container labelled with r's ID (all replicas, every revision
// still there), lines newer than since.
func (d *Driver) LogsSince(ctx context.Context, r *domain.Resource, since map[string]time.Time, fallback time.Time, limit int) ([]engine.ContainerLogLine, error) {
	lctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	list, err := d.cli.ContainerList(lctx, container.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", LabelResourceID+"="+r.ID))})
	if err != nil {
		return nil, d.classify(err)
	}
	var out []engine.ContainerLogLine
	for _, c := range list {
		name := strings.TrimPrefix(firstOr(c.Names, c.ID), "/")
		after, ok := since[name]
		if !ok {
			after = fallback
		}
		rc, err := d.cli.ContainerLogs(lctx, c.ID, container.LogsOptions{ShowStdout: true, ShowStderr: true, Timestamps: true,
			Since: after.Format(time.RFC3339Nano)})
		if cerrdefs.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, d.classify(err)
		}
		var stdout, stderr bytes.Buffer
		_, err = stdcopy.StdCopy(&stdout, &stderr, rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		for _, l := range append(parseLines(stdout.String(), "stdout"), parseLines(stderr.String(), "stderr")...) {
			if ok && !l.Time.Before(after) || !ok && l.Time.After(after) {
				out = append(out, engine.ContainerLogLine{LogLine: l, Container: name})
			}
		}
	}
	slices.SortStableFunc(out, func(a, b engine.ContainerLogLine) int { return a.Time.Compare(b.Time) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func firstOr(names []string, fallback string) string {
	if len(names) > 0 {
		return names[0]
	}
	return fallback
}
