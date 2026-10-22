import { Alert, Button, Card, Chip, EmptyState, Fact, Grid, Icons, Inline, Skeleton, Stack, Table, Text } from '@enclavestack/ui';
import { useState } from 'react';
import { useApi } from '../api/context';
import { ApiError, type AlertSpec, type LogExportSpec, type Resource, type TestResult } from '../api/types';
import { formatBytes } from '../lib/format';
import { formatMetric, logDestinationType, MACHINE_REF, metricOf } from '../lib/operate';
import { useLoad } from '../lib/useLoad';
import { usePolling } from '../lib/usePolling';

// The detail panels of the v0.7 kinds (CON-50 – CON-52, L2.5-002 §14).

const STATE: Record<string, { label: string; tone: 'ok' | 'bad' | 'neutral' | 'warn' }> = {
  ok: { label: 'OK', tone: 'ok' }, firing: { label: 'Firing', tone: 'bad' }, no_data: { label: 'No data', tone: 'neutral' }, disabled: { label: 'Off', tone: 'neutral' },
};

/** "Send a test" for alerts and channels, "Test connection" for secret stores; the answer shows inline. */
export function TestButton({ r, onDone }: { r: Resource; onDone: () => void }) {
  const api = useApi();
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<TestResult | null>(null);
  const [error, setError] = useState<string | null>(null);
  async function test() {
    setBusy(true);
    setError(null);
    try {
      setResult(await api.test(r.id));
      onDone();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : 'That did not work. Try again.');
    } finally {
      setBusy(false);
    }
  }
  return (
    <Stack gap={1}>
      <Button size="sm" icon={<Icons.Send />} disabled={busy} onClick={() => void test()}>{busy ? 'Testing…' : r.kind === 'secretstore' ? 'Test connection' : 'Send a test'}</Button>
      {error && <Text size="xs" tone="bad" role="alert">{error}</Text>}
      {result?.items.map((i) => (
        <Text key={i.target} size="xs" tone={i.ok ? 'ok' : 'bad'} role="status">{i.ok ? '✓' : '✗'} {i.message}</Text>
      ))}
    </Stack>
  );
}

/** The alert's rule as a sentence, and its state (ALR-01). */
export function AlertSummary({ alert }: { alert: Resource }) {
  const a = alert.spec as AlertSpec;
  const c = a.condition;
  const state = STATE[String(alert.outputs.state ?? '')] ?? STATE.no_data;
  const of = c.resources.map((x) => (x === MACHINE_REF ? 'this machine' : x)).join(', ');
  const info = metricOf(c.metric ?? '', c.resources);
  const unit = info?.unit;
  const sentence =
    c.type === 'metric' ? `When ${info?.title ?? c.metric} of ${of} is ${c.operator ?? 'above'} ${unit && c.threshold !== undefined ? formatMetric(c.threshold, unit) : String(c.threshold)} for ${c.forMinutes ?? 5} minutes`
      : c.type === 'crash_loop' ? `When ${of} restart ${c.restarts ?? 3} times in ${c.windowMinutes ?? 10} minutes`
        : c.type === 'failed' ? `When ${of} fail` : `When a run of ${of} fails`;
  return (
    <Card title="Rule">
      <Stack gap={2}>
        <Inline gap={2}>
          <Chip tone={state?.tone ?? 'neutral'}>{state?.label ?? ''}</Chip>
          {alert.outputs.since !== undefined && <Text size="sm" tone="subtle">since {new Date(String(alert.outputs.since)).toLocaleString()}</Text>}
        </Inline>
        <Text>{sentence}, notify {a.channels.join(', ')}{a.repeatMinutes ? `, and remind every ${a.repeatMinutes} minutes while it lasts` : ''}.</Text>
      </Stack>
    </Card>
  );
}

/** An alert's incidents, newest first, with each channel's outcome (ALR-06). */
export function Incidents({ alert }: { alert: Resource }) {
  const api = useApi();
  const list = useLoad(`incidents:${alert.id}:${String(alert.outputs.state ?? '')}`, () => api.listIncidents(alert.id));
  usePolling(true, 10_000, list.reload);
  if (list.error) return <Alert>{list.error.message}</Alert>;
  if (!list.data) return <Skeleton shape="block" />;
  if (!list.data.length) return <EmptyState icon={<Icons.BellRing />} title="No incidents yet">Each time this alert fires, the incident shows here with who was notified.</EmptyState>;
  return (
    <Table>
      <thead>
        <tr><th scope="col">Started</th><th scope="col">Ended</th><th scope="col">Subject</th><th scope="col">Value</th><th scope="col">Notified</th></tr>
      </thead>
      <tbody>
        {list.data.map((i) => (
          <tr key={i.id}>
            <td><Text tone="muted">{new Date(i.startedAt).toLocaleString()}</Text></td>
            <td>{i.resolvedAt ? <Text tone="muted">{new Date(i.resolvedAt).toLocaleString()}</Text> : <Chip tone="bad">Firing</Chip>}</td>
            <td><Text mono size="sm">{i.subject}</Text><Text size="xs" tone="subtle"> {i.message}</Text></td>
            <td><Text mono>{i.value ?? '—'}</Text></td>
            <td>
              <Inline gap={1} wrap>
                {i.notifications.map((n, k) => <Chip key={k} tone={n.status === 'sent' ? 'ok' : n.status === 'failed' ? 'bad' : 'neutral'} title={n.error || undefined}>{n.channel}: {n.event} {n.status}</Chip>)}
              </Inline>
            </td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}

/** A channel's last deliveries (NCH-04). */
export function ChannelDeliveries({ channel, version }: { channel: Resource; version: string }) {
  const api = useApi();
  const list = useLoad(`notifications:${channel.id}:${version}`, () => api.listNotifications(channel.id));
  usePolling(true, 10_000, list.reload);
  if (list.error) return <Alert>{list.error.message}</Alert>;
  if (!list.data) return <Skeleton shape="block" />;
  if (!list.data.length) return <EmptyState icon={<Icons.Megaphone />} title="Nothing sent yet">Send a test, or wait for an alert that uses this channel.</EmptyState>;
  return (
    <Table>
      <thead>
        <tr><th scope="col">When</th><th scope="col">Event</th><th scope="col">Alert</th><th scope="col">Status</th><th scope="col">Attempts</th><th scope="col">Last error</th></tr>
      </thead>
      <tbody>
        {list.data.map((n) => (
          <tr key={n.id}>
            <td><Text tone="muted">{new Date(n.createdAt).toLocaleString()}</Text></td>
            <td><Text>{n.event}</Text></td>
            <td><Text mono size="sm">{n.alert || '—'}</Text></td>
            <td><Chip tone={n.status === 'sent' ? 'ok' : n.status === 'failed' ? 'bad' : 'neutral'}>{n.status}</Chip></td>
            <td><Text mono>{n.attempts}</Text></td>
            <td><Text size="sm" tone={n.lastError ? 'bad' : 'subtle'}>{n.lastError || '—'}</Text></td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}

const DEST_LABEL = { bucket: 'A bucket here', s3: 'S3', http: 'HTTP', otlp: 'OpenTelemetry', loki: 'Grafana Loki', '': '' } as const;

/** Where a log export sends, how far it got, and its last error (LOG-07). */
export function LogExportCard({ r }: { r: Resource }) {
  const l = r.spec as LogExportSpec;
  const type = logDestinationType(l);
  const d = l.destination;
  const where = d.bucket ? `${d.bucket.bucket}/${d.bucket.prefix ?? ''}` : d.s3 ? `${d.s3.endpoint}/${d.s3.bucket}/${d.s3.prefix ?? ''}` : (d.otlp?.endpoint ?? d.http?.url ?? d.loki?.url ?? '');
  const lastError = String(r.outputs.lastError ?? '');
  return (
    <Stack gap={3}>
      {lastError && <Alert tone="warn">The last batch didn&apos;t go: {lastError}. EnclaveStack keeps trying; lines wait on this machine meanwhile.</Alert>}
      <Grid min="sm">
        <Card padding="tight"><Fact label="Sends to" value={`${DEST_LABEL[type]} · ${where}`} mono /></Card>
        <Card padding="tight"><Fact label="Sources" value={l.sources?.length ? l.sources.join(', ') : 'Every resource in the project'} /></Card>
        <Card padding="tight"><Fact label="Lines sent" value={`${String(r.outputs.linesSent ?? 0)} (${formatBytes(Number(r.outputs.bytesSent ?? 0))})`} /></Card>
        <Card padding="tight"><Fact label="Behind by" value={`${String(r.outputs.lagSeconds ?? 0)} s`} /></Card>
        {l.filter && <Card padding="tight"><Fact label="Only lines matching" value={l.filter} mono /></Card>}
      </Grid>
    </Stack>
  );
}

/** A synced secret's store, key and last sync (EXT-03); its value comes from the store. */
export function SyncedSecretCard({ r }: { r: Resource }) {
  const src = (r.spec as { source?: { store: string; key: string; field?: string } }).source;
  if (!src) return null;
  return (
    <Card title="Read from a secret store">
      <Grid min="sm">
        <Fact label="Store" value={src.store} mono />
        <Fact label="Key" value={src.key} mono />
        <Fact label="Field" value={src.field ?? 'The whole value'} mono />
        <Fact label="Last synced" value={r.outputs.syncedAt ? new Date(String(r.outputs.syncedAt)).toLocaleString() : 'Not yet'} />
      </Grid>
    </Card>
  );
}
