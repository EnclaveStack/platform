import {
  Alert, Button, Card, Chip, Choice, CodeBlock, ConfirmDialog, Drawer, Field, Fieldset, Form, FormRow, IconButton, Icons, Inline, Input, Page, PageHead, Select, Stack, Table,
  Text, useToast, VisuallyHidden,
} from '@enclavestack/ui';
import { useState, type FormEvent } from 'react';
import { useApi } from '../api/context';
import { ROLES } from '../api/generated';
import { ApiError, type Invitation, type Role, type UserSummary } from '../api/types';
import { useSession } from '../auth/session';
import { adminProjects, isMachineAdmin, ROLE_LABEL } from '../lib/roles';
import { useLoad } from '../lib/useLoad';
import { validateEmail } from '../lib/validation';

// People (CON-46, L2.5-002 §14): who can use this machine and with which roles. Machine admins see
// everyone; project admins see the people of their projects and give roles there only.

interface RoleRow {
  project: string;
  role: Role;
}

function RolesEditor({ machineAdmin, machineRole, setMachineRole, rows, setRows, projects }: {
  machineAdmin: boolean; machineRole: Role | ''; setMachineRole: (r: Role | '') => void; rows: RoleRow[]; setRows: (r: RoleRow[]) => void; projects: string[];
}) {
  const free = projects.filter((p) => !rows.some((r) => r.project === p));
  return (
    <>
      {machineAdmin && (
        <Fieldset legend="On the whole machine">
          <Choice name="machine-role" value="" checked={machineRole === ''} onChange={() => setMachineRole('')} title="No machine role" description="Only the projects below." />
          {ROLES.map((r) => (
            <Choice key={r} name="machine-role" value={r} checked={machineRole === r} onChange={() => setMachineRole(r)} title={ROLE_LABEL[r]}
              description={r === 'admin' ? 'Everything, in every project: people, projects, settings.' : r === 'editor' ? 'Create, change and delete in every project.' : 'Read everything; change nothing.'} />
          ))}
        </Fieldset>
      )}
      <Fieldset legend="In projects">
        <Stack gap={2}>
          {rows.length === 0 && <Text size="sm" tone="muted">No project roles.</Text>}
          {rows.map((row, i) => (
            <FormRow key={row.project}>
              <Field id={`pr-project-${i}`} label="Project">
                <Select id={`pr-project-${i}`} value={row.project} onChange={(e) => setRows(rows.map((x, j) => (j === i ? { ...x, project: e.target.value } : x)))}>
                  {[row.project, ...free].map((p) => <option key={p} value={p}>{p}</option>)}
                </Select>
              </Field>
              <Field id={`pr-role-${i}`} label="Role">
                <Inline>
                  <Select id={`pr-role-${i}`} value={row.role} onChange={(e) => setRows(rows.map((x, j) => (j === i ? { ...x, role: e.target.value as Role } : x)))}>
                    {ROLES.map((r) => <option key={r} value={r}>{ROLE_LABEL[r]}</option>)}
                  </Select>
                  <IconButton label={`Remove the role in ${row.project}`} icon={<Icons.Trash2 />} onClick={() => setRows(rows.filter((_, j) => j !== i))} />
                </Inline>
              </Field>
            </FormRow>
          ))}
          {free.length > 0 && (
            <div>
              <Button size="sm" icon={<Icons.Plus />} onClick={() => setRows([...rows, { project: free[0] ?? '', role: 'viewer' }])}>Add a project role</Button>
            </div>
          )}
        </Stack>
      </Fieldset>
    </>
  );
}

function LinkCard({ invitation, onDone }: { invitation: Invitation; onDone: () => void }) {
  return (
    <Card tone="accent" role="status" aria-label="Link to send">
      <Stack gap={3}>
        {invitation.emailed ? (
          <Text as="p">Emailed to <strong>{invitation.user.email}</strong>. The link works once, until {new Date(invitation.expiresAt).toLocaleDateString()}; it&apos;s also here in case it doesn&apos;t arrive.</Text>
        ) : (
          <>
            {invitation.emailError && <Alert tone="warn">{invitation.emailError}</Alert>}
            <Text as="p">Send this link to <strong>{invitation.user.email}</strong>. It works once, until {new Date(invitation.expiresAt).toLocaleDateString()}.</Text>
          </>
        )}
        <CodeBlock label="Link" code={invitation.url} />
        <div><Button variant="dark" onClick={onDone}>Done</Button></div>
      </Stack>
    </Card>
  );
}

/** How invite and reset links are emailed (IAM-04): an email service and a sender, or links only. */
function InviteEmailCard() {
  const api = useApi();
  const toast = useToast();
  const machine = useLoad('people-machine', () => api.getMachine());
  const services = useLoad('people-email-services', () => api.listResources('email'));
  const current = machine.data?.inviteEmail ?? null;
  const [service, setService] = useState<string | null>(null);
  const [from, setFrom] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const chosen = service ?? (current ? `${current.project ?? 'default'}/${current.service}` : '');
  const sender = from ?? current?.from ?? '';

  async function save(off: boolean) {
    const m = machine.data;
    if (!m) return;
    setError(null);
    if (!off && (!chosen || validateEmail(sender.trim()))) return setError('Choose an email service and enter the sender, like enclave@example.com.');
    setBusy(true);
    try {
      const [project = '', name = ''] = chosen.split('/');
      // The other settings are sent as they are: leaving them out would clear them.
      await api.updateMachine({ publicAddress: m.publicAddress, acmeEmail: m.acmeEmail, inviteEmail: off ? null : { project, service: name, from: sender.trim() } });
      toast(off ? 'Invites are no longer emailed' : 'Invites will be emailed');
      setService(null);
      setFrom(null);
      machine.reload();
    } catch (err) {
      setError(err instanceof ApiError ? (err.details[0]?.message ?? err.message) : 'That did not work. Try again.');
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Email invites">
      <Stack gap={3}>
        <Text tone="muted">{current ? `Invite and reset links are emailed from ${current.from} through ${current.project ?? 'default'}/${current.service}.` : 'Links are shown to you to send. Choose an email service to also email them.'}</Text>
        {error && <Alert>{error}</Alert>}
        <FormRow>
          <Field id="invite-service" label="Email service" hint={services.data?.length === 0 ? 'Create an email service first (Email), in relay mode to reach real inboxes.' : undefined}>
            <Select id="invite-service" value={chosen} onChange={(e) => setService(e.target.value)}>
              <option value="">Choose…</option>
              {(services.data ?? []).map((r) => <option key={r.id} value={`${r.project}/${r.name}`}>{r.project}/{r.name}</option>)}
            </Select>
          </Field>
          <Field id="invite-from" label="From">
            <Input id="invite-from" type="email" value={sender} onChange={(e) => setFrom(e.target.value)} placeholder="enclave@example.com" />
          </Field>
        </FormRow>
        <Inline gap={2}>
          <Button variant="dark" disabled={busy} onClick={() => void save(false)}>{busy ? 'Saving…' : 'Email invites'}</Button>
          {current && <Button disabled={busy} onClick={() => void save(true)}>Stop emailing</Button>}
        </Inline>
      </Stack>
    </Card>
  );
}

export function PeoplePage() {
  const api = useApi();
  const toast = useToast();
  const { state } = useSession();
  const session = state.status === 'signed-in' ? state.session : null;
  const users = useLoad('people', () => api.listUsers());
  const projects = useLoad('people-projects', () => api.listProjects());
  const machineAdmin = isMachineAdmin(session);
  const mine = adminProjects(session, (projects.data ?? []).map((p) => p.name));
  const [editing, setEditing] = useState<UserSummary | 'new' | null>(null);
  const [email, setEmail] = useState('');
  const [machineRole, setMachineRole] = useState<Role | ''>('');
  const [rows, setRows] = useState<RoleRow[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [link, setLink] = useState<Invitation | null>(null);
  const [removing, setRemoving] = useState<UserSummary | null>(null);
  const [removeError, setRemoveError] = useState<string | null>(null);

  function open(u: UserSummary | 'new') {
    setEditing(u);
    setError(null);
    setEmail(u === 'new' ? '' : u.email);
    setMachineRole(u === 'new' ? '' : (u.machineRole ?? ''));
    setRows(u === 'new' ? (mine.length && !machineAdmin ? [{ project: mine[0] ?? '', role: 'viewer' }] : []) : Object.entries(u.projectRoles).map(([project, role]) => ({ project, role })));
  }

  async function save(e: FormEvent) {
    e.preventDefault();
    if (!editing) return;
    setError(null);
    const projectRoles = Object.fromEntries(rows.map((r) => [r.project, r.role]));
    if (editing === 'new') {
      const bad = validateEmail(email.trim());
      if (bad) return setError(bad);
      if (!machineRole && !rows.length) return setError('Give them a machine role or a role in at least one project.');
    }
    setBusy(true);
    try {
      if (editing === 'new') {
        setLink(await api.inviteUser({ email: email.trim(), ...(machineRole ? { machineRole } : {}), projectRoles }));
      } else {
        // Roles that were there and aren't any more are removed with null (L3-002 §10.2).
        const removed = Object.keys(editing.projectRoles).filter((p) => !(p in projectRoles));
        await api.updateUser(editing.id, { ...(machineAdmin ? { machineRole: machineRole || null } : {}), projectRoles: { ...projectRoles, ...Object.fromEntries(removed.map((p) => [p, null])) } });
        toast(`Saved ${editing.email}`);
      }
      setEditing(null);
      users.reload();
    } catch (err) {
      setError(err instanceof ApiError ? (err.details[0]?.message ?? err.message) : 'That did not work. Try again.');
    } finally {
      setBusy(false);
    }
  }

  async function newLink(u: UserSummary) {
    try {
      setLink(await api.resetUser(u.id));
    } catch (err) {
      toast(err instanceof ApiError ? err.message : 'That did not work. Try again.');
    }
  }

  async function remove() {
    if (!removing) return;
    try {
      await api.removeUser(removing.id);
      toast(`Removed ${removing.email}`);
      setRemoving(null);
      users.reload();
    } catch (err) {
      setRemoveError(err instanceof ApiError ? err.message : 'That did not work. Try again.');
    }
  }

  const self = session?.user.id;
  return (
    <Page>
      <PageHead title="People" lede="Everyone who can sign in to this machine, and what they can do. Invite someone to give them a link that sets their password." />
      {link && <LinkCard invitation={link} onDone={() => setLink(null)} />}
      <Card title="People" action={(machineAdmin || mine.length > 0) && <Button variant="dark" icon={<Icons.UserPlus />} onClick={() => open('new')}>Invite someone</Button>}>
        {users.error && <Alert>{users.error.message}</Alert>}
        <Table>
          <thead>
            <tr>
              <th scope="col">Email</th><th scope="col">Status</th><th scope="col">Machine role</th><th scope="col">Project roles</th><th scope="col">Last sign-in</th>
              <th scope="col"><VisuallyHidden>Actions</VisuallyHidden></th>
            </tr>
          </thead>
          <tbody>
            {users.data?.map((u) => (
              <tr key={u.id}>
                <td><Text weight="semibold">{u.email}</Text>{u.id === self && <Text size="xs" tone="subtle"> (you)</Text>}</td>
                <td><Chip tone={u.status === 'active' ? 'ok' : 'warn'}>{u.status === 'active' ? 'Active' : 'Invited'}</Chip></td>
                <td><Text tone="muted">{u.machineRole ? ROLE_LABEL[u.machineRole] : '—'}</Text></td>
                <td>
                  <Inline gap={1} wrap>
                    {Object.entries(u.projectRoles).map(([p, r]) => <Chip key={p} mono>{p}: {ROLE_LABEL[r]}</Chip>)}
                    {!Object.keys(u.projectRoles).length && <Text tone="subtle">—</Text>}
                  </Inline>
                </td>
                <td><Text tone="muted">{u.lastSignInAt ? new Date(u.lastSignInAt).toLocaleString() : 'Never'}</Text></td>
                <td>
                  {u.id !== self && (
                    <Inline gap={1} justify="end">
                      <Button size="sm" onClick={() => open(u)}>Roles</Button>
                      <Button size="sm" onClick={() => void newLink(u)}>{u.status === 'invited' ? 'Copy a new link' : 'Reset password'}</Button>
                      {machineAdmin && <Button size="sm" variant="danger-outline" onClick={() => { setRemoveError(null); setRemoving(u); }}>Remove</Button>}
                    </Inline>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </Table>
      </Card>

      {machineAdmin && <InviteEmailCard />}
      {editing && (
        <Drawer title={editing === 'new' ? 'Invite someone' : `Roles of ${editing.email}`} onClose={() => setEditing(null)}>
          <Form onSubmit={save} noValidate>
            {error && <Alert>{error}</Alert>}
            {editing === 'new' && (
              <Field id="invite-email" label="Email">
                <Input id="invite-email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} placeholder="dev@example.com" autoComplete="off" />
              </Field>
            )}
            <RolesEditor machineAdmin={machineAdmin} machineRole={machineRole} setMachineRole={setMachineRole} rows={rows} setRows={setRows}
              projects={machineAdmin ? (projects.data ?? []).map((p) => p.name) : mine} />
            <Button type="submit" variant="dark" size="lg" block disabled={busy}>
              {busy ? 'Saving…' : editing === 'new' ? 'Invite and show the link' : 'Save roles'}
            </Button>
          </Form>
        </Drawer>
      )}
      {removing && (
        <ConfirmDialog title={`Remove ${removing.email}?`} danger confirmLabel="Remove" typeToConfirm={removing.email} error={removeError ?? undefined}
          body={<p>They are signed out at once and their tokens stop working. What they made stays.</p>} onCancel={() => setRemoving(null)} onConfirm={() => void remove()} />
      )}
    </Page>
  );
}
