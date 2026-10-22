import { Alert, Button, Card, EmptyState, Field, Grid, Icons, Inline, LineChart, SegmentedTabs, Select, Skeleton, Stack, TabPanel, Text, type ChartSeries } from '@enclavestack/ui';
import { useState } from 'react';
import { useApi } from '../api/context';
import { MACHINE_METRICS, RULES, type MetricUnit } from '../api/generated';
import type { DashboardSpec, MetricData, Resource } from '../api/types';
import { formatMetric, metricInfo, metricOf, metricsOf, parseRef, RANGES, type RangeId } from '../lib/operate';
import { useLoad } from '../lib/useLoad';
import { usePolling } from '../lib/usePolling';

// Graphs of metrics (CON-49, L2.5-002 §14): a range picker above small line charts, the latest value
// large above each. The chart itself (colours, hover, table) is the design system's LineChart.

const REFRESH_MS = 30_000;

function RangePicker({ range, onChange, idPrefix }: { range: RangeId; onChange: (r: RangeId) => void; idPrefix: string }) {
  return <SegmentedTabs label="Time range" idPrefix={idPrefix} tabs={RANGES.map((r) => ({ id: r.id, label: r.label }))} selected={range} onSelect={onChange} />;
}

function rangeWindow(range: RangeId): { from: number; to: number } {
  const to = Math.floor(Date.now() / 1000);
  return { from: to - (RANGES.find((r) => r.id === range)?.seconds ?? 3600), to };
}

/** One chart with a headline: the newest value of its single series, or the series count. */
function ChartCard({ title, unit, series, from, to }: { title: string; unit: MetricUnit; series: ChartSeries[]; from: number; to: number }) {
  const only = series.length === 1 ? series[0] : undefined;
  const last = only?.points[only.points.length - 1];
  return (
    <Card padding="tight">
      <Stack gap={2}>
        <Inline justify="between" align="baseline" wrap>
          <Text size="sm" weight="semibold">{title}</Text>
          {last && <Text size="lg" weight="bold" mono>{formatMetric(last.v, unit)}</Text>}
        </Inline>
        <LineChart title={title} series={series} from={from} to={to} format={(v) => formatMetric(v, unit)} empty="No data yet. The first sample comes within a minute." />
      </Stack>
    </Card>
  );
}

function LoadError({ error, retry }: { error: Error; retry: () => void }) {
  return (
    <Alert>
      <Inline justify="between">
        <span>{error.message}</span>
        <Button size="sm" onClick={retry}>Retry</Button>
      </Inline>
    </Alert>
  );
}

/** The Metrics tab of a resource: its standard graphs. */
export function ResourceMetrics({ resource }: { resource: Resource }) {
  const api = useApi();
  const [range, setRange] = useState<RangeId>('1h');
  const { from, to } = rangeWindow(range);
  const data = useLoad(`metrics:${resource.id}:${range}`, () => api.getMetrics(resource.id, [], from, to));
  usePolling(true, REFRESH_MS, data.reload);
  const known = metricsOf(resource.kind);
  return (
    <Stack gap={4}>
      <RangePicker range={range} onChange={setRange} idPrefix="range" />
      <TabPanel idPrefix="range" id={range}>
        {data.error && <LoadError error={data.error} retry={data.reload} />}
        {!data.data && !data.error && <Grid min="lg">{known.slice(0, 4).map((m) => <Skeleton key={m.name} shape="card" />)}</Grid>}
        {data.data && <Charts data={data.data} titles={(m) => metricInfo(m, resource.kind)?.title ?? m} label={(m) => metricInfo(m, resource.kind)?.title ?? m} />}
      </TabPanel>
    </Stack>
  );
}

function Charts({ data, titles, label }: { data: MetricData; titles: (metric: string) => string; label: (metric: string) => string }) {
  const from = Date.parse(data.from) / 1000;
  const to = Date.parse(data.to) / 1000;
  if (!data.items.length) return <EmptyState icon={<Icons.Activity />} title="No metrics for this kind">EnclaveStack samples apps, databases, caches, queues and the other resources that run something.</EmptyState>;
  return (
    <Grid min="lg">
      {data.items.map((s) => (
        <ChartCard key={s.metric} title={titles(s.metric)} unit={s.unit} from={from} to={to} series={[{ label: label(s.metric), points: s.points }]} />
      ))}
    </Grid>
  );
}

/** The machine's own dashboard (MET-05): disk, memory and CPU of EnclaveStack, failed resources. */
export function MachineDashboard() {
  const api = useApi();
  const [range, setRange] = useState<RangeId>('1h');
  // v0.8 (CON-58): one machine's series at a time; disk metrics are the main machine's only.
  const [machine, setMachine] = useState<string>(RULES.mainMachine);
  const machines = useLoad('dashboard-machines', () => api.listMachines());
  const { from, to } = rangeWindow(range);
  const data = useLoad(`metrics:machine:${machine}:${range}`, () => api.getMachineMetricsOf(machine, [], from, to));
  usePolling(true, REFRESH_MS, data.reload);
  const title = (m: string) => MACHINE_METRICS.find((x) => x.name === m)?.title ?? m;
  return (
    <Stack gap={4}>
      {(machines.data?.length ?? 0) > 1 && (
        <Field id="dash-machine" label="Machine" hint={machine === RULES.mainMachine ? undefined : 'Disk charts are the main machine’s only.'}>
          <Select id="dash-machine" value={machine} onChange={(e) => setMachine(e.target.value)}>
            {machines.data?.map((m) => <option key={m.id} value={m.name}>{m.main ? `${m.name} (${m.title})` : m.name}</option>)}
          </Select>
        </Field>
      )}
      <RangePicker range={range} onChange={setRange} idPrefix="range" />
      <TabPanel idPrefix="range" id={range}>
        {data.error && <LoadError error={data.error} retry={data.reload} />}
        {!data.data && !data.error && <Grid min="lg">{MACHINE_METRICS.slice(0, 4).map((m) => <Skeleton key={m.name} shape="card" />)}</Grid>}
        {data.data && <Charts data={data.data} titles={title} label={title} />}
      </TabPanel>
    </Stack>
  );
}

/** A dashboard's widgets: one chart per widget, one line per resource coloured by its order (DSH-02). */
export function DashboardCharts({ dashboard }: { dashboard: Resource<'dashboard'> }) {
  const api = useApi();
  const [range, setRange] = useState<RangeId>('1h');
  const { from, to } = rangeWindow(range);
  const spec = dashboard.spec as DashboardSpec;
  // One request per resource, for every metric its widgets need.
  const data = useLoad(`dashboard:${dashboard.id}:${dashboard.generation}:${range}`, async () => {
    const wanted = new Map<string, Set<string>>();
    for (const w of spec.widgets) for (const ref of w.resources) wanted.set(ref, (wanted.get(ref) ?? new Set()).add(w.metric));
    const all = await api.listResources(undefined, dashboard.project);
    const out = new Map<string, MetricData | Error>();
    await Promise.all([...wanted].map(async ([ref, metrics]) => {
      try {
        if (ref === 'machine') {
          out.set(ref, await api.getMachineMetrics([...metrics], from, to));
          return;
        }
        const [kind, name] = parseRef(ref) ?? [];
        const r = all.find((x) => x.kind === kind && x.name === name);
        out.set(ref, r ? await api.getMetrics(r.id, [...metrics], from, to) : new Error(`${ref} doesn't exist any more.`));
      } catch (e) {
        out.set(ref, e instanceof Error ? e : new Error(String(e)));
      }
    }));
    return out;
  });
  usePolling(true, REFRESH_MS, data.reload);
  return (
    <Stack gap={4}>
      <RangePicker range={range} onChange={setRange} idPrefix="range" />
      <TabPanel idPrefix="range" id={range}>
        {data.error && <LoadError error={data.error} retry={data.reload} />}
        {!data.data && !data.error && <Grid min="lg">{spec.widgets.slice(0, 4).map((w) => <Skeleton key={w.title} shape="card" />)}</Grid>}
        {data.data && (
          <Grid min="lg">
            {spec.widgets.map((w, i) => {
              const missing = w.resources.flatMap((ref) => {
                const d = data.data?.get(ref);
                return d instanceof Error ? [d.message] : [];
              });
              const series = w.resources.map((ref) => {
                const d = data.data?.get(ref);
                const s = d instanceof Error ? undefined : d?.items.find((x) => x.metric === w.metric);
                return { label: ref, points: s?.points ?? [] };
              });
              const unit = metricOf(w.metric, w.resources)?.unit ?? 'count';
              return (
                <Stack key={`${i}-${w.title}`} gap={1}>
                  <ChartCard title={w.title} unit={unit} series={series} from={from} to={to} />
                  {missing.map((m) => <Text key={m} size="xs" tone="warn">{m}</Text>)}
                </Stack>
              );
            })}
          </Grid>
        )}
      </TabPanel>
    </Stack>
  );
}
