import {
  Alert, Button, Card, Checkbox, Chip, CodeBlock, ConfirmDialog, Field, Fieldset, Form, FormRow, Icons, Inline, Input, Page, PageHead, Select, Stack, Table,
  Text, useToast, VisuallyHidden,
} from '@enclavestack/ui';
import { useState, type FormEvent } from 'react';
import { useApi } from '../api/context';
import { RULES } from '../api/generated';
import { ApiError, type CreatedToken, type Role, type Token } from '../api/types';
import { useSession } from '../auth/session';
import { isMachineAdmin, ROLE_LABEL } from '../lib/roles';
import { useLoad } from '../lib/useLoad';

// API tokens for the CLI, CI jobs and OpenTofu (CON-13, CON-47, L2.5-002 §6, §14). A token carries a role
// and, optionally, a list of projects; it can never do more than its owner.
export function TokensPage() {
  const api = useApi();
  const toast = useToast();
  const { state } = useSession();
  const session = state.status === 'signed-in' ? state.session : null;
  const tokens = useLoad('tokens', () => api.listTokens());
  const projects = useLoad('token-projects', () => api.listProjects());
  const [name, setName] = useState('');
  const [role, setRole] = useState<Role | ''>('');
  const [allProjects, setAllProjects] = useState(true);
  const [chosen, setChosen] = useState<string[]>([]);
  const [days, setDays] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [created, setCreated] = useState<CreatedToken | null>(null);
  const [acknowledged, setAcknowledged] = useState(false);
  const [revoking, setRevoking] = useState<Token | null>(null);

  async function create(e: FormEvent) {
    e.preventDefault();
    setError(null);
    const n = name.trim();
    const d = days.trim() ? Number(days) : 0;
    if (!n || n.length > RULES.tokenNameMax) return setError('Give the token a name of 1–64 characters, like "laptop" or "ci".');
    if (days.trim() && (!Number.isInteger(d) || d < 1 || d > RULES.tokenMaxExpiryDays)) return setError('Use 1–365 days, or leave empty for no expiry.');
    if (!allProjects && !chosen.length) return setError('Choose at least one project, or All projects.');
    setBusy(true);
    try {
      setCreated(await api.createToken({ name: n, ...(role ? { role } : {}), ...(allProjects ? {} : { projects: chosen }), expiresInDays: d || undefined }));
      setAcknowledged(false);
      setName('');
      setDays('');
      tokens.reload();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'That did not work. Try again.');
    } finally {
      setBusy(false);
    }
  }

  async function revoke() {
    if (!revoking) return;
    try {
      await api.revokeToken(revoking.id);
      toast(`Revoked ${revoking.name}`);
      setRevoking(null);
      tokens.reload();
    } catch (err) {
      toast(err instanceof ApiError ? err.message : 'That did not work. Try again.');
    }
  }

  // Machine admins see every person's tokens and can revoke them (TOK-05).
  const everyone = isMachineAdmin(session);

  return (
    <Page>
      <PageHead title="API tokens"
        lede={<>Tokens let the <Text mono>enclave</Text> CLI, CI jobs and OpenTofu act on this machine. Treat them like passwords.</>} />

      {created && (
        <Card tone="accent" role="status" aria-label="New token">
          <Stack gap={4}>
            <Text as="p"><strong>{created.token.name}</strong> was created. You won&apos;t see this token again.</Text>
            <CodeBlock label="Token" code={created.secret} />
            <CodeBlock label="Use it" code={`export ENCLAVE_SERVER=${window.location.origin}\nexport ENCLAVE_TOKEN=${created.secret}`} />
            <Inline justify="between" wrap>
              <Checkbox label="I've copied it" checked={acknowledged} onChange={(e) => setAcknowledged(e.target.checked)} />
              <Button variant="dark" disabled={!acknowledged} onClick={() => setCreated(null)}>Done</Button>
            </Inline>
          </Stack>
        </Card>
      )}

      <Card title="Create a token">
        <Form onSubmit={create} noValidate>
          {error && <Alert>{error}</Alert>}
          <FormRow>
            <Field id="token-name" label="Name" hint="What it's for, e.g. laptop, github-actions, opentofu.">
              <Input id="token-name" value={name} onChange={(e) => setName(e.target.value)} maxLength={RULES.tokenNameMax} />
            </Field>
            <Field id="token-role" label="Role" hint="Never more than yours.">
              <Select id="token-role" value={role} onChange={(e) => setRole(e.target.value as Role | '')}>
                <option value="">The same as mine</option>
                <option value="admin">Admin: everything I can do</option>
                <option value="editor">Editor: create, change and delete</option>
                <option value="viewer">Viewer: read only</option>
              </Select>
            </Field>
            <Field id="token-days" label="Expires after (days)" hint="Empty = never.">
              <Input id="token-days" inputMode="numeric" value={days} onChange={(e) => setDays(e.target.value)} />
            </Field>
          </FormRow>
          <Fieldset legend="Projects">
            <Checkbox label="All projects I can see, now and later" checked={allProjects} onChange={(e) => setAllProjects(e.target.checked)} />
            {!allProjects && (
              <Inline gap={3} wrap>
                {(projects.data ?? []).map((p) => (
                  <Checkbox key={p.name} label={p.name} checked={chosen.includes(p.name)} onChange={(e) => setChosen(e.target.checked ? [...chosen, p.name] : chosen.filter((x) => x !== p.name))} />
                ))}
              </Inline>
            )}
          </Fieldset>
          <div>
            <Button type="submit" variant="dark" icon={<Icons.Plus />} disabled={busy}>{busy ? 'Creating…' : 'Create token'}</Button>
          </div>
        </Form>
      </Card>

      <Card title="Tokens">
        {tokens.error && <Alert>{tokens.error.message}</Alert>}
        <Table>
          <thead>
            <tr>
              <th scope="col">Name</th>{everyone && <th scope="col">Owner</th>}<th scope="col">Token</th><th scope="col">Role</th><th scope="col">Projects</th>
              <th scope="col">Last used</th><th scope="col">Expires</th><th scope="col"><VisuallyHidden>Actions</VisuallyHidden></th>
            </tr>
          </thead>
          <tbody>
            {tokens.data?.length === 0 && (
              <tr><td colSpan={everyone ? 8 : 7}><Text tone="subtle">No tokens yet. Create one for the CLI with <Text mono>enclave login</Text> or above.</Text></td></tr>
            )}
            {tokens.data?.map((t) => (
              <tr key={t.id}>
                <td><Text weight="semibold">{t.name}</Text></td>
                {everyone && <td><Text tone="muted">{t.userEmail}</Text></td>}
                <td><Text mono>{t.prefix}…</Text></td>
                <td><Chip tone={t.role === 'viewer' ? 'neutral' : 'accent'}>{ROLE_LABEL[t.role]}</Chip></td>
                <td><Text tone="muted">{t.projects ? t.projects.join(', ') : 'All'}</Text></td>
                <td><Text tone="muted">{t.lastUsedAt ? new Date(t.lastUsedAt).toLocaleString() : 'Never'}</Text></td>
                <td><Text tone="muted">{t.expiresAt ? new Date(t.expiresAt).toLocaleDateString() : 'Never'}</Text></td>
                <td><Button size="sm" variant="danger-outline" onClick={() => setRevoking(t)}>Revoke</Button></td>
              </tr>
            ))}
          </tbody>
        </Table>
      </Card>

      {revoking && (
        <ConfirmDialog title={`Revoke ${revoking.name}?`} danger confirmLabel="Revoke token" onCancel={() => setRevoking(null)} onConfirm={revoke}
          body={<p>Anything using <Text mono>{revoking.prefix}…</Text> stops working immediately.</p>} />
      )}
    </Page>
  );
}
