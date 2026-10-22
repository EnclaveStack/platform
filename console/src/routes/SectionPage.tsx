import {
  Alert, Avatar, Badge, Button, CodeBlock, Drawer, EmptyState, Heading, Icons, Inline, Input, List, ListCardContent, listCardClass, ListHead, Panel,
  Skeleton, Stack, Text, useToast, VisuallyHidden, Workspace,
} from '@enclavestack/ui';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link, Navigate, useNavigate, useParams } from 'react-router';
import { useApi } from '../api/context';
import { MachineDashboard } from '../components/MetricsPanel';
import { SectionIcon } from '../components/SectionIcon';
import { inProgress, PhaseDot } from '../components/StatusBadge';
import { filterResources } from '../lib/filter';
import { formatBytes } from '../lib/format';
import { useProject } from '../lib/project';
import { needRole } from '../lib/roles';
import { sectionBySlug } from '../lib/sections';
import { useLoad } from '../lib/useLoad';
import { usePolling } from '../lib/usePolling';
import { ResourceDetailView } from './ResourceDetailView';
import { ResourceForm } from './ResourceForm';
import type { Resource } from '../api/types';

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
  const [creating, setCreating] = useState(false);
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

  // Default to the first resource that isn't on its way out.
  // Monitoring opens on the machine's own dashboard (MET-05).
  const machineView = kind === 'dashboard' && (id === 'machine' || !id);
  const selectedId = machineView ? undefined : (id ?? list.data?.find((r) => r.status.phase !== 'deleting')?.id);
  const total = (list.data ?? []).filter((r) => r.status.phase !== 'deleting').length;
  // Viewers see no create button (CON-48); the tooltip says who can.
  const newButton = canCreate
    ? <Button variant="dark" icon={<Icons.Plus />} onClick={() => setCreating(true)}>New {section.noun}</Button>
    : <Text size="xs" tone="subtle" title={needRole('editor', createIn)}>View only</Text>;

  return (
    <Workspace hasSelection={Boolean(id)}>
      <Panel layout="list" aria-label={`${section.label} list`}>
        <ListHead>
          <Inline justify="between">
            <Inline gap={2}>
              <Heading level={2}>{section.label}</Heading>
              {list.data && <Badge tone="soft">{total}</Badge>}
            </Inline>
            {newButton}
          </Inline>
          <VisuallyHidden><label htmlFor="search">Search {section.label.toLowerCase()}</label></VisuallyHidden>
          <Input id="search" ref={searchRef} sunk type="search" placeholder="Search…  ( / )" value={query} onChange={(e) => setQuery(e.target.value)} />
          {noVMs && (
            <Alert tone="warn">
              <Text size="sm" weight="semibold">VMs unavailable</Text>
              <Text size="sm">{noVMs}</Text>
              {fix && <CodeBlock label="Fix" code={fix} />}
            </Alert>
          )}
        </ListHead>
        <List>
          {kind === 'dashboard' && (
            <Link to={`/${section.slug}/machine`} className={listCardClass} aria-current={machineView ? 'true' : undefined}>
              <ListCardContent avatar={<Avatar name="machine"><Icons.Server /></Avatar>} name="This machine" meta={<span>disk, memory, CPU and failures</span>} />
            </Link>
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
          {visible.map((r) => {
            const fact = keyFact(r);
            return (
              <Link key={r.id} to={`/${section.slug}/${encodeURIComponent(r.id)}`} className={listCardClass} aria-current={r.id === selectedId ? 'true' : undefined}>
                <ListCardContent
                  avatar={<Avatar name={r.name} />}
                  name={r.name}
                  meta={
                    <>
                      <PhaseDot phase={r.status.phase} />
                      <span>{r.status.phase === 'ready' ? section.noun : r.status.phase}</span>
                      {fact && <Text mono truncate>{fact}</Text>}
                      {!project && <Badge tone="soft" mono>{r.project}</Badge>}
                    </>
                  }
                />
              </Link>
            );
          })}
          {list.data && visible.length === 0 && (
            query ? (
              <EmptyState>Nothing matches “{query}”.</EmptyState>
            ) : (
              <EmptyState icon={<SectionIcon kind={section.kind} />} title={`No ${section.label.toLowerCase()} yet.`} action={newButton}>
                <Text size="sm" tone="subtle">{section.blurb}</Text>
              </EmptyState>
            )
          )}
        </List>
      </Panel>
      <Panel layout="detail" aria-label="Details">
        {machineView ? (
          <Stack gap={4}>
            <Heading level={2}>This machine</Heading>
            <Text tone="muted">How much of this machine EnclaveStack uses, the disk EnclaveStack keeps its data on, and how many resources have failed.</Text>
            <MachineDashboard />
          </Stack>
        ) : selectedId ? (
          <ResourceDetailView key={selectedId} id={selectedId} onChanged={refresh} onDeleted={onDeleted} />
        ) : (
          !list.loading && <EmptyState full icon={<SectionIcon kind={section.kind} />} title="Nothing selected">Select a resource to see its details.</EmptyState>
        )}
      </Panel>
      {creating && (
        <Drawer title={`New ${section.noun}`} onClose={() => setCreating(false)}>
          <ResourceForm
            kind={section.kind}
            onDone={(r) => {
              setCreating(false);
              toast(`Creating ${r.kind} ${r.name}`);
              refresh();
              navigate(`/${section.slug}/${encodeURIComponent(r.id)}`);
            }}
          />
        </Drawer>
      )}
    </Workspace>
  );
}
