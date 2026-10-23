import {
  Alert, Button, buttonClass, Card, Columns, EmptyState, Icons, Inline, Page, PageHeader, ProgressBar, Row, Rows, Skeleton, Stack, StatRow, StatTile, Text,
  useToast,
} from '@enclavestack/ui';
import { useState } from 'react';
import { Link } from 'react-router';
import { useApi } from '../api/context';
import { ApiError, type Resource } from '../api/types';
import { MachineDashboard } from '../components/MetricsPanel';
import { SectionIcon } from '../components/SectionIcon';
import { inProgress, StatusBadge } from '../components/StatusBadge';
import { formatAgo, formatBytes, percentOf } from '../lib/format';
import { useProject } from '../lib/project';
import { isNavPage, NAV_GROUPS, sectionByKind, sectionBySlug } from '../lib/sections';
import { useLoad } from '../lib/useLoad';
import { usePolling } from '../lib/usePolling';

// The overview (CON-65, L2.5-002 §16): the whole picture on one page, from what the console already loads.

const REFRESH_MS = 10_000;
const BUSY_MS = 1000;
const RECENT = 8;

function greeting(now = new Date()): string {
  const h = now.getHours();
  return h < 12 ? 'Good morning' : h < 18 ? 'Good afternoon' : 'Good evening';
}

/** A failed resource, or an alert that is firing: what someone should look at. */
export function needsAttention(resources: Resource[]): Resource[] {
  return resources.filter((r) => r.status.phase === 'failed' || (r.kind === 'alert' && r.outputs.state === 'firing'));
}

export function summaryLine(attention: number): string {
  if (attention === 0) return 'Everything is running.';
  return `${attention} ${attention === 1 ? 'resource needs' : 'resources need'} attention.`;
}

function linkTo(r: Resource): string {
  return `/${sectionByKind(r.kind).slug}/${encodeURIComponent(r.id)}`;
}

export function OverviewPage() {
  const api = useApi();
  const toast = useToast();
  const { project, allowed } = useProject();
  const resources = useLoad(`overview:${project}`, () => api.listResources(undefined, project || undefined));
  const machine = useLoad('overview-machine', () => api.getMachine());
  const machines = useLoad('overview-machines', () => api.listMachines());
  const [retrying, setRetrying] = useState<string | null>(null);
  // Follow anything in progress every second, as the sections do (L2.5-002 §4); otherwise every 10 s.
  const busy = (resources.data ?? []).some((r) => inProgress(r.status.phase));
  usePolling(true, busy ? BUSY_MS : REFRESH_MS, resources.reload);
  usePolling(true, REFRESH_MS, machine.reload);

  const live = (resources.data ?? []).filter((r) => r.status.phase !== 'deleting');
  const attention = needsAttention(live);
  const running = live.filter((r) => r.status.phase === 'ready').length;
  const recent = [...live].sort((a, b) => Date.parse(b.updatedAt) - Date.parse(a.updatedAt)).slice(0, RECENT);
  const online = (machines.data ?? []).filter((m) => m.state === 'online').length;
  const m = machine.data;
  const search = project ? `?project=${encodeURIComponent(project)}` : '';

  async function retry(r: Resource) {
    setRetrying(r.id);
    try {
      await api.action(r.id, 'reconcile');
      toast(`Retrying ${r.name}`);
      resources.reload();
    } catch (e) {
      toast(e instanceof ApiError ? e.message : 'That did not work. Try again.');
    } finally {
      setRetrying(null);
    }
  }

  // Viewers get no create actions (CON-48), as in the sections.
  const quick = (
    <>
      {allowed('editor') && <Link to={`/apps${search ? `${search}&` : '?'}new=1`} className={buttonClass({ variant: 'dark' })}><Icons.Plus /> New app</Link>}
      {allowed('editor') && <Link to={`/databases${search ? `${search}&` : '?'}new=1`} className={buttonClass({ variant: 'outline' })}><Icons.Database /> New database</Link>}
      <Link to="/catalogue" className={buttonClass({ variant: 'outline' })}><Icons.Store /> Catalogue</Link>
    </>
  );

  return (
    <Page>
      <PageHeader label="Overview" title={greeting()} lede={resources.data ? summaryLine(attention.length) : 'Looking at everything…'} actions={quick} />
      {resources.error && (
        <Alert>
          <Inline justify="between">
            <span>{resources.error.message}</span>
            <Button size="sm" onClick={resources.reload}>Retry</Button>
          </Inline>
        </Alert>
      )}
      <StatRow label="Headline numbers">
        <StatTile label="Running" icon={<Icons.Activity />} value={running} note={resources.data ? `of ${live.length} resources` : '…'} tone="ok" />
        <StatTile label="Need attention" icon={<Icons.CircleAlert />} value={attention.length} note="failed or alerting" tone={attention.length ? 'bad' : 'plain'} />
        <StatTile label="Machines online" icon={<Icons.Server />} value={machines.data ? `${online} of ${machines.data.length}` : '…'} note={<Link to="/machines">See machines</Link>} />
        <StatTile label="Memory reserved" icon={<Icons.HardDrive />} value={m ? formatBytes(m.memoryReservedBytes) : '…'}
          note={m?.memoryTotalBytes ? <ProgressBar label="Memory reserved" value={percentOf(m.memoryReservedBytes, m.memoryTotalBytes)} /> : 'Total memory unknown'} />
      </StatRow>
      <Columns>
        <Card as="section" title="This machine">
          <MachineDashboard initialRange="24h" />
        </Card>
        <Card as="section" title="Needs attention">
          {!resources.data ? <Skeleton shape="block" /> : attention.length === 0 ? (
            <EmptyState icon={<Icons.ShieldCheck />} title="Nothing needs attention">Every resource is running and no alert is firing.</EmptyState>
          ) : (
            <Rows>
              {attention.map((r) => (
                <Row key={r.id}>
                  <Stack gap={0} grow>
                    <Link to={linkTo(r)}><Text weight="semibold">{r.name}</Text></Link>
                    <Text size="sm" tone="subtle">{r.kind === 'alert' ? 'Alert firing' : r.status.message || 'Failed'}</Text>
                  </Stack>
                  {r.status.phase === 'failed'
                    ? <Button size="sm" icon={<Icons.RotateCw />} disabled={retrying === r.id} onClick={() => void retry(r)}>Retry</Button>
                    : <Link to={linkTo(r)} className={buttonClass({ size: 'sm' })}>Open</Link>}
                </Row>
              ))}
            </Rows>
          )}
        </Card>
      </Columns>
      <Columns ratio="even">
        <Card as="section" title="Recently changed">
          {!resources.data ? <Skeleton shape="block" /> : recent.length === 0 ? (
            <EmptyState icon={<Icons.History />} title="Nothing yet">What you create shows here.</EmptyState>
          ) : (
            <Rows>
              {recent.map((r) => (
                <Row key={r.id}>
                  <SectionIcon kind={r.kind} />
                  <Stack gap={0} grow>
                    <Link to={linkTo(r)}><Text weight="semibold">{r.name}</Text></Link>
                    <Text size="sm" tone="subtle">{sectionByKind(r.kind).noun} · {formatAgo(r.updatedAt)}</Text>
                  </Stack>
                  <StatusBadge phase={r.status.phase} />
                </Row>
              ))}
            </Rows>
          )}
        </Card>
        <Card as="section" title="What's running">
          <Rows>
            {NAV_GROUPS.map((g) => {
              const kinds = g.items.filter((i) => !isNavPage(i)).map((i) => sectionBySlug(i)).filter((s) => s !== undefined);
              const counted = kinds.map((s) => ({ s, n: live.filter((r) => r.kind === s.kind).length })).filter((x) => x.n > 0);
              if (counted.length === 0) return null;
              return (
                <Row key={g.id}>
                  <Text weight="semibold">{g.heading}</Text>
                  <Inline gap={3} wrap>
                    {counted.map(({ s, n }) => <Link key={s.slug} to={`/${s.slug}${search}`}><Text size="sm">{s.label} {n}</Text></Link>)}
                  </Inline>
                </Row>
              );
            })}
            {resources.data && live.length === 0 && <Text tone="subtle">Nothing yet. Start with an app, a database or something from the catalogue.</Text>}
          </Rows>
        </Card>
      </Columns>
    </Page>
  );
}
