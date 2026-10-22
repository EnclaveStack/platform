import { Alert, Button, Field, Form, Heading, Inline, Input, Stack, Text } from '@enclavestack/ui';
import { useState, type FormEvent } from 'react';
import { Link, Navigate, useSearchParams } from 'react-router';
import { useApi } from '../api/context';
import { ApiError } from '../api/types';
import { useSession } from '../auth/session';
import { useLoad } from '../lib/useLoad';
import { validatePassword } from '../lib/validation';
import { AuthLayout } from './AuthLayout';
import { Splash } from './RequireAuth';

// Joining from an invite link, or setting a new password from a reset link (CON-46, IAM-03, IAM-07).
export function InvitePage() {
  const api = useApi();
  const { state, acceptInvite } = useSession();
  const [params] = useSearchParams();
  const code = params.get('code') ?? '';
  const info = useLoad(`invite:${code}`, () => api.getInvite(code));
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [joined, setJoined] = useState(false);

  if (joined && state.status === 'signed-in') return <Navigate to="/apps" replace />;
  if (info.loading && !info.data && !info.error) return <Splash />;

  async function submit(e: FormEvent) {
    e.preventDefault();
    const bad = validatePassword(password);
    if (bad) return setError(bad);
    if (password !== confirm) return setError("The two passwords don't match.");
    setBusy(true);
    setError(null);
    try {
      await acceptInvite(code, password);
      setJoined(true);
    } catch (err) {
      setError(err instanceof ApiError ? (err.details[0]?.message ?? err.message) : 'Something went wrong. Try again.');
      setBusy(false);
    }
  }

  const reset = info.data?.kind === 'reset';
  return (
    <AuthLayout title={reset ? 'Choose a new password.' : 'You were invited to a EnclaveStack.'} lede="EnclaveStack runs apps, databases and more on one machine. Your role decides what you can see and change.">
      <Stack gap={2}>
        <Heading level={2} size="2xl">{reset ? 'Set a new password' : 'Join this machine'}</Heading>
        {info.data && <Text as="p" tone="muted">For <Text mono>{info.data.email}</Text></Text>}
      </Stack>
      {info.error ? (
        <Stack gap={3}>
          <Alert>{info.error instanceof ApiError && info.error.code === 'NOT_FOUND' ? 'This link was used already or has expired. Ask the person who sent it for a new one.' : info.error.message}</Alert>
          <Inline><Link to="/login">Go to sign in</Link></Inline>
        </Stack>
      ) : (
        <Form onSubmit={submit} noValidate>
          {error && <Alert>{error}</Alert>}
          <Field id="invite-email" label="Email" hint="Your email can't change here.">
            <Input id="invite-email" value={info.data?.email ?? ''} readOnly aria-readonly="true" autoComplete="username" />
          </Field>
          <Field id="invite-password" label="Password" hint="At least 12 characters.">
            <Input id="invite-password" type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} />
          </Field>
          <Field id="invite-confirm" label="The same password again">
            <Input id="invite-confirm" type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} />
          </Field>
          <Button type="submit" variant="dark" size="lg" block disabled={busy || !password || !confirm}>
            {busy ? 'Saving…' : reset ? 'Set password and sign in' : 'Join'}
          </Button>
        </Form>
      )}
    </AuthLayout>
  );
}
