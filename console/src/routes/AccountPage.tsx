import { Alert, Button, Card, Chip, Field, Form, Inline, Input, Page, PageHead, Stack, Text, useToast } from '@enclavestack/ui';
import { useState, type FormEvent } from 'react';
import { useApi } from '../api/context';
import { ApiError } from '../api/types';
import { useSession } from '../auth/session';
import { ROLE_LABEL } from '../lib/roles';
import { validatePassword } from '../lib/validation';

// Your account (CON-47): your roles, and changing your password.
export function AccountPage() {
  const api = useApi();
  const toast = useToast();
  const { state } = useSession();
  const session = state.status === 'signed-in' ? state.session : null;
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [confirm, setConfirm] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function change(e: FormEvent) {
    e.preventDefault();
    const bad = validatePassword(next);
    if (bad) return setError(bad);
    if (next !== confirm) return setError("The two new passwords don't match.");
    setBusy(true);
    setError(null);
    try {
      await api.changePassword(current, next);
      toast('Password changed. Other sessions were signed out.');
      setCurrent('');
      setNext('');
      setConfirm('');
    } catch (err) {
      setError(err instanceof ApiError ? (err.details[0]?.message ?? err.message) : 'That did not work. Try again.');
    } finally {
      setBusy(false);
    }
  }

  return (
    <Page>
      <PageHead title="Your account" lede={session?.user.email} />
      <Card title="Your roles">
        <Stack gap={2}>
          <Text>On the whole machine: <strong>{session?.machineRole ? ROLE_LABEL[session.machineRole] : 'none'}</strong></Text>
          <Inline gap={1} wrap>
            {Object.entries(session?.projectRoles ?? {}).map(([p, r]) => <Chip key={p} mono>{p}: {ROLE_LABEL[r]}</Chip>)}
          </Inline>
          {session?.token && <Text size="sm" tone="muted">Signed in with a token limited to {ROLE_LABEL[session.token.role]}{session.token.projects ? ` in ${session.token.projects.join(', ')}` : ''}.</Text>}
        </Stack>
      </Card>
      {session?.authMethod === 'session' && (
        <Card title="Change password">
          <Form onSubmit={change} noValidate>
            {error && <Alert>{error}</Alert>}
            <Field id="pw-current" label="Current password">
              <Input id="pw-current" type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} />
            </Field>
            <Field id="pw-new" label="New password" hint="At least 12 characters.">
              <Input id="pw-new" type="password" autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} />
            </Field>
            <Field id="pw-confirm" label="New password again">
              <Input id="pw-confirm" type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} />
            </Field>
            <div><Button type="submit" variant="dark" disabled={busy || !current || !next}>{busy ? 'Saving…' : 'Change password'}</Button></div>
          </Form>
        </Card>
      )}
    </Page>
  );
}
