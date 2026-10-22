import { Alert, Button, ConfirmDialog, describedBy, Field, Fieldset, Icons, Select, Stack, Text } from '@enclavestack/ui';
import { useState } from 'react';
import { useApi } from '../api/context';
import { RULES } from '../api/generated';
import { ApiError, type Kind, type MachineSummary, type Placed, type Resource, type VolumeSpec } from '../api/types';
import { formatBytes } from '../lib/format';
import { useLoad } from '../lib/useLoad';
import type { FieldErrors } from '../lib/validation';
import { fromRows, KeyValueEditor, toRows, type Row } from './KeyValueEditor';

// Placement on a machine (CON-56, CON-57; L2.5-002 §15): the Machine field of placed kinds' forms, the
// machine a resource runs on, and the move dialog.

export const MACHINE_STATE: Record<string, string> = { waiting: 'Waiting to join', online: 'Online', offline: 'Offline' };

export function isPlaced(kind: Kind): boolean {
  return (RULES.placedKinds as readonly Kind[]).includes(kind);
}

export function isDataKind(kind: Kind): boolean {
  return (RULES.dataKinds as readonly Kind[]).includes(kind);
}

export function freeBytes(m: MachineSummary): number {
  return Math.max(m.memoryTotalBytes - m.memoryReservedBytes, 0);
}

/** "rack-1 · 6.2 GiB free", or why it can't take anything. */
export function machineOption(m: MachineSummary): string {
  const label = m.main ? `${m.name} (${m.title})` : m.name;
  if (m.state !== 'online') return `${label} · ${MACHINE_STATE[m.state]?.toLowerCase() ?? m.state}`;
  if (!m.schedulable) return `${label} · not schedulable`;
  return `${label} · ${formatBytes(freeBytes(m))} free`;
}

/** The form state of a placed kind's machine and labels. */
export function usePlacement(existing?: Resource) {
  const p = (existing?.spec ?? {}) as Placed;
  const [machine, setMachine] = useState(p.machine ?? '');
  const [labels, setLabels] = useState<Row[]>(toRows(p.machineLabels));
  return {
    machine, setMachine, labels, setLabels,
    /** Adds machine and machineLabels when set. */
    apply<T extends object>(spec: T): T {
      const l = fromRows(labels);
      return { ...spec, ...(machine ? { machine } : {}), ...(!machine && l ? { machineLabels: l } : {}) };
    },
  };
}

export function MachineFields({ placement, machines, errors }: {
  placement: ReturnType<typeof usePlacement>; machines: MachineSummary[]; errors: FieldErrors;
}) {
  return (
    <Fieldset legend="Machine">
      <Field id="f-machine" label="Run on" hint={placement.machine ? 'It stays there until you move it.' : 'EnclaveStack picks the machine with the most free memory that fits.'}
        error={errors['spec.machine']}>
        <Select id="f-machine" value={placement.machine} onChange={(e) => placement.setMachine(e.target.value)} aria-invalid={Boolean(errors['spec.machine'])}
          aria-describedby={describedBy('f-machine', errors['spec.machine'], true)}>
          <option value="">Choose for me</option>
          {machines.map((m) => <option key={m.id} value={m.name} disabled={m.state === 'waiting'}>{machineOption(m)}</option>)}
        </Select>
      </Field>
      {!placement.machine && (
        <KeyValueEditor idPrefix="f-machine-labels" label="Required labels" rows={placement.labels} onChange={placement.setLabels} errors={errors}
          errorField={(k) => `spec.machineLabels.${k}`} addLabel="Add a label" emptyHint="Optional: only machines with every label, like disk = ssd." />
      )}
      {errors['spec.machineLabels'] && <Alert>{errors['spec.machineLabels']}</Alert>}
    </Fieldset>
  );
}

/** What a move does, in the dialog's words (MOVE-*, CON-57). */
export function moveEffect(r: Resource, users: Resource[]): string {
  if (r.kind === 'app') return 'The app deploys there as a rolling deploy: the old replicas serve until the new ones are healthy.';
  if (r.kind === 'job') return 'Its next runs start there.';
  if (r.kind === 'loadbalancer') return 'It starts there, then stops here; its address stays.';
  const size = r.kind === 'volume' && (r.spec as VolumeSpec).type === 'disk' ? ` (${String(r.outputs.sizeGiB ?? '')} GiB)` : r.outputs.usedBytes !== undefined ? ` (${formatBytes(Number(r.outputs.usedBytes))})` : '';
  const along = users.length ? ` ${users.map((u) => `${u.kind} ${u.name}`).join(', ')} ${users.length === 1 ? 'moves' : 'move'} with it.` : '';
  return `It stops, its data is copied${size}, and it starts there with the same address; it is unavailable while the data copies.${along}`;
}

export function MoveDialog({ r, onClose, onMoved }: { r: Resource; onClose: () => void; onMoved: () => void }) {
  const api = useApi();
  const machines = useLoad('move-machines', () => api.listMachines());
  const everything = useLoad(`move-users:${r.project}`, () => (r.kind === 'volume' ? api.listResources(undefined, r.project) : Promise.resolve([])));
  const [to, setTo] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const here = String(r.outputs.machine ?? '');
  const users = (everything.data ?? []).filter((o) => o.status.phase !== 'deleting' && (
    ((o.kind === 'app' || o.kind === 'job') && ((o.spec as { volumes?: { volume: string }[] }).volumes ?? []).some((m) => m.volume === r.name))
    || (o.kind === 'vm' && ((o.spec as { disks?: string[] }).disks ?? []).includes(r.name))));
  const targets = (machines.data ?? []).filter((m) => m.name !== here);
  const offlineHere = machines.data?.find((m) => m.name === here)?.state === 'offline';

  async function move() {
    if (!to) return setError('Choose the machine to move it to.');
    setBusy(true);
    setError(null);
    try {
      await api.updateResource(r.id, { machine: to }, r.generation);
      onMoved();
    } catch (e) {
      setError(e instanceof ApiError ? (e.details[0]?.message ?? e.message) : 'That did not work. Try again.');
      setBusy(false);
    }
  }

  return (
    <ConfirmDialog title={`Move ${r.name} to another machine`} confirmLabel="Move" busy={busy} error={error ?? undefined} onCancel={onClose} onConfirm={() => void move()}
      body={
        <Stack gap={3}>
          {offlineHere && isDataKind(r.kind) && (
            <Alert tone="warn">{here} is offline, so the data of {r.name} can&apos;t be read. Restore a backup to a new {r.kind} instead.</Alert>
          )}
          <Field id="move-to" label={`From ${here || 'its machine'} to`}>
            <Select id="move-to" value={to} onChange={(e) => setTo(e.target.value)}>
              <option value="">Choose…</option>
              {targets.map((m) => <option key={m.id} value={m.name} disabled={m.state !== 'online' || !m.schedulable}>{machineOption(m)}</option>)}
            </Select>
          </Field>
          <Text size="sm" tone="muted">{moveEffect(r, users)}</Text>
          {targets.length === 0 && machines.data && <Text size="sm" tone="subtle">There is no other machine yet. Add one under Machines.</Text>}
        </Stack>
      } />
  );
}

/** "Move" in a resource's toolbar, for editors of placed resources. */
export function MoveButton({ r, disabled, onMoved }: { r: Resource; disabled: boolean; onMoved: () => void }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button size="sm" icon={<Icons.ArrowRightLeft />} disabled={disabled} onClick={() => setOpen(true)}>Move</Button>
      {open && <MoveDialog r={r} onClose={() => setOpen(false)} onMoved={() => { setOpen(false); onMoved(); }} />}
    </>
  );
}

/** "rack-1 is offline since …" on a resource whose machine is offline (FAIL-01, CON-56). */
export function OfflineBanner({ r }: { r: Resource }) {
  const api = useApi();
  const here = String(r.outputs.machine ?? '');
  const m = useLoad(`machine-of:${here}`, () => (here ? api.getMachineByName(here) : Promise.resolve(null)));
  if (!m.data || m.data.state !== 'offline') return null;
  const since = new Date(m.data.since).toLocaleString();
  const what = isDataKind(r.kind) ? 'Its data is there: it waits for the machine to come back.' : 'It restarts on another machine shortly.';
  return <Alert tone="warn" role="status">{here} is offline since {since}. {what}</Alert>;
}
