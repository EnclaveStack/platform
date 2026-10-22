import { Alert, Button, Field, Form, Heading, Inline, Input, Stack, Text } from '@enclavestack/ui';
import { useState, type FormEvent } from 'react';
import { Navigate, useLocation } from 'react-router';
import { resolveApiMode } from '../api/client';
import { DEMO_ADMIN } from '../api/mock';
import { ApiError } from '../api/types';
import { useSession } from '../auth/session';
import { AuthLayout } from './AuthLayout';
import { Splash } from './RequireAuth';

const isMock = resolveApiMode(import.meta.env) === 'mock';

export function LoginPage() {
  const { state, signIn } = useSession();
  const location = useLocation();
  const from = (location.state as { from?: string } | null)?.from ?? '/apps';
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [showPassword, setShowPassword] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  if (state.status === 'loading') return <Splash />;
  if (state.status === 'signed-in') return <Navigate to={from} replace />;
  if (state.setupRequired) return <Navigate to="/setup" replace />;

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await signIn(email, password);
    } catch (err) {
      // CON-01: say what went wrong (including when to retry after throttling), keep what was typed.
      setError(err instanceof ApiError ? err.message : 'Something went wrong. Try again.');
      setBusy(false);
    }
  }

  return (
    <AuthLayout title="Welcome back to your cloud." lede="This console only talks to the machine it runs on. Nothing leaves it unless you publish an app.">
      <Stack gap={2}>
        <Heading level={2} size="2xl">Sign in to the console</Heading>
        <Text as="p" tone="muted">Use your account on this machine. Someone invited you? Open the link they sent.</Text>
      </Stack>
      <Form onSubmit={submit} noValidate>
        {error && <Alert>{error}</Alert>}
        <Field id="email" label="Email">
          <Input id="email" type="email" autoComplete="username" required value={email}
            onChange={(e) => setEmail(e.target.value)} aria-invalid={error ? true : undefined} placeholder="you@company.com" />
        </Field>
        <Field id="password" label="Password">
          <Inline>
            <Input id="password" type={showPassword ? 'text' : 'password'} autoComplete="current-password" required
              value={password} onChange={(e) => setPassword(e.target.value)} aria-invalid={error ? true : undefined} />
            <Button onClick={() => setShowPassword((v) => !v)} aria-label={showPassword ? 'Hide password' : 'Show password'}>
              {showPassword ? 'Hide' : 'Show'}
            </Button>
          </Inline>
        </Field>
        <Button type="submit" variant="dark" size="lg" block disabled={busy || !email || !password}>
          {busy ? 'Signing in…' : 'Sign in'}
        </Button>
      </Form>
      {isMock && (
        <Alert tone="info">
          Demo mode — sign in with <Text mono>{DEMO_ADMIN.email}</Text> / <Text mono>{DEMO_ADMIN.password}</Text>
        </Alert>
      )}
    </AuthLayout>
  );
}
