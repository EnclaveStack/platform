import {
  Alert, Badge, Button, buttonClass, Card, Chip, CodeBlock, ConfirmDialog, Drawer, Fact, Field, Form, Grid, Heading, Icons, Inline, Input, Page, PageHeader,
  ProgressBar, Row, Rows, Skeleton, Stack, Status, Table, Text, useToast,
} from '@enclavestack/ui';
import { useState, type FormEvent, type ReactNode } from 'react';
import { Link, useNavigate, useParams } from 'react-router';
import { useApi } from '../api/context';
import { RULES } from '../api/generated';
import { ApiError, type AddedMachine, type DrainResult, type MachineSummary } from '../api/types';
import { useSession } from '../auth/session';
import { fromRows, KeyValueEditor, toRows, type Row as KV } from '../components/KeyValueEditor';
import { MACHINE_STATE } from '../components/MachineFields';
import { PhaseDot } from '../components/StatusBadge';
import { formatBytes, percentOf } from '../lib/format';
import { isMachineAdmin } from '../lib/roles';
import { sectionByKind } from '../lib/sections';
import { usePageTitle } from '../lib/shell';
import { useLoad } from '../lib/useLoad';
import { usePolling } from '../lib/usePolling';
import { validateLabels, validateName, type FieldErrors } from '../lib/validation';

// Machines (CON-54, CON-55; L2.5-002 §15): every machine, the main one first, and a machine's page.

const POLL_MS = 5000;

const tone = (m: MachineSummary) => (m.state === 'online' ? 'ok' : m.state === 'offline' ? 'bad' : 'busy');

const errorText = (e: unknown) => (e instanceof ApiError ? e.message + (e.details.length ? ' ' + e.details.map((d) => `${d.field}: ${d.message}`).join(' ') : '') : 'That did not work. Try again.');

export function MachinesPage() {
  const api = useApi();
  const { name } = useParams();
  const navigate = useNavigate();
  const { state } = useSession();
  const admin = isMachineAdmin(state.status === 'signed-in' ? state.session : null);
  const list = useLoad('machines', () => api.listMachines());
  const [adding, setAdding] = useState(false);
  const [added, setAdded] = useState<AddedMachine | null>(null);
  // Waiting machines turn online without a reload (CON-54).
  usePolling(true, POLL_MS, list.reload);

  const back = <Link to="/machines" className={buttonClass({ variant: 'ghost', size: 'sm' })}><Icons.ChevronLeft /> All machines</Link>;
  const addDrawer = adding && (
    <Drawer title="Add machine" onClose={() => setAdding(false)}>
      <AddMachineForm onDone={(a) => { setAdding(false); setAdded(a); list.reload(); }} />
    </Drawer>
  );

  // The join command, once, on a page of its own (MCH-03).
  if (added) {
    return (
      <Page>
        <JoinCard added={added} onDone={() => { setAdded(null); navigate(`/machines/${encodeURIComponent(added.machine.name)}`); }} />
      </Page>
    );
  }

  // A machine opens on a page of its own (CON-64).
  if (name) {
    return (
      <Page>
        <MachineDetail key={name} name={name} admin={admin} back={back} onChanged={list.reload} onRemoved={() => { list.reload(); navigate('/machines', { replace: true }); }} />
      </Page>
    );
  }

  return (
    <Page>
      <PageHeader label="Machines" title="Machines" status={list.data && <Badge tone="soft">{list.data.length}</Badge>}
        lede="The computers EnclaveStack runs on. New resources go to the one with the most free memory, unless you choose."
        actions={admin && <Button variant="dark" icon={<Icons.Plus />} onClick={() => { setAdded(null); setAdding(true); }}>Add machine</Button>} />
      <Stack as="section" gap={3} aria-label="Machines list">
      {list.loading && !list.data && [0, 1].map((i) => <Skeleton key={i} shape="card" />)}
      {list.error && <Alert>{list.error.message}</Alert>}
      {list.data && (
        <Table aria-label="Machines">
          <thead>
            <tr>
              <th scope="col">Name</th>
              <th scope="col">State</th>
              <th scope="col">Memory</th>
              <th scope="col">Resources</th>
              <th scope="col">Notes</th>
            </tr>
          </thead>
          <tbody>
            {list.data.map((m) => (
              <tr key={m.id}>
                <td><Link to={`/machines/${encodeURIComponent(m.name)}`}><Text weight="semibold">{m.main ? `${m.name} · ${m.title}` : m.name}</Text></Link></td>
                <td><Status tone={tone(m)}>{MACHINE_STATE[m.state] ?? m.state}</Status></td>
                <td><Text size="sm" mono>{m.memoryTotalBytes > 0 ? formatBytes(m.memoryTotalBytes) : '—'}</Text></td>
                <td><Text size="sm">{m.resources} {m.resources === 1 ? 'resource' : 'resources'}</Text></td>
                <td>
                  <Inline gap={2} wrap>
                    {!m.schedulable && <Chip icon={<Icons.CirclePause />}>Not schedulable</Chip>}
                    {m.versionMismatch && <Chip tone="warn">Different version</Chip>}
                  </Inline>
                </td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
      </Stack>
      {addDrawer}
    </Page>
  );
}

function AddMachineForm({ onDone }: { onDone: (a: AddedMachine) => void }) {
  const api = useApi();
  const [name, setName] = useState('');
  const [labels, setLabels] = useState<KV[]>([]);
  const [errors, setErrors] = useState<FieldErrors>({});
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    const l = fromRows(labels);
    const found: FieldErrors = { ...validateLabels(l, 'labels') };
    const bad = validateName(name.trim());
    if (bad) found.name = bad;
    else if (name.trim() === RULES.mainMachine) found.name = "main is the main machine's name. Choose another.";
    setErrors(found);
    if (Object.keys(found).length) return;
    setBusy(true);
    setError(null);
    try {
      onDone(await api.addMachine(name.trim(), l));
    } catch (err) {
      if (err instanceof ApiError && err.details.length) setErrors(Object.fromEntries(err.details.map((d) => [d.field, d.message])));
      else setError(errorText(err));
      setBusy(false);
    }
  }

  return (
    <Form onSubmit={submit} noValidate>
      {error && <Alert>{error}</Alert>}
      <Field id="m-name" label="Name" hint="How you'll refer to it, like rack-1." error={errors.name}>
        <Input id="m-name" mono value={name} onChange={(e) => setName(e.target.value)} autoComplete="off" spellCheck={false} aria-invalid={Boolean(errors.name)} />
      </Field>
      <KeyValueEditor idPrefix="m-labels" label="Labels" rows={labels} onChange={setLabels} errors={errors} errorField={(k) => `labels.${k}`}
        addLabel="Add a label" emptyHint="Optional: resources can ask for machines with labels, like disk = ssd." />
      {errors.labels && <Alert>{errors.labels}</Alert>}
      <Button type="submit" variant="dark" size="lg" block disabled={busy}>{busy ? 'Adding…' : 'Add and show the join command'}</Button>
    </Form>
  );
}

/** The main machine's display name: the ID and the name used in commands stay (MCH-10). */
function RenameForm({ initial, onSave }: { initial: string; onSave: (title: string) => Promise<void> }) {
  const [title, setTitle] = useState(initial);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    const length = [...title.trim()].length;
    if (length < 1 || length > RULES.machineNameMax) return setError('Use 1–64 characters.');
    setBusy(true);
    setError(null);
    try {
      await onSave(title.trim());
    } catch (err) {
      setError(errorText(err));
      setBusy(false);
    }
  }

  return (
    <Form onSubmit={submit} noValidate>
      <Field id="m-title" label="Display name" hint="What people see. Commands still use the machine's name and ID." error={error ?? undefined}>
        <Input id="m-title" value={title} onChange={(e) => setTitle(e.target.value)} autoComplete="off" aria-invalid={Boolean(error)} autoFocus />
      </Field>
      <Button type="submit" variant="dark" size="lg" block disabled={busy}>{busy ? 'Saving…' : 'Save'}</Button>
    </Form>
  );
}

/** The join command, shown once (MCH-03). */
function JoinCard({ added, onDone }: { added: AddedMachine; onDone: () => void }) {
  return (
    <Card tone="accent" role="status" aria-label="Join command">
        <Stack gap={3}>
          <Heading level={2}>Join {added.machine.name}</Heading>
          <Text as="p">On the other computer, with Docker running, run this. It works once, until {new Date(added.expiresAt).toLocaleTimeString()}.</Text>
          <CodeBlock label="Join command" code={added.joinCommand} />
          <Text as="p" size="sm" tone="muted">
            That computer must reach this one, and this one must reach it on the agent port ({RULES.agentPortDefault}/tcp) and the tunnel ({RULES.tunnelPortDefault}/udp).
            It shows here as online once it joins.
          </Text>
          <div><Button variant="dark" onClick={onDone}>Done</Button></div>
        </Stack>
      </Card>
  );
}

function MachineDetail({ name, admin, back, onChanged, onRemoved }: { name: string; admin: boolean; back: ReactNode; onChanged: () => void; onRemoved: () => void }) {
  const api = useApi();
  const toast = useToast();
  const m = useLoad(`machine:${name}`, () => api.getMachineByName(name));
  const here = useLoad(`machine-resources:${name}`, async () => (await api.listResources()).filter((r) => r.outputs.machine === name && r.status.phase !== 'deleting'));
  const events = useLoad(`machine-events:${name}`, () => api.getMachineEvents(name));
  usePolling(true, POLL_MS, m.reload);
  const [busy, setBusy] = useState(false);
  const [editingLabels, setEditingLabels] = useState<KV[] | null>(null);
  const [labelErrors, setLabelErrors] = useState<FieldErrors>({});
  const [renaming, setRenaming] = useState(false);
  const [draining, setDraining] = useState(false);
  const [drained, setDrained] = useState<DrainResult | null>(null);
  const [removing, setRemoving] = useState<'ask' | 'force' | null>(null);
  const [removeError, setRemoveError] = useState<string | null>(null);
  const [joined, setJoined] = useState<AddedMachine | null>(null);

  usePageTitle(m.data ? (m.data.main ? m.data.title : m.data.name) : undefined);
  if (m.error) return <Alert>{m.error.message}</Alert>;
  const x = m.data;
  if (!x) return <Stack aria-busy="true"><Skeleton shape="title" /><Skeleton shape="block" /></Stack>;

  const refresh = () => {
    m.reload();
    here.reload();
    events.reload();
    onChanged();
  };
  async function run(label: string, fn: () => Promise<unknown>) {
    setBusy(true);
    try {
      await fn();
      toast(label);
      refresh();
    } catch (e) {
      toast(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  async function saveLabels() {
    const l = fromRows(editingLabels ?? []) ?? {};
    const found = validateLabels(l, 'labels');
    setLabelErrors(found);
    if (Object.keys(found).length) return;
    await run(`Saved the labels of ${x?.name ?? ''}`, () => api.updateMachineByName(name, { labels: l }));
    setEditingLabels(null);
  }
  async function drain() {
    setBusy(true);
    try {
      setDrained(await api.drainMachine(name));
      setDraining(false);
      refresh();
    } catch (e) {
      toast(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  async function remove(force: boolean) {
    setBusy(true);
    setRemoveError(null);
    try {
      await api.removeMachine(name, force);
      toast(`Removed ${name}`);
      onRemoved();
    } catch (e) {
      // A machine with resources: offer force when it's offline, naming what fails (CON-55).
      if (e instanceof ApiError && e.code === 'MACHINE_NOT_EMPTY' && x?.state === 'offline' && !force) setRemoving('force');
      setRemoveError(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <PageHeader
        label="Summary"
        back={back}
        title={x.main ? x.title : x.name}
        status={(
          <>
            <Status tone={tone(x)}>{MACHINE_STATE[x.state] ?? x.state}</Status>
            <Text size="xs" tone="subtle" mono title="The machine's ID">{x.id}</Text>
          </>
        )}
        lede={`${x.main ? `The main machine, called ${x.name} in commands` : 'A machine'} · since ${new Date(x.since).toLocaleString()} · last heard ${x.lastSeenAt ? new Date(x.lastSeenAt).toLocaleString() : '—'}`}
        meta={(!x.schedulable || x.versionMismatch) && (
          <>
            {!x.schedulable && <Chip icon={<Icons.CirclePause />}>Not schedulable</Chip>}
            {x.versionMismatch && <Chip tone="warn">Different version</Chip>}
          </>
        )}
        actions={admin && (
              <Inline gap={2} wrap>
                {x.main && <Button size="sm" icon={<Icons.Pencil />} disabled={busy} onClick={() => setRenaming(true)}>Rename</Button>}
                {x.state === 'waiting' && <Button size="sm" icon={<Icons.KeyRound />} disabled={busy} onClick={() => void api.newJoinCode(name).then(setJoined, (e: unknown) => toast(errorText(e)))}>New join code</Button>}
                <Button size="sm" icon={<Icons.CirclePause />} disabled={busy} onClick={() => void run(x.schedulable ? `${name} takes nothing new` : `${name} is schedulable again`, () => api.updateMachineByName(name, { schedulable: !x.schedulable }))}>
                  {x.schedulable ? 'Not schedulable' : 'Schedulable'}
                </Button>
                {x.state === 'online' && !x.main && <Button size="sm" icon={<Icons.ArrowRightLeft />} disabled={busy} onClick={() => setDraining(true)}>Drain</Button>}
                {!x.main && <Button size="sm" variant="danger-outline" icon={<Icons.Trash2 />} disabled={busy} onClick={() => { setRemoveError(null); setRemoving('ask'); }}>Remove</Button>}
              </Inline>
        )}
      />
      {x.message && <Alert tone={x.state === 'offline' ? 'bad' : 'info'}>{x.message}</Alert>}
      <Stack gap={5}>
        {joined && <JoinCard added={joined} onDone={() => setJoined(null)} />}
        {drained && (
          <Alert tone="info" role="status">
            <Stack gap={1}>
              <Text>Draining {name}: {drained.moving.length ? `moving ${drained.moving.join(', ')}` : 'nothing to move'}.</Text>
              {drained.pinned.length > 0 && <Text>Pinned here, so not moved: {drained.pinned.join(', ')}. Move them from their pages.</Text>}
            </Stack>
          </Alert>
        )}
        <Grid min="sm">
          <Card><Fact label="Address" mono value={x.address ?? '—'} /></Card>
          <Card><Fact label="System" value={x.os ? `${x.os} · ${x.arch}` : '—'} /></Card>
          <Card><Fact label="Agent" mono value={x.agentVersion || '—'} /></Card>
          <Card><Fact label="Engine" value={x.engine.driver ? `${x.engine.driver} ${x.engine.version}` : '—'} /></Card>
          <Card><Fact label="CPUs" value={x.cpus || '—'} /></Card>
          <Card><Fact label="VMs" value={x.vms.available ? 'Yes' : x.vms.message || 'No'} /></Card>
        </Grid>
        <Card title="Memory">
          <Stack gap={2}>
            <ProgressBar label="Memory reserved" value={percentOf(x.memoryReservedBytes, x.memoryTotalBytes)} />
            <Text size="sm" tone="muted">{x.memoryTotalBytes ? `${formatBytes(x.memoryReservedBytes)} reserved of ${formatBytes(x.memoryTotalBytes)}` : 'Not known until it joins.'}</Text>
          </Stack>
        </Card>
        <Card title="Labels" action={admin && editingLabels === null && <Button size="sm" icon={<Icons.Pencil />} onClick={() => setEditingLabels(toRows(x.labels))}>Edit</Button>}>
          {editingLabels === null ? (
            <Inline gap={1} wrap>
              {Object.entries(x.labels).map(([k, v]) => <Chip key={k} mono>{k}={v}</Chip>)}
              {!Object.keys(x.labels).length && <Text tone="subtle">No labels.</Text>}
            </Inline>
          ) : (
            <Stack gap={2}>
              <KeyValueEditor idPrefix="ml" label="Labels" rows={editingLabels} onChange={setEditingLabels} errors={labelErrors} errorField={(k) => `labels.${k}`} addLabel="Add a label" emptyHint="No labels." />
              {labelErrors.labels && <Alert>{labelErrors.labels}</Alert>}
              <Inline gap={2}>
                <Button variant="dark" disabled={busy} onClick={() => void saveLabels()}>Save labels</Button>
                <Button onClick={() => setEditingLabels(null)}>Cancel</Button>
              </Inline>
            </Stack>
          )}
        </Card>
        {x.links.length > 0 && (
          <Card title="Tunnel">
            <Table>
              <thead><tr><th scope="col">To</th><th scope="col">Connected</th><th scope="col">Last handshake</th></tr></thead>
              <tbody>
                {x.links.map((l) => (
                  <tr key={l.machine}>
                    <td><Text mono>{l.machine}</Text></td>
                    <td><Status tone={l.connected ? 'ok' : 'bad'}>{l.connected ? 'Connected' : 'Not connected'}</Status></td>
                    <td><Text tone="muted">{l.lastHandshakeAt ? new Date(l.lastHandshakeAt).toLocaleString() : 'Never'}</Text></td>
                  </tr>
                ))}
              </tbody>
            </Table>
          </Card>
        )}
        <Card title="Runs here">
          {here.data?.length === 0 ? <Text tone="subtle">Nothing runs here.</Text> : (
            <Rows>
              {here.data?.map((r) => (
                <Row key={r.id}>
                  <PhaseDot phase={r.status.phase} />
                  <Link to={`/${sectionByKind(r.kind).slug}/${encodeURIComponent(r.id)}`}><Text weight="semibold">{r.name}</Text></Link>
                  <Chip>{sectionByKind(r.kind).noun}</Chip>
                  <Text size="sm" tone="subtle">{r.project}</Text>
                </Row>
              ))}
            </Rows>
          )}
        </Card>
        <Card title="Events">
          {events.data?.length === 0 ? <Text tone="subtle">No events yet.</Text> : (
            <Rows>
              {events.data?.map((e) => (
                <Row key={e.id}>
                  <Text mono size="xs" tone="subtle">{new Date(e.createdAt).toLocaleString()}</Text>
                  <Chip>{e.type.replace(/_/g, ' ')}</Chip>
                  <Text>{e.message}</Text>
                </Row>
              ))}
            </Rows>
          )}
        </Card>
      </Stack>
      {renaming && (
        <Drawer title="Rename machine" onClose={() => setRenaming(false)}>
          <RenameForm initial={x.title} onSave={async (title) => {
            await api.updateMachineByName(name, { title });
            toast(`Renamed to ${title}`);
            setRenaming(false);
            refresh();
          }} />
        </Drawer>
      )}
      {draining && (
        <ConfirmDialog title={`Drain ${name}?`} confirmLabel="Drain" busy={busy} onCancel={() => setDraining(false)} onConfirm={() => void drain()}
          body={<p>{name} takes nothing new, and everything not pinned to it moves to other machines. Data moves stop while their data copies.</p>} />
      )}
      {removing && (
        <ConfirmDialog title={removing === 'force' ? `Remove ${name} anyway?` : `Remove ${name}?`} danger busy={busy} typeToConfirm={removing === 'force' ? name : undefined}
          confirmLabel={removing === 'force' ? 'Remove anyway' : 'Remove'} error={removeError ?? undefined}
          body={removing === 'force'
            ? <p>{name} is offline. What runs there fails, and data kept only there is lost: restore backups elsewhere.</p>
            : <p>{name} stops being part of EnclaveStack. Its agent is told to leave.</p>}
          onCancel={() => setRemoving(null)} onConfirm={() => void remove(removing === 'force')} />
      )}
    </>
  );
}
