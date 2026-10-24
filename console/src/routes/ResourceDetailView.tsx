import {
  Alert, Button, buttonClass, Card, Chip, Choice, Code, CodeBlock, ConfirmDialog, DetailBody, DetailList, DetailProgress, Disclosure, Drawer, EmptyState,
  Field, Fieldset, Form, FormRow, Icons, Inline, Input, PageHeader, Row, Rows, SegmentedTabs, Skeleton, Stack, StageTrack, Table, TabPanel,
  Terminal, TerminalLine, TerminalText, Text, useToast, VisuallyHidden,
} from '@enclavestack/ui';
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { Link, useNavigate } from 'react-router';
import { useApi } from '../api/context';
import { MANAGED } from '../api/generated';
import { DETAIL_HELP, isCopyable, isMono, isSecretLabel, OUTPUT_LABELS, withHelp } from '../lib/details';
import { ApiError, type AppSpec, type BackupSpec, type BucketSpec, type CacheSpec, type CDNSpec, type DatabaseSpec, type EmailSpec, type QueueSpec, type SearchSpec, type TopicSpec, type TranscoderSpec, type DomainSpec, type FirewallSpec, type JobSpec, type Kind, type LoadBalancerSpec, type NetworkSpec, type RecordSpec, type Resource, type Run, type SnapshotSpec, type VMSpec, type VolumeSpec, type ZoneSpec, type GateMode, type TunnelSpec } from '../api/types';
import { BucketAccessCard } from '../components/AccessFields';
import { GATE_TITLE, gateOf, WAY_TITLE } from '../components/PublicAccessFields';
import { connectionChip, MakePublic, PublicAddressesCard, PublicDomainCard, TunnelCards } from '../components/PublicAccessPanels';
import { TranscodeOutputsCards } from '../components/TranscodeFields';
import { BucketObjects } from '../components/BucketObjects';
import { IMAGE_TITLES } from '../components/ComputeFields';
import { ConnectCard, EmailMessages, QueueMessages, TopicDeliveries, TranscodeDialog } from '../components/DataPanels';
import { DashboardCharts, ResourceMetrics } from '../components/MetricsPanel';
import { AlertSummary, ChannelDeliveries, Incidents, LogExportCard, SyncedSecretCard, TestButton } from '../components/OperatePanels';
import { ruleSentence } from '../components/NetworkingFields';
import { inProgress, StatusBadge } from '../components/StatusBadge';
import { isPlaced, MoveButton, OfflineBanner } from '../components/MachineFields';
import { StackForm, StackMembers } from '../components/StackFields';
import { cliCommand, tofuBlock } from '../lib/codegen';
import { formatAgo, formatBytes } from '../lib/format';
import { describeSchedule } from '../lib/schedule';
import { describeHealthCheck } from '../lib/validation';
import { RULES } from '../api/generated';
import { metricsOf } from '../lib/operate';
import { useProject } from '../lib/project';
import { needRole } from '../lib/roles';
import { sectionByKind } from '../lib/sections';
import { usePageTitle } from '../lib/shell';
import { useLoad } from '../lib/useLoad';
import { usePolling } from '../lib/usePolling';
import { ResourceForm } from './ResourceForm';

type Tab = 'overview' | 'objects' | 'metrics' | 'logs' | 'build' | 'env' | 'runs' | 'backups' | 'records' | 'messages' | 'deliveries' | 'emails' | 'incidents' | 'notifications' | 'events';

/** A drawer that creates a related resource: a snapshot of this one, or a volume or VM from this snapshot. */
type Spawn = { kind: Kind; title: string; snapshot?: string; source?: string };
const POLL_MS = 1000;
const REVEAL_MS = 30_000;

const RUN_LABEL: Record<string, string> = { running: 'Running', succeeded: 'Succeeded', failed: 'Failed', timed_out: 'Timed out', skipped: 'Skipped' };
const RUN_TONE: Record<string, 'ok' | 'warn' | 'bad' | 'neutral' | 'accent'> = { running: 'accent', succeeded: 'ok', failed: 'bad', timed_out: 'bad', skipped: 'neutral' };
const TRIGGER_LABEL: Record<string, string> = { schedule: 'Scheduled', manual: 'Manual', catch_up: 'Catch-up', pre_restore: 'Before restore', watch: 'New file' };

const CERT_LABEL: Record<string, string> = { issued: 'Issued', pending: 'Waiting', failed: 'Failed', none: 'None (HTTP only)' };
const TLS_LABEL: Record<string, string> = { auto: "Let's Encrypt", internal: 'EnclaveStack certificate', off: 'None (HTTP only)' };

function outputText(k: string, v: unknown): string {
  if (k === 'lastDeploy') return DEPLOY_LABEL[String(v)] ?? String(v);
  if (k === 'certificateState') return CERT_LABEL[String(v)] ?? String(v);
  if (k === 'nextRunAt' || k === 'lastRunAt' || k === 'lastSuccessAt') return new Date(String(v)).toLocaleString(undefined, { weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' });
  if (k === 'lastRunStatus') return RUN_LABEL[String(v)] ?? String(v);
  if (k === 'builtCommit') return String(v).slice(0, 12);
  if (k === 'lastBuild') return BUILD_LABEL[String(v)] ?? String(v);
  if (k === 'certificateExpiresAt') return new Date(String(v)).toLocaleDateString(undefined, { day: 'numeric', month: 'short', year: 'numeric' });
  if (k === 'appliedAt') return new Date(String(v)).toLocaleString();
  if (k === 'nameServers' || k === 'values') return String(v).split(',').join(', ');
  if (k === 'power') return v === 'running' ? 'Running' : v === 'stopped' ? 'Stopped' : String(v);
  if (k === 'usedBytes' || k === 'sizeBytes') return formatBytes(Number(v));
  if (k === 'sizeGiB') return `${String(v)} GiB`;
  if (k === 'takenAt') return new Date(String(v)).toLocaleString();
  if (k === 'inUse' || k === 'funnel' || k === 'public') return v ? 'Yes' : 'No';
  if (k === 'connection' || k === 'tunnelState') return v === 'connected' ? 'Connected' : v === 'reconnecting' ? 'Reconnecting' : String(v);
  if (k === 'gate') return GATE_TITLE[v as GateMode] ?? String(v);
  if (k === 'attachedTo') return String(v) ? String(v).split(',').join(', ') : 'Nothing';
  if (k === 'cachedBytes' || k === 'bytesSent') return formatBytes(Number(v));
  if (k === 'state') return ({ ok: 'OK', firing: 'Firing', no_data: 'No data', disabled: 'Off' } as Record<string, string>)[String(v)] ?? String(v);
  if (k === 'since' || k === 'lastEvaluatedAt' || k === 'lastDeliveryAt' || k === 'lastSentAt' || k === 'checkedAt' || k === 'syncedAt') return v ? new Date(String(v)).toLocaleString() : 'Never';
  if (k === 'lagSeconds') return `${String(v)} s`;
  if (k === 'lastError') return String(v) || 'None';
  if (k === 'watching') return v ? 'Yes: new files are converted' : 'No: only files asked for';
  if (k === 'mode') return v === 'relay' ? 'Relay through the provider' : 'Catch every message';
  return String(v);
}

/** Outputs that are bookkeeping, not facts for people. */
const HIDDEN_OUTPUTS = new Set(['latestRevision', 'pendingRestore', 'builtInputs', 'appliedTargets', 'backends', 'owned', 'linkPort', 'webPort', 'dnsRecord', 'dnsRecordOwned', 'dnsZone']);

const BUILD_LABEL: Record<string, string> = { succeeded: 'Succeeded', failed: 'Failed', skipped: 'Skipped — nothing changed' };
const DEPLOY_LABEL: Record<string, string> = { succeeded: 'Succeeded', failed: 'Failed — previous revision kept', in_progress: 'In progress' };

interface Props {
  id: string;
  /** The way back to the resource's section, shown above its name (CON-63). */
  back: ReactNode;
  onChanged: () => void;
  onDeleted: () => void;
}

export function ResourceDetailView({ id, back, onChanged, onDeleted }: Props) {
  const api = useApi();
  const toast = useToast();
  const [tab, setTab] = useState<Tab>('overview');
  const [editing, setEditing] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [deleteError, setDeleteError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [addingDomain, setAddingDomain] = useState(false);
  const [makingPublic, setMakingPublic] = useState(false);
  const [addingBackup, setAddingBackup] = useState(false);
  const [addingRecord, setAddingRecord] = useState(false);
  const [spawning, setSpawning] = useState<Spawn | null>(null);
  const [confirmPurge, setConfirmPurge] = useState(false);
  const [transcoding, setTranscoding] = useState(false);
  const navigate = useNavigate();
  const detail = useLoad(`detail:${id}`, () => api.getResource(id));
  const r = detail.data;
  const { allowed } = useProject();
  const busyPhase = r ? inProgress(r.status.phase) : false;
  usePageTitle(r?.name);

  // Poll while anything is in progress, then refresh the list once it settles (L2.5-002 §4).
  usePolling(busyPhase, POLL_MS, detail.reload);
  const wasBusy = useRef(false);
  useEffect(() => {
    if (wasBusy.current && !busyPhase) onChanged();
    wasBusy.current = busyPhase;
  }, [busyPhase, onChanged]);

  // Once gone, tell the page exactly once (it navigates away and refreshes the list).
  const reportedGone = useRef(false);
  useEffect(() => {
    if (!reportedGone.current && detail.error instanceof ApiError && detail.error.code === 'NOT_FOUND') {
      reportedGone.current = true;
      onDeleted();
    }
  }, [detail.error, onDeleted]);

  const reloadDetail = detail.reload;
  const run = useCallback(
    async (label: string, fn: () => Promise<unknown>) => {
      setBusy(true);
      try {
        await fn();
        toast(label);
        reloadDetail();
        onChanged();
      } catch (e) {
        toast(e instanceof ApiError ? e.message : 'That did not work. Try again.');
      } finally {
        setBusy(false);
      }
    },
    [toast, reloadDetail, onChanged],
  );

  if (detail.error && !(detail.error instanceof ApiError && detail.error.code === 'NOT_FOUND')) {
    return (
      <DetailBody>
        <Alert>
          <Inline justify="between">
            <span>{detail.error.message}</span>
            <Button size="sm" onClick={detail.reload}>Retry</Button>
          </Inline>
        </Alert>
      </DetailBody>
    );
  }
  if (!r) {
    return (
      <DetailBody>
        <Stack aria-busy="true">
          <Skeleton shape="short" />
          <Skeleton shape="title" />
          <Skeleton shape="block" />
        </Stack>
      </DetailBody>
    );
  }

  const section = sectionByKind(r.kind);
  // A stack's member is changed through its stack (CAT-06): no edit, delete or move.
  const managed = r.ownerId !== null || r.stack !== null;
  const canRestart = ['app', 'database', 'cache', 'search', 'email', 'cdn'].includes(r.kind) && r.status.phase === 'ready' || (r.kind === 'vm' && r.status.phase === 'ready' && r.outputs.power === 'running');
  const canRedeploy = r.kind === 'app' && r.status.phase === 'ready';
  const hasLogs = ['app', 'database', 'cache', 'vm', 'search', 'email', 'cdn'].includes(r.kind);
  // Viewers read; editors change (CON-48). The server refuses anyway; this keeps buttons honest.
  const canEdit = allowed('editor', r);
  const viewOnly = needRole('editor', r.project);
  const tabs: { id: Tab; label: string }[] = [
    { id: 'overview', label: r.kind === 'dashboard' ? 'Graphs' : 'Overview' },
    ...(r.kind === 'bucket' && r.status.phase === 'ready' ? [{ id: 'objects' as const, label: 'Objects' }] : []),
    ...(metricsOf(r.kind).length ? [{ id: 'metrics' as const, label: 'Metrics' }] : []),
    ...(hasLogs ? [{ id: 'logs' as const, label: r.kind === 'vm' ? 'Boot console' : 'Logs' }] : []),
    ...(r.kind === 'app' && (r.spec as AppSpec).source ? [{ id: 'build' as const, label: 'Build log' }] : []),
    ...(r.kind === 'app' ? [{ id: 'env' as const, label: 'Environment' }] : []),
    ...(r.kind === 'job' || r.kind === 'backup' || r.kind === 'transcoder' ? [{ id: 'runs' as const, label: 'Runs' }] : []),
    ...(r.kind === 'queue' && r.status.phase === 'ready' ? [{ id: 'messages' as const, label: 'Messages' }] : []),
    ...(r.kind === 'topic' && r.status.phase === 'ready' ? [{ id: 'deliveries' as const, label: 'Deliveries' }] : []),
    ...(r.kind === 'email' && r.status.phase === 'ready' ? [{ id: 'emails' as const, label: 'Messages' }] : []),
    ...(r.kind === 'database' || r.kind === 'cache' ? [{ id: 'backups' as const, label: 'Backups' }] : []),
    ...(r.kind === 'zone' ? [{ id: 'records' as const, label: 'Records' }] : []),
    ...(r.kind === 'alert' ? [{ id: 'incidents' as const, label: 'Incidents' }] : []),
    ...(r.kind === 'channel' ? [{ id: 'notifications' as const, label: 'Deliveries' }] : []),
    { id: 'events', label: 'Events' },
  ];
  const dataLoss = ['database', 'cache', 'bucket', 'volume', 'snapshot', 'queue', 'search', 'email'].includes(r.kind);
  const failed = r.status.phase === 'failed';

  async function doDelete() {
    if (!r) return;
    setBusy(true);
    setDeleteError(null);
    try {
      await api.deleteResource(r.id, r.generation);
      setConfirmDelete(false);
      toast(`Deleting ${r.kind} ${r.name}`);
      detail.reload();
      onChanged();
    } catch (e) {
      if (e instanceof ApiError) {
        setDeleteError(e.message + (e.details.length ? ' ' + e.details.map((d) => d.message).join(' ') : ''));
      } else {
        setDeleteError('That did not work. Try again.');
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <PageHeader
        label="Summary"
        back={back}
        title={r.name}
        status={<StatusBadge phase={r.status.phase} />}
        lede={`${section.noun[0]?.toUpperCase() ?? ''}${section.noun.slice(1)} in project ${r.project} · changed ${formatAgo(r.updatedAt)}`}
        meta={(r.ownerId !== null || r.stack || r.kind === 'tunnel' || (r.kind === 'domain' && (r.spec as DomainSpec).tunnel) || (typeof r.outputs.machine === 'string' && r.outputs.machine)) && (
          <>
            {connectionChip(r)}
            {r.kind === 'domain' && (r.spec as DomainSpec).tunnel && <Chip icon={<Icons.Cable />}>Public via {(r.spec as DomainSpec).tunnel}</Chip>}
            {r.ownerId !== null && <Chip>Managed by {r.ownerId === MANAGED.systemOwner ? 'EnclaveStack' : 'its database'}</Chip>}
            {r.stack && <StackChip id={r.stack} />}
            {typeof r.outputs.machine === 'string' && r.outputs.machine && (
              <Link to={`/machines/${encodeURIComponent(r.outputs.machine)}`}><Chip mono icon={<Icons.Server />}>{r.outputs.machine}</Chip></Link>
            )}
          </>
        )}
        actions={
          <>
          {!canEdit ? (
            <Inline gap={2} wrap>
              <Text size="sm" tone="subtle">View only</Text>
              {!managed && <Button size="sm" icon={<Icons.Pencil />} disabled title={viewOnly}>Edit</Button>}
              {!managed && <Button size="sm" variant="danger-outline" icon={<Icons.Trash2 />} disabled title={viewOnly}>Delete</Button>}
            </Inline>
          ) : (
          <Inline gap={2} wrap>
            {(r.kind === 'alert' || r.kind === 'channel' || r.kind === 'secretstore') && r.status.phase === 'ready' && <TestButton r={r} onDone={() => { detail.reload(); if (r.kind === 'channel') setTab('notifications'); }} />}
            {r.kind === 'secret' && (r.spec as { source?: unknown }).source !== undefined && r.status.phase !== 'deleting' && (
              <Button size="sm" icon={<Icons.RefreshCw />} disabled={busy} onClick={() => run(`Reading ${r.name} again`, () => api.action(r.id, 'refresh'))}>Refresh now</Button>
            )}
            {canRestart && <Button size="sm" icon={<Icons.RotateCw />} disabled={busy} onClick={() => run(`Restart requested for ${r.name}`, () => api.action(r.id, 'restart'))}>Restart</Button>}
            {(r.kind === 'job' || r.kind === 'backup') && r.status.phase === 'ready' && (
              <Button size="sm" icon={<Icons.Play />} disabled={busy} onClick={() => run(r.kind === 'backup' ? `Backup ${r.name} started` : `Run of ${r.name} started`, async () => {
                await api.runNow(r.id);
                setTab('runs');
              })}>{r.kind === 'backup' ? 'Back up now' : 'Run now'}</Button>
            )}
            {(r.kind === 'queue' || r.kind === 'cdn') && r.status.phase === 'ready' && (
              <Button size="sm" icon={<Icons.Eraser />} disabled={busy} onClick={() => setConfirmPurge(true)}>Purge</Button>
            )}
            {r.kind === 'transcoder' && r.status.phase === 'ready' && (
              <Button size="sm" icon={<Icons.Play />} disabled={busy} onClick={() => setTranscoding(true)}>Transcode a file</Button>
            )}
            {canRedeploy && <Button size="sm" icon={<Icons.RefreshCw />} disabled={busy} onClick={() => run(`Redeploying ${r.name}`, () => api.action(r.id, 'redeploy'))}>Redeploy</Button>}
            {r.kind === 'vm' && r.status.phase !== 'deleting' && ((r.spec as VMSpec).power === 'stopped'
              ? <Button size="sm" icon={<Icons.Play />} disabled={busy} onClick={() => run(`Starting ${r.name}`, () => api.action(r.id, 'start'))}>Start</Button>
              : <Button size="sm" icon={<Icons.Power />} disabled={busy} onClick={() => run(`Stopping ${r.name}`, () => api.action(r.id, 'stop'))}>Stop</Button>)}
            {(r.kind === 'volume' || r.kind === 'vm') && r.status.phase === 'ready' && (
              <Button size="sm" icon={<Icons.Camera />} disabled={busy} onClick={() => setSpawning({ kind: 'snapshot', title: 'Take snapshot', source: `${r.kind}/${r.name}` })}>Take snapshot</Button>
            )}
            {r.kind === 'snapshot' && r.status.phase === 'ready' && (
              <>
                <Button size="sm" icon={<Icons.HardDrive />} disabled={busy} onClick={() => setSpawning({ kind: 'volume', title: 'New volume from snapshot', snapshot: r.name })}>Create volume</Button>
                {r.outputs.type === 'disk' && <Button size="sm" icon={<Icons.Server />} disabled={busy} onClick={() => setSpawning({ kind: 'vm', title: 'Launch VM from snapshot', snapshot: r.name })}>Launch VM</Button>}
              </>
            )}
            {r.kind === 'app' && r.status.phase !== 'deleting' && !r.stack && <Button size="sm" icon={<Icons.Cable />} disabled={busy || !(r.spec as AppSpec).port} title={(r.spec as AppSpec).port ? undefined : 'Give the app a port first.'} onClick={() => setMakingPublic(true)}>Make public</Button>}
            {r.kind === 'app' && r.status.phase !== 'deleting' && !r.stack && <Button size="sm" icon={<Icons.Globe />} disabled={busy} onClick={() => setAddingDomain(true)}>Add domain</Button>}
            {isPlaced(r.kind) && !managed && r.status.phase !== 'deleting' && !(r.kind === 'job' && (r.spec as JobSpec).app) && (
              <MoveButton r={r} disabled={busy} onMoved={() => { toast(`Moving ${r.name}`); detail.reload(); onChanged(); }} />
            )}
            {!managed && r.status.phase !== 'deleting' && <Button size="sm" icon={<Icons.Pencil />} disabled={busy} onClick={() => setEditing(true)}>{r.kind === 'stack' ? 'Edit parameters' : 'Edit'}</Button>}
            {!managed && r.status.phase !== 'deleting' && <Button size="sm" variant="danger-outline" icon={<Icons.Trash2 />} disabled={busy} onClick={() => setConfirmDelete(true)}>Delete</Button>}
          </Inline>
          )}
          </>
        }
      />
      {(r.status.steps.length > 0 || failed) && (
        <DetailProgress>
          {r.status.steps.length > 0 && (
            <StageTrack label="Progress" stages={r.status.steps.map((s) => ({ label: (s.name[0] ?? '').toUpperCase() + s.name.slice(1), state: s.state }))} />
          )}
          {failed && (
            <Button variant="dark" icon={<Icons.RotateCw />} disabled={busy} onClick={() => run(`Retrying ${r.name}`, () => api.action(r.id, 'reconcile'))}>Retry</Button>
          )}
        </DetailProgress>
      )}

      {isPlaced(r.kind) && <OfflineBanner r={r} />}
      {r.status.message && (
        <Alert tone={failed ? 'bad' : 'info'} role={failed ? 'alert' : undefined}>{r.status.message}</Alert>
      )}

      <Stack gap={5}>
        <div>
          <SegmentedTabs label="Details" tabs={tabs} selected={tab} onSelect={setTab} />
        </div>
        <TabPanel id={tab}>
          {tab === 'overview' && (r.kind === 'dashboard' ? <DashboardCharts dashboard={r as Resource<'dashboard'>} /> : r.kind === 'stack' ? <StackOverview stack={r as Resource<'stack'>} /> : <Overview r={r} canReveal={canEdit} />)}
          {tab === 'objects' && <BucketObjects bucket={r} canEdit={canEdit} />}
          {tab === 'metrics' && <ResourceMetrics resource={r} />}
          {tab === 'incidents' && <Incidents alert={r} />}
          {tab === 'notifications' && <ChannelDeliveries channel={r} version={String(r.outputs.lastDeliveryAt ?? '')} />}
          {tab === 'logs' && <Logs id={r.id} />}
          {tab === 'build' && <Logs id={r.id} build version={`${r.generation}:${String(r.outputs.builtCommit ?? '')}:${r.status.phase}`} />}
          {tab === 'env' && <Env spec={r.spec as AppSpec} />}
          {tab === 'runs' && (r.kind === 'backup' ? <BackupRuns backup={r} /> : <Runs id={r.id} version={String(r.outputs.lastRunAt ?? '')} />)}
          {tab === 'backups' && <BackupsOf r={r} onAdd={() => setAddingBackup(true)} />}
          {tab === 'records' && <ZoneRecords zone={r} onAdd={() => setAddingRecord(true)} />}
          {tab === 'messages' && <QueueMessages queue={r} />}
          {tab === 'deliveries' && <TopicDeliveries topic={r} />}
          {tab === 'emails' && <EmailMessages email={r} />}
          {tab === 'events' && <Events id={r.id} version={`${r.generation}:${r.updatedAt}:${r.status.phase}`} />}
        </TabPanel>
      </Stack>

      {editing && (
        <Drawer title={r.kind === 'stack' ? `Parameters of ${r.name}` : `Edit ${r.kind} ${r.name}`} onClose={() => setEditing(false)}>
          {r.kind === 'stack' ? <StackEdit stack={r as Resource<'stack'>} onDone={() => { setEditing(false); toast(`Updating stack ${r.name}`); detail.reload(); onChanged(); }} /> : <ResourceForm
            kind={r.kind}
            existing={r}
            onReload={() => {
              setEditing(false);
              detail.reload();
            }}
            onDone={(updated) => {
              setEditing(false);
              if (updated.generation !== r.generation) toast(`Updating ${r.kind} ${r.name}`);
              detail.reload();
              onChanged();
            }}
          />}
        </Drawer>
      )}
      {makingPublic && <MakePublic app={r} onClose={() => setMakingPublic(false)} />}
      {addingDomain && (
        <Drawer title="New domain" onClose={() => setAddingDomain(false)}>
          <ResourceForm
            kind="domain"
            initialApp={r.name}
            onDone={(created) => {
              setAddingDomain(false);
              toast(`Creating domain ${created.name}`);
              navigate(`/domains/${encodeURIComponent(created.id)}`);
            }}
          />
        </Drawer>
      )}
      {addingRecord && (
        <Drawer title="New record" onClose={() => setAddingRecord(false)}>
          <ResourceForm
            kind="record"
            initialZone={r.name}
            onDone={(created) => {
              setAddingRecord(false);
              toast(`Creating record ${created.name}`);
              navigate(`/records/${encodeURIComponent(created.id)}`);
            }}
          />
        </Drawer>
      )}
      {spawning && (
        <Drawer title={spawning.title} onClose={() => setSpawning(null)}>
          <ResourceForm
            kind={spawning.kind}
            initialSnapshot={spawning.snapshot}
            initialSource2={spawning.source}
            onDone={(created) => {
              setSpawning(null);
              toast(`Creating ${created.kind} ${created.name}`);
              navigate(`/${sectionByKind(created.kind).slug}/${encodeURIComponent(created.id)}`);
            }}
          />
        </Drawer>
      )}
      {addingBackup && (
        <Drawer title="New backup" onClose={() => setAddingBackup(false)}>
          <ResourceForm
            kind="backup"
            initialSource={{ kind: r.kind === 'cache' ? 'cache' : 'database', name: r.name }}
            onDone={(created) => {
              setAddingBackup(false);
              toast(`Creating backup ${created.name}`);
              navigate(`/backups/${encodeURIComponent(created.id)}`);
            }}
          />
        </Drawer>
      )}
      {confirmPurge && (
        <ConfirmDialog
          title={r.kind === 'queue' ? `Delete every message of ${r.name}?` : `Empty the cache of ${r.name}?`}
          danger={r.kind === 'queue'}
          busy={busy}
          confirmLabel="Purge"
          body={r.kind === 'queue'
            ? <p>Every message waiting or in flight in <strong>{r.name}</strong>{r.outputs.messages ? ` (about ${String(r.outputs.messages)})` : ''} is deleted. This cannot be undone.</p>
            : <p>The next requests to <strong>{(r.spec as CDNSpec).hostname}</strong> go to the origin again and fill the cache anew.</p>}
          onConfirm={async () => {
            await run(`Purging ${r.name}`, () => api.action(r.id, 'purge'));
            setConfirmPurge(false);
          }}
          onCancel={() => setConfirmPurge(false)}
        />
      )}
      {transcoding && (
        <TranscodeDialog transcoder={r} onClose={() => setTranscoding(false)} onDone={() => {
          setTranscoding(false);
          toast('Transcoding started');
          setTab('runs');
          detail.reload();
        }} />
      )}
      {confirmDelete && (
        <ConfirmDialog
          title={`Delete ${section.noun} ${r.name}?`}
          danger
          busy={busy}
          typeToConfirm={r.name}
          confirmLabel={`Delete ${section.noun}`}
          error={deleteError}
          body={r.kind === 'stack' ? <StackDeleteBody stack={r} />
            : r.kind === 'vm' ? <p>This deletes the VM <strong>{r.name}</strong> and its root disk. Its data disks are detached and kept. This cannot be undone.</p>
            : dataLoss ? <p>This deletes the {r.kind} <strong>{r.name}</strong> and all of its data. This cannot be undone.</p>
            : r.kind === 'backup' ? <p>This deletes the backup <strong>{r.name}</strong>: its schedule and run history. Backups already stored in the bucket are kept; delete them there if you no longer need them.</p>
              : <p>This deletes the {r.kind} <strong>{r.name}</strong>.</p>}
          onConfirm={doDelete}
          onCancel={() => {
            setConfirmDelete(false);
            setDeleteError(null);
          }}
        />
      )}
    </>
  );
}

/** "Part of stack <name>", linked (CON-60). */
function StackChip({ id }: { id: string }) {
  const api = useApi();
  const st = useLoad(`stack-of:${id}`, () => api.getResource(id));
  return <Link to={`/stacks/${encodeURIComponent(id)}`}><Chip tone="accent" icon={<Icons.Boxes />}>Part of stack {st.data?.name ?? '…'}</Chip></Link>;
}

/** What deleting a stack deletes, naming the members with data (CON-60). */
function StackDeleteBody({ stack }: { stack: Resource }) {
  const members = (stack.outputs.members ?? []) as unknown as { kind: Kind; name: string }[];
  const data = members.filter((m) => ['database', 'cache', 'bucket', 'volume', 'search', 'email', 'queue'].includes(m.kind));
  return (
    <Stack gap={2}>
      <p>This deletes the stack <strong>{stack.name}</strong> and its {members.length} members.</p>
      {data.length > 0 && <p>Their data goes with them: {data.map((m) => `${m.kind} ${m.name}`).join(', ')}. This cannot be undone.</p>}
    </Stack>
  );
}

/** A stack's template, parameters, outputs and members (CON-60). */
function StackOverview({ stack }: { stack: Resource<'stack'> }) {
  const api = useApi();
  const st = stack.spec;
  const entry = useLoad(`stack-entry:${st.template ?? ''}`, () => (st.template ? api.getCatalogueEntry(st.template) : Promise.resolve(null)));
  const outs = (stack.outputs.outputs ?? {}) as unknown as Record<string, string>;
  const version = String(stack.outputs.version ?? st.version ?? '');
  const newer = entry.data && entry.data.version !== version ? entry.data.version : '';
  return (
    <Stack gap={4}>
      <Card as="section" title="Details">
        <DetailList items={withHelp([
          { label: 'Template', value: st.template ?? 'Its own' },
          { label: 'Template version', mono: true, value: `${version}${newer ? ` (${newer} available)` : ''}` },
          { label: 'Members ready', value: `${String(stack.outputs.ready ?? 0)} of ${String(stack.outputs.total ?? 0)}` },
        ])} />
      </Card>
      {Object.keys(st.parameters ?? {}).length > 0 && (
        <Card as="section" title="Parameters">
          <DetailList label="Parameters" items={Object.entries(st.parameters ?? {}).map(([k, v]) => {
            const p = entry.data?.parameters.find((x) => x.name === k);
            return { label: p?.title ?? k, hint: p?.description, mono: true, value: String(v), copy: String(v) };
          })} />
        </Card>
      )}
      {Object.keys(outs).length > 0 && (
        <Card as="section" title="Outputs">
          <DetailList label="Outputs" items={Object.entries(outs).map(([k, v]) => ({ label: k, hint: DETAIL_HELP['Stack output'], mono: true, copy: v,
            value: /^https?:\/\//.test(v) ? <a href={v} target="_blank" rel="noreferrer">{v}</a> : v }))} />
        </Card>
      )}
      <Card title="Members"><StackMembers stack={stack} /></Card>
    </Stack>
  );
}

/** Edit parameters, or upgrade to the newest version (CON-60). */
function StackEdit({ stack, onDone }: { stack: Resource<'stack'>; onDone: () => void }) {
  const api = useApi();
  const toast = useToast();
  const name = stack.spec.template ?? '';
  const version = String(stack.outputs.version ?? stack.spec.version ?? '');
  const current = useLoad(`stack-edit:${name}:${version}`, () => (name ? api.getCatalogueEntry(name, version) : Promise.resolve(null)));
  const newest = useLoad(`stack-edit-newest:${name}`, () => (name ? api.getCatalogueEntry(name) : Promise.resolve(null)));
  if (!name) return <Text tone="muted">This stack uses a template of its own. Change it with the CLI or OpenTofu: enclave stack update {stack.name} --param name=value.</Text>;
  if (!current.data) return current.error ? <Alert>{current.error.message}</Alert> : <Skeleton shape="block" />;
  const upgrade = newest.data && newest.data.version !== version ? newest.data.version : '';
  return (
    <Stack gap={4}>
      {upgrade && (
        <Alert tone="info">
          <Inline justify="between" wrap>
            <Text size="sm">Version {upgrade} is available; this stack is on {version}.</Text>
            <Button size="sm" variant="dark" onClick={() => void api.updateResource(stack.id, { version: upgrade }, stack.generation).then(() => { toast(`Upgrading ${stack.name} to ${upgrade}`); onDone(); },
              (e: unknown) => toast(e instanceof ApiError ? e.message : 'That did not work. Try again.'))}>Upgrade</Button>
          </Inline>
        </Alert>
      )}
      <StackForm entry={current.data} existing={stack} onDone={onDone} />
    </Stack>
  );
}

/** DNS instructions, the root certificate and machine-wide domain settings (CON-19, CON-20). */
function DomainCards({ r }: { r: Resource }) {
  const d = r.spec as DomainSpec;
  const url = typeof r.outputs.url === 'string' ? r.outputs.url : '';
  const waiting = d.tls === 'auto' && r.outputs.certificateState !== 'issued';
  const target = typeof r.outputs.publicAddress === 'string' ? r.outputs.publicAddress : "this machine's public IP";
  return (
    <>
      {url && r.status.phase === 'ready' && (
        <Card padding="tight">
          <Inline justify="between" wrap>
            <Text tone="muted">The site is live.</Text>
            <a href={url} target="_blank" rel="noreferrer" className={buttonClass({ size: 'sm' })}><Icons.ExternalLink /> Open site</a>
          </Inline>
        </Card>
      )}
      {waiting && (
        <Card title="Point DNS here">
          <Stack>
            <Text as="p" tone="muted">Create this record at your DNS provider. The certificate is issued once it resolves and ports 80 and 443 reach this machine.</Text>
            <CodeBlock label="DNS record" code={`${d.hostname}.  A  ${target}`} />
          </Stack>
        </Card>
      )}
      {d.tls === 'internal' && <RootCACard />}
      <MachineSettingsCard />
    </>
  );
}

function RootCACard() {
  const api = useApi();
  const [error, setError] = useState<string | null>(null);
  async function download() {
    setError(null);
    try {
      const pem = await api.getRootCA();
      const link = document.createElement('a');
      link.href = URL.createObjectURL(new Blob([pem], { type: 'application/x-pem-file' }));
      link.download = 'enclave-root-ca.pem';
      link.click();
      URL.revokeObjectURL(link.href);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : 'That did not work. Try again.');
    }
  }
  return (
    <Card title="Trust this machine's certificates">
      <Stack>
        <Text as="p" tone="muted">
          This domain's certificate is signed by EnclaveStack's own certificate authority. Install its root certificate once on each device that
          visits, and browsers will trust every internal domain on this machine — and nothing else.
        </Text>
        <div>
          <Button icon={<Icons.Download />} onClick={() => void download()}>Download root certificate</Button>
        </div>
        {error && <Alert>{error}</Alert>}
        <CodeBlock label="macOS" code="security add-trusted-cert -r trustRoot -k ~/Library/Keychains/login.keychain-db enclave-root-ca.pem" />
        <CodeBlock label="Linux (Debian, Ubuntu)" code={'sudo cp enclave-root-ca.pem /usr/local/share/ca-certificates/enclave.crt\nsudo update-ca-certificates'} />
        <Text as="p" size="xs" tone="subtle">iOS and Android: open the file on the device, install the profile, then enable full trust for it in the certificate settings.</Text>
      </Stack>
    </Card>
  );
}

function MachineSettingsCard() {
  const api = useApi();
  const toast = useToast();
  const machine = useLoad('machine-settings', () => api.getMachine());
  const [publicAddress, setPublicAddress] = useState<string | null>(null);
  const [acmeEmail, setAcmeEmail] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const m = machine.data;
  if (!m) return null;
  const pa = publicAddress ?? m.publicAddress ?? '';
  const email = acmeEmail ?? m.acmeEmail ?? '';
  async function save() {
    setBusy(true);
    setError(null);
    try {
      await api.updateMachine({ publicAddress: pa.trim() || null, acmeEmail: email.trim() || null });
      toast('Domain settings saved');
      machine.reload();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : 'That did not work. Try again.');
    } finally {
      setBusy(false);
    }
  }
  return (
    <Card title="Settings for all domains">
      <Stack gap={4}>
        <FormRow>
          <Field id="ms-public" label="Public address" hint="The IP or hostname your DNS records point to. EnclaveStack never looks it up itself.">
            <Input id="ms-public" mono value={pa} onChange={(e) => setPublicAddress(e.target.value)} placeholder="203.0.113.7" />
          </Field>
          <Field id="ms-acme" label="Let's Encrypt email" hint={"Sent to Let's Encrypt for expiry notices. Empty: the admin's email."}>
            <Input id="ms-acme" type="email" value={email} onChange={(e) => setAcmeEmail(e.target.value)} placeholder="you@company.com" />
          </Field>
        </FormRow>
        {!m.edge.available && m.edge.message && <Alert tone="warn">{m.edge.message}</Alert>}
        {error && <Alert>{error}</Alert>}
        <div>
          <Button variant="dark" disabled={busy} onClick={() => void save()}>{busy ? 'Saving…' : 'Save settings'}</Button>
        </div>
      </Stack>
    </Card>
  );
}

/** Created and last-changed times, beside the title. */
function Overview({ r, canReveal }: { r: Resource; canReveal: boolean }) {
  const api = useApi();
  const secrets = useLoad(`detail-secrets:${r.project}:${r.updatedAt}`, () => api.listResources('secret', r.project));
  const secretIds = new Map((secrets.data ?? []).map((x) => [x.name, x.id]));
  const facts: [string, string][] = Object.entries(r.outputs)
    .filter(([k]) => !HIDDEN_OUTPUTS.has(k))
    .map(([k, v]) => [OUTPUT_LABELS[k] ?? k, outputText(k, v)]);
  switch (r.kind) {
    case 'app': {
      const s = r.spec as AppSpec;
      if (s.source) facts.unshift(['Source', `${s.source.git} @ ${s.source.ref}`]);
      else facts.unshift(['Image', s.image ?? '']);
      facts.push(['Network', s.network ? `${s.network}${s.subnet ? ` · ${s.subnet}` : ''}` : 'Default network'], ['Replicas', String(s.replicas ?? 1)])
      facts.push(['Memory', `${s.memoryMiB ?? 256} MiB`], ['Published', s.publish ? 'Yes' : 'No'],
        ['Deploy strategy', s.publish || s.strategy === 'recreate' ? 'Recreate' : 'Rolling'], ['Health check', describeHealthCheck(s.healthCheck, s.port)]);
      break;
    }
    case 'database': {
      const d = r.spec as DatabaseSpec;
      const title = { postgres: 'PostgreSQL', mysql: 'MySQL', mongodb: 'MongoDB', clickhouse: 'ClickHouse' }[d.engine] ?? d.engine;
      facts.unshift(['Engine', `${title} ${d.version}`], ['Network', d.network ? `${d.network}${d.subnet ? ` · ${d.subnet}` : ''}` : 'Default network']);
      break;
    }
    case 'queue': {
      const q = r.spec as QueueSpec;
      facts.unshift(['Visibility timeout', `${q.visibilityTimeoutSeconds ?? RULES.queueVisibilityDefault} s`], ['Retention', `${q.retentionHours ?? RULES.queueRetentionDefault} h`],
        ['Largest message', formatBytes(q.maxMessageBytes ?? RULES.messageBytesDefault)],
        ['Dead letters', q.maxReceives ? `After ${q.maxReceives} receives${q.deadLetterQueue ? ` → ${q.deadLetterQueue}` : ', kept until retention ends'}` : 'No limit']);
      break;
    }
    case 'topic': {
      const t = r.spec as TopicSpec;
      facts.unshift(['Retention', `${t.retentionHours ?? RULES.topicRetentionDefault} h`],
        ['Delivers to', (t.subscriptions ?? []).map((x) => (x.queue ? `queue/${x.queue}` : `${x.url ?? ''}${x.secret ? ' (signed)' : ''}`)).join(', ') || 'Live listeners only']);
      break;
    }
    case 'search': {
      const sp = r.spec as SearchSpec;
      facts.unshift(['Memory', `${sp.memoryMiB ?? RULES.searchMemoryDefault} MiB`], ['Network', sp.network ? `${sp.network}${sp.subnet ? ` · ${sp.subnet}` : ''}` : 'Default network']);
      break;
    }
    case 'transcoder': {
      const t = r.spec as TranscoderSpec;
      facts.unshift(['Output', `${t.output.bucket}/${t.output.prefix ?? ''}`], ['Presets', t.presets?.length ? t.presets.join(', ') : 'None'],
        ['Limits', `${t.concurrency ?? 1} at once · ${t.memoryMiB ?? RULES.transcodeMemoryDefault} MiB · ${t.timeoutSeconds ?? RULES.transcodeTimeoutDefault} s per file`]);
      break;
    }
    case 'cdn': {
      const c = r.spec as CDNSpec;
      facts.unshift(['Hostname', c.hostname], ['Origin', `${c.origin}${c.originPort ? ` : ${c.originPort}` : ''}`], ['HTTPS', TLS_LABEL[c.tls ?? 'auto'] ?? String(c.tls)],
        ['Default cache time', `${c.defaultTtlSeconds ?? RULES.cdnTtlDefault} s`], ['Cache size', `${c.maxSizeMiB ?? RULES.cdnSizeDefault} MiB`]);
      break;
    }
    case 'email': {
      const e = r.spec as EmailSpec;
      if (e.relay) facts.push(['Relay', `${e.relay.host}:${e.relay.port ?? RULES.relayPortDefault} (${e.relay.tls ?? 'starttls'})`]);
      facts.push(['Sender domain', e.domain ?? 'Any'], ['Messages kept', String(e.keepMessages ?? RULES.keepMessagesDefault)]);
      break;
    }
    case 'cache': {
      const c = r.spec as CacheSpec;
      facts.unshift(['Engine', 'Redis 7'], ['Max memory', `${c.maxMemoryMiB ?? 256} MiB`], ['Network', c.network ? `${c.network}${c.subnet ? ` · ${c.subnet}` : ''}` : 'Default network']);
      break;
    }
    case 'network': {
      const n = r.spec as NetworkSpec;
      facts.unshift(['Address range', n.cidr], ['Internet', n.internet === false ? 'None (private)' : 'Yes'],
        ['Subnets', (n.subnets ?? []).map((x) => `${x.name} ${x.cidr}`).join(', ')]);
      break;
    }
    case 'firewall': {
      const f = r.spec as FirewallSpec;
      facts.unshift(['Protects', f.targets.join(', ')], ['Other outgoing traffic', f.egressDefault === 'deny' ? 'Denied' : 'Allowed']);
      break;
    }
    case 'loadbalancer': {
      const l = r.spec as LoadBalancerSpec;
      facts.unshift(['Listens on', `${(l.protocol ?? 'http').toUpperCase()} ${l.port}`],
        ['Sends to', l.targets.map((t) => `${t.app}${t.port ? `:${t.port}` : ''}${t.weight && t.weight !== 1 ? ` ×${t.weight}` : ''}`).join(', ')],
        ['Algorithm', l.algorithm === 'least_conn' ? 'Fewest connections' : l.algorithm === 'source' ? 'Same client, same replica' : 'Round robin'],
        ['Replica check', l.healthCheck?.type === 'http' ? `HTTP ${l.healthCheck.path ?? '/'}` : 'TCP']);
      break;
    }
    case 'zone': {
      const z = r.spec as ZoneSpec;
      facts.unshift(['Zone', z.zone], ['Credentials secret', z.credentialsSecret]);
      break;
    }
    case 'record': {
      const d = r.spec as RecordSpec;
      facts.unshift(['Record', `${d.name} ${d.type}`], ['Zone', d.zone], ['Points at', d.machine ? "This machine's public address" : (d.values ?? []).join(', ')], ['TTL', `${d.ttl ?? 300} s`]);
      break;
    }
    case 'bucket':
      facts.unshift(['Versioning', (r.spec as BucketSpec).versioning ? 'On' : 'Off']);
      break;
    case 'vm': {
      const v = r.spec as VMSpec;
      const snap = v.image.startsWith(RULES.snapshotImagePrefix);
      facts.unshift(['Image', snap ? `Snapshot ${v.image.slice(RULES.snapshotImagePrefix.length)}` : (IMAGE_TITLES[v.image] ?? v.image)],
        ['Size', `${v.cpus ?? RULES.vmCpusDefault} CPU${(v.cpus ?? 1) > 1 ? 's' : ''} · ${((v.memoryMiB ?? RULES.vmMemoryDefault) / 1024).toFixed(1).replace(/\.0$/, '')} GiB`],
        ['Root disk', `${v.diskGiB ?? RULES.vmDiskDefault} GiB`], ['Network', v.network ? `${v.network}${v.subnet ? ` · ${v.subnet}` : ''}` : 'Default network']);
      if (v.disks?.length) facts.push(['Disks', v.disks.join(', ')]);
      if (v.publish?.length) facts.push(['Published ports', v.publish.join(', ')]);
      break;
    }
    case 'volume': {
      const v = r.spec as VolumeSpec;
      facts.unshift(['Kind of volume', v.type === 'disk' ? 'Disk (for one VM)' : 'Filesystem (for apps and jobs)']);
      break;
    }
    case 'snapshot':
      facts.unshift(['Copy of', (r.spec as SnapshotSpec).source]);
      break;
    case 'secret':
      break;
    case 'job': {
      const j = r.spec as JobSpec;
      facts.unshift(['Schedule', describeSchedule(j.schedule, j.timeZone)], ['Runs with', j.app ? `App ${j.app}` : (j.image ?? '')]);
      if (j.command?.length) facts.push(['Command', j.command.join(' ')]);
      facts.push(['Timeout', `${j.timeoutSeconds ?? RULES.jobTimeoutDefault} s`], ['If still running', j.overlap === 'allow' ? 'Run anyway' : 'Skip the next run']);
      break;
    }
    case 'backup': {
      const b = r.spec as BackupSpec;
      facts.unshift(['Source', b.source.database ? `Database ${b.source.database}` : `Cache ${b.source.cache ?? ''}`],
        ['Stored in', describeDestination(b)], ['Schedule', describeSchedule(b.schedule ?? RULES.backupScheduleDefault, b.timeZone)]);
      facts.push(['Keeps', `The newest ${b.keep ?? RULES.backupKeepDefault}${b.keepDays ? `, at most ${b.keepDays} days old` : ''}`], ['Key prefix', b.prefix ?? RULES.backupPrefixDefault]);
      break;
    }
    case 'domain': {
      const d = r.spec as DomainSpec;
      facts.unshift(['Hostname', d.hostname], ['App', d.port ? `${d.app} : ${d.port}` : d.app]);
      if (!d.tunnel) facts.splice(2, 0, ['HTTPS', TLS_LABEL[d.tls ?? 'auto'] ?? String(d.tls)]);
      if (d.tls !== 'off' && !d.tunnel) facts.push(['HTTP', d.redirectHttp === false ? 'Served as well' : 'Redirects to HTTPS']);
      break;
    }
    case 'tunnel': {
      // Public access (CON-76): the way in, what it reaches, and for a relay its edge machine.
      const t = r.spec as TunnelSpec;
      facts.unshift(['Way in', WAY_TITLE[t.provider]]);
      if (t.app) facts.splice(1, 0, ['App', t.port ? `${t.app} : ${t.port}` : t.app]);
      if (t.provider === 'tailscale') facts.push(['Who can open it', t.mode === 'public' ? 'Public, with Funnel' : 'Private: your tailnet']);
      if (t.credentialsSecret) facts.push(['Credentials secret', t.credentialsSecret]);
      if (t.accountId) facts.push(['Account ID', t.accountId]);
      if (t.machine) facts.push(['Edge machine', t.machine]);
      if (t.provider === 'custom' && t.image) facts.push(['Image', t.image]);
      if (gateOf(t).openPaths?.length) facts.push(['Open paths', (gateOf(t).openPaths ?? []).join(', ')]);
      break;
    }
  }
  facts.push(['Created', new Date(r.createdAt).toLocaleString()]);
  return (
    <Stack gap={4}>
      <Card as="section" title="Details">
        <DetailList items={facts.map(([k, v]) => ({ label: k, hint: DETAIL_HELP[k], mono: isMono(k), copy: v && isCopyable(k) ? v : undefined,
          value: isSecretLabel(k) && secretIds.get(v) ? <Link to={`/secrets/${secretIds.get(v) ?? ''}`}>{v}</Link> : v }))} />
      </Card>
      {r.kind === 'alert' && <AlertSummary alert={r} />}
      {r.kind === 'logexport' && <LogExportCard r={r} />}
      {r.kind === 'secret' && (r.spec as { source?: unknown }).source !== undefined && <SyncedSecretCard r={r} />}
      {r.kind === 'secret' && (canReveal ? <Reveal r={r} /> : <Card title="Value"><Text tone="subtle">{needRole('editor', r.project)}</Text></Card>)}
      {r.kind === 'domain' && ((r.spec as DomainSpec).tunnel ? <PublicDomainCard r={r} /> : <DomainCards r={r} />)}
      {r.kind === 'tunnel' && <TunnelCards r={r as Resource<'tunnel'>} />}
      {r.kind === 'app' && <PublicAddressesCard app={r} />}
      {r.kind === 'firewall' && <RulesCard spec={r.spec as FirewallSpec} />}
      {r.kind === 'vm' && <SSHCard r={r} />}
      {['database', 'queue', 'topic', 'search', 'email'].includes(r.kind) && r.status.phase === 'ready' && <ConnectCard r={r} />}
      {r.kind === 'cdn' && <DomainCards r={r} />}
      {(r.kind === 'app' || r.kind === 'database' || r.kind === 'cache' || r.kind === 'loadbalancer' || r.kind === 'vm') && <FirewallsCard r={r} />}
      {(r.kind === 'app' || r.kind === 'job') && <BucketAccessCard r={r} />}
      {r.kind === 'transcoder' && <TranscodeOutputsCards spec={r.spec as TranscoderSpec} />}
      {r.ownerId === null && (
        <Card>
          <Disclosure summary={<><Icons.Terminal /> Use from code</>}>
            <Stack gap={3}>
              <CodeBlock label="CLI" code={cliCommand(r)} />
              <CodeBlock label="OpenTofu" code={tofuBlock(r)} />
            </Stack>
          </Disclosure>
        </Card>
      )}
    </Stack>
  );
}

/** How to reach a VM over SSH, ready to copy (L2.5-002 §12). */
function SSHCard({ r }: { r: Resource }) {
  const addr = String(r.outputs.sshAddress ?? '');
  const [host, port] = addr.split(':');
  if (!addr) return null;
  return (
    <Card>
      <Stack gap={3}>
        <Text weight="bold">Connect over SSH</Text>
        <CodeBlock label="With the CLI" code={`enclave vm ssh ${r.name}`} />
        <CodeBlock label="With ssh" code={`ssh -p ${port ?? ''} ${String(r.outputs.sshUser ?? 'enclave')}@${host ?? ''}`} />
        <Text size="sm" tone="muted">The user has your keys and administrator rights (sudo). The address works from this machine only.</Text>
      </Stack>
    </Card>
  );
}

/** A zone's records as a table, with Add record (L2.5-002 §11). */
function ZoneRecords({ zone, onAdd }: { zone: Resource; onAdd: () => void }) {
  const api = useApi();
  const records = useLoad(`records-of:${zone.id}`, () => api.listResources('record'));
  const mine = (records.data ?? []).filter((x) => (x.spec as RecordSpec).zone === zone.name);
  const domain = (zone.spec as ZoneSpec).zone;
  return (
    <Stack gap={3}>
      <Inline justify="between" align="center">
        <Text tone="muted">{mine.length ? `${mine.length} record${mine.length === 1 ? '' : 's'} in ${domain}` : `No records in ${domain} yet.`}</Text>
        <Button size="sm" icon={<Icons.Plus />} onClick={onAdd}>Add record</Button>
      </Inline>
      {mine.length > 0 && (
        <Table aria-label={`Records in ${domain}`}>
          <thead>
            <tr><th scope="col">Name</th><th scope="col">Type</th><th scope="col">Value</th><th scope="col">TTL</th><th scope="col">Status</th></tr>
          </thead>
          <tbody>
            {mine.map((x) => {
              const rs = x.spec as RecordSpec;
              return (
                <tr key={x.id}>
                  <td><Link to={`/records/${encodeURIComponent(x.id)}`}><Text mono size="sm">{rs.name === '@' ? domain : `${rs.name}.${domain}`}</Text></Link></td>
                  <td><Chip>{rs.type}</Chip></td>
                  <td><Text mono size="sm">{rs.machine ? 'This machine' : (rs.values ?? []).join(', ')}</Text></td>
                  <td><Text size="sm">{rs.ttl ?? RULES.recordTtlDefault} s</Text></td>
                  <td><StatusBadge phase={x.status.phase} /></td>
                </tr>
              );
            })}
          </tbody>
        </Table>
      )}
    </Stack>
  );
}

/** A firewall's rules as sentences (L2.5-002 §11). */
function RulesCard({ spec }: { spec: FirewallSpec }) {
  const lines = [...(spec.ingress ?? []).map((x) => ruleSentence(x, 'from')), ...(spec.egressDefault === 'deny'
    ? [...(spec.egress ?? []).map((x) => ruleSentence(x, 'to')), 'Deny other outgoing traffic'] : [])];
  return (
    <Card title="Rules">
      <Stack gap={2}>
        {lines.length ? lines.map((l) => <Text key={l} mono>{l}</Text>) : <Text tone="muted">No rules: only EnclaveStack’s own services get in.</Text>}
        <Text size="sm" tone="muted">Published host ports stay reachable through the machine: firewalls govern traffic between resources and networks.</Text>
      </Stack>
    </Card>
  );
}

/** The firewalls that apply to a resource, or "Accepts everything" (L2.5-002 §11). */
function FirewallsCard({ r }: { r: Resource }) {
  const api = useApi();
  const fws = useLoad(`firewalls-of:${r.id}`, () => api.listResources('firewall'));
  const key = `${r.kind}/${r.name}`;
  const covering = (fws.data ?? []).filter((f) => (f.spec as FirewallSpec).targets.includes(key));
  return (
    <Card title="Firewalls">
      {covering.length ? (
        <Stack gap={1}>
          {covering.map((f) => <Link key={f.id} to={`/firewalls/${f.id}`}>{f.name}</Link>)}
        </Stack>
      ) : (
        <Text tone="muted">Accepts everything: no firewall names {key}.</Text>
      )}
    </Card>
  );
}

// Reveal asks first and hides the value again after 30 s or when the view changes (CON-08).
function Reveal({ r }: { r: Resource }) {
  const api = useApi();
  const [asking, setAsking] = useState(false);
  const [value, setValue] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    if (value === null) return;
    const t = setTimeout(() => setValue(null), REVEAL_MS);
    return () => clearTimeout(t);
  }, [value]);
  async function reveal() {
    setError(null);
    try {
      setValue(await api.revealSecret(r.id));
      setAsking(false);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : 'That did not work.');
    }
  }
  return (
    <Card title="Value">
      {value === null ? (
        <div>
          <Button icon={<Icons.Eye />} onClick={() => setAsking(true)}>Reveal value</Button>
        </div>
      ) : (
        <Stack>
          <Code label="Secret value">{value}</Code>
          <Inline wrap>
            <Button size="sm" icon={<Icons.Copy />} onClick={() => void navigator.clipboard?.writeText(value).catch(() => undefined)}>Copy</Button>
            <Button size="sm" icon={<Icons.EyeOff />} onClick={() => setValue(null)}>Hide</Button>
            <Text size="xs" tone="subtle">Hides automatically after 30 seconds.</Text>
          </Inline>
        </Stack>
      )}
      {asking && (
        <ConfirmDialog title={`Reveal ${r.name}?`} confirmLabel="Reveal" error={error} onCancel={() => setAsking(false)} onConfirm={reveal}
          body={<p>The value will be shown on screen, and the reveal is recorded in this secret&apos;s events.</p>} />
      )}
    </Card>
  );
}

/** Container output, or (build) a source app's latest build log (CON-24). */
function Logs({ id, build = false, version = '' }: { id: string; build?: boolean; version?: string }) {
  const api = useApi();
  const logs = useLoad(`logs:${id}:${build ? 'build' : 'run'}:${version}`, () => api.getLogs(id, build ? 1000 : 200, build));
  return (
    <Stack>
      <Terminal label={build ? 'Build output' : 'Logs'}>
        {logs.loading && !logs.data && <TerminalLine><TerminalText level="dim">Loading…</TerminalText></TerminalLine>}
        {logs.error && <TerminalLine><TerminalText level="error">{logs.error.message}</TerminalText></TerminalLine>}
        {logs.data && logs.data.length === 0 && <TerminalLine><TerminalText level="dim">{build ? 'No build yet.' : 'No output yet.'}</TerminalText></TerminalLine>}
        {build && logs.data?.map((l, i) => (
          <TerminalLine key={i}>
            <TerminalText level={/ERROR/.test(l.message) ? 'error' : 'plain'}>{l.message}</TerminalText>
          </TerminalLine>
        ))}
        {!build && logs.data?.map((l, i) => (
          <TerminalLine key={i}>
            <TerminalText level="dim">{new Date(l.time).toLocaleTimeString()}</TerminalText>{' '}
            <TerminalText level={l.stream === 'stderr' ? 'warn' : 'info'}>{l.stream}</TerminalText>{' '}
            {l.message}
          </TerminalLine>
        ))}
      </Terminal>
      <div>
        <Button size="sm" variant="ghost" icon={<Icons.RefreshCw />} onClick={logs.reload}>Refresh</Button>
      </div>
    </Stack>
  );
}

function Env({ spec }: { spec: AppSpec }) {
  const rows = [
    ...Object.entries(spec.env ?? {}).map(([k, v]) => ({ k, v, tag: 'plain', secret: false })),
    ...Object.entries(spec.secretEnv ?? {}).map(([k, v]) => ({ k, v: '••••••••', tag: `secret · ${v}`, secret: true })),
  ];
  if (rows.length === 0) return <EmptyState>This app has no environment variables.</EmptyState>;
  return (
    <Card>
      <Rows>
        {rows.map((e) => (
          <Row key={e.k}>
            <Text mono weight="semibold">{e.k}</Text>
            <Text mono tone="muted" breakAnywhere>{e.v}</Text>
            <Chip tone={e.secret ? 'accent' : 'neutral'}>{e.tag}</Chip>
          </Row>
        ))}
      </Rows>
    </Card>
  );
}

function duration(run: Run): string {
  if (!run.finishedAt) return '';
  const ms = new Date(run.finishedAt).getTime() - new Date(run.startedAt).getTime();
  return ms < 1000 ? `${ms} ms` : ms < 60_000 ? `${(ms / 1000).toFixed(1)} s` : `${Math.round(ms / 60_000)} min`;
}

/** A job's runs, newest first; refreshes every second while one is going (CON-22). */
function Runs({ id, version }: { id: string; version: string }) {
  const api = useApi();
  const runs = useLoad(`runs:${id}:${version}`, () => api.listRuns(id));
  const [open, setOpen] = useState<string | null>(null);
  usePolling(Boolean(runs.data?.some((x) => x.status === 'running')), POLL_MS, runs.reload);
  if (runs.error) return <Alert>{runs.error.message}</Alert>;
  if (runs.data?.length === 0) return <EmptyState>No runs yet. Use Run now, or wait for the schedule.</EmptyState>;
  return (
    <Card padding="flush">
      <Table aria-label="Runs">
        <thead>
          <tr><th scope="col">Started</th><th scope="col">Trigger</th><th scope="col">Result</th><th scope="col">Duration</th><th scope="col">Exit</th><th scope="col"><VisuallyHidden>Output</VisuallyHidden></th></tr>
        </thead>
        <tbody>
          {runs.data?.map((x) => (
            <tr key={x.id}>
              <td><Text mono size="sm">{new Date(x.startedAt).toLocaleString()}</Text></td>
              <td><Chip>{TRIGGER_LABEL[x.trigger] ?? x.trigger}</Chip></td>
              <td><Chip tone={RUN_TONE[x.status] ?? 'neutral'}>{RUN_LABEL[x.status] ?? x.status}</Chip>{x.status === 'skipped' && <Text size="xs" tone="subtle"> {x.message}</Text>}</td>
              <td><Text tone="muted">{duration(x)}</Text></td>
              <td><Text mono>{x.exitCode ?? ''}</Text></td>
              <td>{x.status !== 'skipped' && <Button size="sm" onClick={() => setOpen(x.id)} aria-label={`Output of run ${x.id}`}>Output</Button>}</td>
            </tr>
          ))}
        </tbody>
      </Table>
      {open && <RunOutput id={id} runId={open} onClose={() => setOpen(null)} />}
    </Card>
  );
}

function RunOutput({ id, runId, onClose }: { id: string; runId: string; onClose: () => void }) {
  const api = useApi();
  const run = useLoad(`run:${id}:${runId}`, () => api.getRun(id, runId));
  usePolling(run.data?.status === 'running', POLL_MS, run.reload);
  const d = run.data;
  return (
    <Drawer title={`Run ${runId}`} onClose={onClose}>
      <Stack gap={4}>
        {run.error && <Alert>{run.error.message}</Alert>}
        {d && (
          <>
            <Inline gap={2} wrap>
              <Chip tone={RUN_TONE[d.status] ?? 'neutral'}>{RUN_LABEL[d.status] ?? d.status}</Chip>
              <Chip>{TRIGGER_LABEL[d.trigger] ?? d.trigger}</Chip>
              {d.message && <Text tone="muted">{d.message}</Text>}
            </Inline>
            {d.outputs && d.outputs.length > 0 && (
              <Rows aria-label="Files written">
                {[...d.outputs].sort((a, b) => Number(b.endsWith('/master.m3u8')) - Number(a.endsWith('/master.m3u8'))).map((k) => (
                  <Row key={k}><Text mono size="sm">{k}</Text>{k.endsWith('/master.m3u8') && <Chip tone="accent">Master playlist</Chip>}</Row>
                ))}
              </Rows>
            )}
            <Terminal label="Run output">
              {d.outputTail ? d.outputTail.split('\n').map((line, i) => <TerminalLine key={i}>{line}</TerminalLine>)
                : <TerminalLine><TerminalText level="dim">{d.status === 'running' ? 'Running…' : 'No output.'}</TerminalText></TerminalLine>}
            </Terminal>
            <Text size="xs" tone="subtle">The last 64 KiB of output, stored exactly as the run printed it.</Text>
          </>
        )}
      </Stack>
    </Drawer>
  );
}

function describeDestination(b: BackupSpec): string {
  if (b.destination.s3) return `S3 ${b.destination.s3.bucket} at ${b.destination.s3.endpoint.replace(/^https?:\/\//, '')}`;
  return `Bucket ${b.destination.bucket ?? ''}`;
}

/** A backup's runs with what each stored, and Restore on the ones that can be restored (CON-23). */
function BackupRuns({ backup }: { backup: Resource }) {
  const api = useApi();
  const runs = useLoad(`runs:${backup.id}:${String(backup.outputs.lastRunAt ?? '')}`, () => api.listRuns(backup.id));
  const [details, setDetails] = useState<Run | null>(null);
  const [restoring, setRestoring] = useState<Run | null>(null);
  usePolling(Boolean(runs.data?.some((x) => x.status === 'running')), POLL_MS, runs.reload);
  if (runs.error) return <Alert>{runs.error.message}</Alert>;
  if (runs.data?.length === 0) return <EmptyState>No backups yet. Use Back up now, or wait for the schedule.</EmptyState>;
  return (
    <Card padding="flush">
      <Table aria-label="Backup runs">
        <thead>
          <tr><th scope="col">Started</th><th scope="col">Trigger</th><th scope="col">Result</th><th scope="col">Duration</th><th scope="col">Size</th><th scope="col"><VisuallyHidden>Actions</VisuallyHidden></th></tr>
        </thead>
        <tbody>
          {runs.data?.map((x) => (
            <tr key={x.id}>
              <td><Text mono size="sm">{new Date(x.startedAt).toLocaleString()}</Text></td>
              <td><Chip>{TRIGGER_LABEL[x.trigger] ?? x.trigger}</Chip></td>
              <td><Chip tone={RUN_TONE[x.status] ?? 'neutral'}>{RUN_LABEL[x.status] ?? x.status}</Chip></td>
              <td><Text tone="muted">{duration(x)}</Text></td>
              <td><Text mono>{x.artifact ? formatBytes(x.artifact.sizeBytes) : ''}</Text></td>
              <td>
                <Inline gap={2}>
                  {x.status !== 'running' && <Button size="sm" onClick={() => setDetails(x)} aria-label={`Details of run ${x.id}`}>Details</Button>}
                  {x.status === 'succeeded' && x.artifact && (
                    <Button size="sm" icon={<Icons.ArchiveRestore />} onClick={() => setRestoring(x)} aria-label={`Restore run ${x.id}`}>Restore</Button>
                  )}
                </Inline>
              </td>
            </tr>
          ))}
        </tbody>
      </Table>
      {details && (
        <Drawer title={`Run ${details.id}`} onClose={() => setDetails(null)}>
          <Stack gap={4}>
            <Inline gap={2} wrap>
              <Chip tone={RUN_TONE[details.status] ?? 'neutral'}>{RUN_LABEL[details.status] ?? details.status}</Chip>
              <Chip>{TRIGGER_LABEL[details.trigger] ?? details.trigger}</Chip>
            </Inline>
            {details.message && <Text as="p" tone="muted">{details.message}</Text>}
            {details.artifact && (
              <DetailList label="Backup file" items={withHelp([
                { label: 'Backup file', value: details.artifact.key, mono: true, copy: details.artifact.key },
                { label: 'Stored in', value: details.artifact.destination, mono: true },
                { label: 'Size', value: formatBytes(details.artifact.sizeBytes) },
                { label: 'SHA-256', value: details.artifact.sha256, mono: true, copy: details.artifact.sha256 },
              ])} />
            )}
          </Stack>
        </Drawer>
      )}
      {restoring && <RestoreDrawer backup={backup} run={restoring} onClose={() => setRestoring(null)} />}
    </Card>
  );
}

/**
 * Restore one run (BAK-06, BAK-07). Into a new database or cache is first and preselected; replacing the
 * source's data needs its name typed, and says that a safety backup is taken first.
 */
function RestoreDrawer({ backup, run, onClose }: { backup: Resource; run: Run; onClose: () => void }) {
  const api = useApi();
  const toast = useToast();
  const navigate = useNavigate();
  const b = backup.spec as BackupSpec;
  const kind = b.source.cache ? 'cache' : 'database';
  const source = b.source.database ?? b.source.cache ?? '';
  const [mode, setMode] = useState<'new' | 'replace'>('new');
  const [name, setName] = useState(`${source}-restored`.slice(0, RULES.nameMax));
  const [confirm, setConfirm] = useState('');
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const section = sectionByKind(kind);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setErrors({});
    setFormError(null);
    if (mode === 'replace' && confirm !== source) {
      setErrors({ confirmName: `Type ${source} to confirm that its data will be replaced.` });
      return;
    }
    setBusy(true);
    try {
      const target = await api.restore(backup.id, mode === 'new' ? { runId: run.id, mode, name: name.trim() } : { runId: run.id, mode, confirmName: confirm });
      toast(mode === 'new' ? `Restoring into ${kind} ${target.name}` : `Restoring ${kind} ${target.name}`);
      onClose();
      navigate(`/${section.slug}/${encodeURIComponent(target.id)}`);
    } catch (err) {
      if (err instanceof ApiError && err.details.length) {
        setErrors(Object.fromEntries(err.details.map((d) => [d.field, d.message])));
        if (!err.details.some((d) => d.field === 'name' || d.field === 'confirmName')) setFormError(err.message);
      } else setFormError(err instanceof ApiError ? err.message : 'That did not work. Try again.');
      setBusy(false);
    }
  }

  return (
    <Drawer title="Restore backup" onClose={onClose}>
      <Form onSubmit={(e) => void submit(e)} noValidate>
        <Text as="p" tone="muted">
          From the run of {new Date(run.startedAt).toLocaleString()}{run.artifact ? ` (${formatBytes(run.artifact.sizeBytes)})` : ''}. The backup is checked against its
          checksum before anything changes.
        </Text>
        {formError && <Alert>{formError}</Alert>}
        <Fieldset legend="Restore">
          <Choice name="restore-mode" value="new" checked={mode === 'new'} onChange={() => setMode('new')}
            title={`Into a new ${kind}`} description={`Recommended. ${source} keeps running untouched; compare, then switch your app over.`} />
          <Choice name="restore-mode" value="replace" checked={mode === 'replace'} onChange={() => setMode('replace')}
            title={`Replace the data in ${source}`} description="Overwrites what is there now. A safety backup is taken first; the restore stops if it fails." />
        </Fieldset>
        {mode === 'new' ? (
          <Field id="r-name" label={`New ${kind} name`} error={errors.name} hint="It gets the same settings as the source.">
            <Input id="r-name" mono value={name} onChange={(e) => setName(e.target.value)} spellCheck={false} autoComplete="off" aria-invalid={Boolean(errors.name)} />
          </Field>
        ) : (
          <>
            <Alert tone="warn">All data in the {kind} <strong>{source}</strong> is replaced with this backup. Anything written since then is lost, except in the safety backup.</Alert>
            <Field id="r-confirm" label={`Type ${source} to confirm`} error={errors.confirmName}>
              <Input id="r-confirm" mono value={confirm} onChange={(e) => setConfirm(e.target.value)} spellCheck={false} autoComplete="off" aria-invalid={Boolean(errors.confirmName)} />
            </Field>
          </>
        )}
        <Button type="submit" variant={mode === 'replace' ? 'danger' : 'dark'} size="lg" block disabled={busy || (mode === 'replace' && confirm !== source)}>
          {busy ? 'Restoring…' : mode === 'new' ? `Restore into ${name.trim() || 'a new ' + kind}` : `Replace data in ${source}`}
        </Button>
      </Form>
    </Drawer>
  );
}

/** The Backups tab on a database or cache: the backups that cover it (CON-23). */
function BackupsOf({ r, onAdd }: { r: Resource; onAdd: () => void }) {
  const api = useApi();
  const backups = useLoad(`backups-of:${r.id}`, () => api.listResources('backup'));
  const mine = (backups.data ?? []).filter((b) => {
    const s = b.spec as BackupSpec;
    return r.kind === 'database' ? s.source.database === r.name : s.source.cache === r.name;
  });
  if (backups.error) return <Alert>{backups.error.message}</Alert>;
  return (
    <Stack gap={4}>
      {backups.data && mine.length === 0 && (
        <EmptyState>
          <Stack gap={3}>
            <Text>Nothing backs up {r.name} yet.</Text>
            <div><Button variant="dark" icon={<Icons.Plus />} onClick={onAdd}>Set up backups</Button></div>
          </Stack>
        </EmptyState>
      )}
      {mine.length > 0 && (
        <Card>
          <Rows>
            {mine.map((b) => (
              <Row key={b.id}>
                <Link to={`/backups/${encodeURIComponent(b.id)}`}><Text mono weight="semibold">{b.name}</Text></Link>
                <Text tone="muted">{describeSchedule((b.spec as BackupSpec).schedule ?? RULES.backupScheduleDefault, (b.spec as BackupSpec).timeZone)} · {describeDestination(b.spec as BackupSpec)}</Text>
                <StatusBadge phase={b.status.phase} />
              </Row>
            ))}
          </Rows>
          <div><Button size="sm" icon={<Icons.Plus />} onClick={onAdd}>Add another backup</Button></div>
        </Card>
      )}
    </Stack>
  );
}

function Events({ id, version }: { id: string; version: string }) {
  const api = useApi();
  const events = useLoad(`events:${id}:${version}`, () => api.getEvents(id));
  if (events.data?.length === 0) return <EmptyState>No events yet.</EmptyState>;
  return (
    <Card>
      <Rows>
        {events.data?.map((e) => (
          <Row key={e.id}>
            <Text mono size="xs" tone="subtle">{new Date(e.createdAt).toLocaleString()}</Text>
            <Chip>{e.type.replace(/_/g, ' ')}</Chip>
            <Text>{e.message}</Text>
          </Row>
        ))}
      </Rows>
    </Card>
  );
}
