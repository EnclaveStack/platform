import {
  Alert, Badge, Button, buttonClass, CodeBlock, Drawer, EmptyState, Icons, Inline, Input, Page, PageHeader, Skeleton, Stack, StatRow, StatTile, Table, Text, useToast,
  VisuallyHidden,
} from '@enclavestack/ui';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link, Navigate, useNavigate, useParams, useSearchParams } from 'react-router';
import { useApi } from '../api/context';
import { MachineDashboard } from '../components/MetricsPanel';
import { SectionIcon } from '../components/SectionIcon';
import { inProgress, StatusBadge } from '../components/StatusBadge';
import { filterResources } from '../lib/filter';
import { formatAgo, formatBytes } from '../lib/format';
import { useProject } from '../lib/project';
import { needRole } from '../lib/roles';
import { inSentence, sectionBySlug } from '../lib/sections';
import { useLoad } from '../lib/useLoad';
import { usePolling } from '../lib/usePolling';
import { ResourceDetailView } from './ResourceDetailView';
import { ResourceForm } from './ResourceForm';
import { RULES } from '../api/generated';
import type { Resource, TunnelProvider, TunnelSpec } from '../api/types';
import { WAY_TITLE } from '../components/PublicAccessFields';

export const ALERT_STATE: Record<string, string> = { ok: 'OK', firing: 'Firing', no_data: 'No data', disabled: 'Off' };

/** A short fact for the list card (CON-05). */
function keyFact(r: Resource): string {
  const o = r.outputs;
  if (r.kind === 'bucket') return (r.spec as { versioning?: boolean }).versioning ? 'versioned' : '';
  if (r.kind === 'secret') {
    const src = (r.spec as { source?: { store: string } }).source;
    return r.ownerId ? 'managed' : src ? `from ${src.store}` : `v${r.generation}`;
  }
  if (r.kind === 'network') return (r.spec as { cidr?: string }).cidr ?? '';
  if (r.kind === 'firewall') return `${(r.spec as { targets?: string[] }).targets?.length ?? 0} target(s)`;
  if (r.kind === 'loadbalancer' && o.targets !== undefined) return `${String(o.healthyTargets)} of ${String(o.targets)} healthy`;
  if (r.kind === 'zone' || r.kind === 'record') return String(o.fqdn ?? (r.spec as { zone?: string }).zone ?? '');
  if (r.kind === 'app' && Number(o.runningReplicas ?? 1) > 1) return `${String(o.runningReplicas)} replicas`;
  if (r.kind === 'vm') {
    const v = r.spec as { cpus?: number; memoryMiB?: number; power?: string };
    return `${v.power === 'stopped' ? 'stopped' : 'running'} · ${v.cpus ?? 1} CPU · ${formatBytes((v.memoryMiB ?? 1024) * 2 ** 20)}${o.privateIp ? ` · ${String(o.privateIp)}` : ''}`;
  }
  if (r.kind === 'volume') {
    const v = r.spec as { type?: string; sizeGiB?: number };
    return v.type === 'disk' ? `disk · ${v.sizeGiB ?? 10} GiB` : `filesystem${o.usedBytes !== undefined ? ` · ${formatBytes(Number(o.usedBytes))} used` : ''}`;
  }
  if (r.kind === 'alert') return ALERT_STATE[String(o.state ?? '')] ?? '';
  if (r.kind === 'channel') return (r.spec as { type?: string }).type ?? '';
  if (r.kind === 'dashboard') return `${(r.spec as { widgets?: unknown[] }).widgets?.length ?? 0} widget(s)`;
  if (r.kind === 'logexport') return o.linesSent !== undefined ? `${String(o.linesSent)} lines sent` : '';
  if (r.kind === 'secretstore') return (r.spec as { provider?: string }).provider ?? '';
  if (r.kind === 'stack') return o.total !== undefined ? `${String(o.ready)} of ${String(o.total)} ready` : '';
  if (r.kind === 'tunnel') {
    // The way in and what it gives: an app link's address, or how many connections a domain tunnel holds (CON-76).
    const t = r.spec as TunnelSpec;
    const where = typeof o.url === 'string' && o.url ? o.url.replace(/^https:\/\//, '') : t.app ? `→ ${t.app}` : o.publicAddress ? String(o.publicAddress) : '';
    return [WAY_TITLE[t.provider], o.connection === 'reconnecting' ? 'reconnecting' : where].filter(Boolean).join(' · ');
  }
  if (r.kind === 'domain' && o.public === true) return `public via ${String(o.tunnel ?? '')}`;
  if (r.kind === 'snapshot') return `${String(o.type ?? '')}${o.sizeBytes !== undefined ? ` · ${formatBytes(Number(o.sizeBytes))}` : ''}`;
  if (o.host && o.port) return `${String(o.host)}:${String(o.port)}`;
  if (o.internalHost) return String(o.internalHost);
  return '';
}

export function SectionPage() {
  const api = useApi();
  const toast = useToast();
  const navigate = useNavigate();
  const { section: slug, id } = useParams();
  const section = sectionBySlug(slug);
  const [query, setQuery] = useState('');
  const [params, setParams] = useSearchParams();
  // `?new=1` opens the create form: the overview's quick actions and the top bar's New link here.
  const [opened, setOpened] = useState(false);
  const creating = opened || params.get('new') === '1';
  const setCreating = (open: boolean) => {
    setOpened(open);
    if (!open && (params.has('new') || params.has('provider'))) {
      params.delete('new');
      params.delete('provider');
      setParams(params, { replace: true });
    }
  };
  const searchRef = useRef<HTMLInputElement>(null);
  const kind = section?.kind ?? 'app';
  const { project, createIn, allowed } = useProject();
  const list = useLoad(`list:${kind}:${project}`, () => api.listResources(kind, project || undefined));
  const canCreate = allowed('editor');
  // VMs need QEMU on this machine; without it the section says what to install (VM-11, CON-36).
  const machine = useLoad(`machine:${kind}`, () => (kind === 'vm' ? api.getMachine() : Promise.resolve(null)));
  const noVMs = kind === 'vm' && machine.data && !machine.data.vms.available ? machine.data.vms.message : '';
  const fix = noVMs.match(/(?:Install QEMU|Upgrade QEMU|Install it|Run): (.+)$/)?.[1];
  const anyBusy = (list.data ?? []).some((r) => inProgress(r.status.phase));
  // With more than one machine, each card says where it runs (CON-56).
  const machines = useLoad('section-machines', () => api.listMachines());
  const manyMachines = (machines.data?.length ?? 0) > 1;
  // Follow progress every second; otherwise refresh quietly every 5 s so resources made from the CLI or
  // OpenTofu show up without a reload (ADR-003: one API, three surfaces).
  usePolling(true, anyBusy ? 1000 : 5000, list.reload);

  // "/" focuses search unless the operator is typing (CON-05).
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      const t = e.target as HTMLElement | null;
      const typing = t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable);
      if (e.key === '/' && !typing && !e.metaKey && !e.ctrlKey) {
        e.preventDefault();
        searchRef.current?.focus();
      }
    }
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  const visible = useMemo(() => filterResources(list.data ?? [], query), [list.data, query]);
  // reload functions are stable; depending on them (not on the load objects) keeps these callbacks
  // stable, so effects that receive them don't re-fire on every render.
  const refresh = list.reload;
  const onDeleted = useCallback(() => {
    refresh();
    navigate(`/${slug ?? 'apps'}`, { replace: true });
  }, [navigate, refresh, slug]);

  if (!section) return <Navigate to="/apps" replace />;

  const listTo = `/${section.slug}${project ? `?project=${encodeURIComponent(project)}` : ''}`;
  const back = <Link to={listTo} className={buttonClass({ variant: 'ghost', size: 'sm' })}><Icons.ChevronLeft /> All {inSentence(section.label)}</Link>;
  const live = (list.data ?? []).filter((r) => r.status.phase !== 'deleting');
  const total = live.length;
  const counts = {
    ready: live.filter((r) => r.status.phase === 'ready').length,
    busy: live.filter((r) => inProgress(r.status.phase)).length,
    failed: live.filter((r) => r.status.phase === 'failed').length,
  };
  // Records are part of their zone's guide (CON-67).
  const guideTo = `/guide/${section.slug === 'records' ? 'zones' : section.slug}`;
  const guideLink = <Link to={guideTo} className={buttonClass({ variant: 'outline' })}><Icons.ScrollText /> Guide</Link>;
  // Viewers see no create button (CON-48); the tooltip says who can.
  // Stacks are installed from the catalogue (CON-59).
  const newButton = canCreate
    ? <Button variant="dark" icon={<Icons.Plus />} onClick={() => (kind === 'stack' ? navigate('/catalogue') : setCreating(true))}>New {section.noun}</Button>
    : <Text size="xs" tone="subtle" title={needRole('editor', createIn)}>View only</Text>;
  const createDrawer = creating && (
    <Drawer title={`New ${section.noun}`} onClose={() => setCreating(false)}>
      <ResourceForm
        kind={section.kind}
        // `?provider=` sets a new tunnel's way in: Install on a catalogue Public access card (CON-80).
        initialProvider={kind === 'tunnel' ? (RULES.tunnelProviders as readonly string[]).find((p) => p === params.get('provider')) as TunnelProvider | undefined : undefined}
        onDone={(r) => {
          setCreating(false);
          toast(`Creating ${r.kind} ${r.name}`);
          refresh();
          navigate(`/${section.slug}/${encodeURIComponent(r.id)}`);
        }}
      />
    </Drawer>
  );

  // Monitoring's machine dashboard is a page of its own (MET-05).
  if (kind === 'dashboard' && id === 'machine') {
    return (
      <Page>
        <PageHeader label="Summary" back={back} title="This machine"
          lede="How much of this machine EnclaveStack uses, the disk EnclaveStack keeps its data on, and how many resources have failed." />
        <MachineDashboard />
      </Page>
    );
  }

  // A resource opens on a page of its own (CON-64).
  if (id) {
    return (
      <Page>
        <Stack as="section" gap={5} aria-label="Details">
          <ResourceDetailView key={id} id={id} back={back} onChanged={refresh} onDeleted={onDeleted} />
        </Stack>
      </Page>
    );
  }

  return (
    <Page>
      <PageHeader label={section.label} title={section.label} status={list.data && <Badge tone="soft">{total}</Badge>} lede={section.blurb}
        actions={<>{guideLink}{newButton}</>} />
      {list.data && total > 0 && (
        <StatRow label={`${section.label} by state`}>
          <StatTile label="Total" value={total} />
          <StatTile label="Running" value={counts.ready} tone="ok" />
          {counts.busy > 0 && <StatTile label="In progress" value={counts.busy} />}
          {counts.failed > 0 && <StatTile label="Need attention" value={counts.failed} tone="bad" />}
        </StatRow>
      )}
      <Stack as="section" gap={3} aria-label={`${section.label} list`}>
        <VisuallyHidden><label htmlFor="search">Search {inSentence(section.label)}</label></VisuallyHidden>
        <Input id="search" ref={searchRef} sunk type="search" placeholder="Search…  ( / )" value={query} onChange={(e) => setQuery(e.target.value)} />
        {noVMs && (
          <Alert tone="warn">
            <Text size="sm" weight="semibold">VMs unavailable</Text>
            <Text size="sm">{noVMs}</Text>
            {fix && <CodeBlock label="Fix" code={fix} />}
          </Alert>
        )}
        {list.loading && !list.data && [0, 1, 2].map((i) => <Skeleton key={i} shape="card" />)}
        {list.error && (
          <Alert>
            <Inline justify="between">
              <span>{list.error.message}</span>
              <Button size="sm" onClick={list.reload}>Retry</Button>
            </Inline>
          </Alert>
        )}
        {list.data && (visible.length > 0 || kind === 'dashboard') && (
          <Table aria-label={`${section.label}`}>
            <thead>
              <tr>
                <th scope="col">Name</th>
                <th scope="col">State</th>
                <th scope="col">Details</th>
                {!project && <th scope="col">Project</th>}
                {manyMachines && <th scope="col">Machine</th>}
                <th scope="col">Changed</th>
              </tr>
            </thead>
            <tbody>
              {kind === 'dashboard' && (
                <tr>
                  <td><Link to={`/${section.slug}/machine`}>This machine</Link></td>
                  <td />
                  <td><Text size="sm" tone="subtle">disk, memory, CPU and failures</Text></td>
                  {!project && <td />}
                  {manyMachines && <td />}
                  <td />
                </tr>
              )}
              {visible.map((r) => (
                <tr key={r.id}>
                  <td><Link to={`/${section.slug}/${encodeURIComponent(r.id)}`}><Text weight="semibold">{r.name}</Text></Link></td>
                  <td><StatusBadge phase={r.status.phase} /></td>
                  <td><Text size="sm" mono truncate>{keyFact(r)}</Text></td>
                  {!project && <td><Text size="sm" tone="subtle" mono>{r.project}</Text></td>}
                  {manyMachines && <td><Text size="sm" tone="subtle" mono>{r.outputs.machine === undefined ? '' : String(r.outputs.machine)}</Text></td>}
                  <td><Text size="sm" tone="subtle">{formatAgo(r.updatedAt)}</Text></td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
        {list.data && visible.length === 0 && kind !== 'dashboard' && (
          query ? (
            <EmptyState>Nothing matches “{query}”.</EmptyState>
          ) : (
            <EmptyState icon={<SectionIcon kind={section.kind} />} title={`No ${inSentence(section.label)} yet.`} action={<Inline gap={2} wrap>{newButton}<Link to={guideTo} className={buttonClass({ variant: 'outline' })}>Read the guide</Link></Inline>}>
              <Text size="sm" tone="subtle">{section.blurb}</Text>
            </EmptyState>
          )
        )}
      </Stack>
      {createDrawer}
    </Page>
  );
}
