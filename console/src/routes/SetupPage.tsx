import { Alert, Button, describedBy, Field, Form, Heading, Input, Stack, Text } from '@enclavestack/ui';
import { useState, type FormEvent } from 'react';
import { Navigate } from 'react-router';
import { resolveApiMode } from '../api/client';
import { DEMO_SETUP_CODE } from '../api/mock';
import { ApiError } from '../api/types';
import { useSession } from '../auth/session';
import { validateEmail, validatePassword, type FieldErrors } from '../lib/validation';
import { AuthLayout } from './AuthLayout';
import { Splash } from './RequireAuth';

const isMock = resolveApiMode(import.meta.env) === 'mock';

/** CON-02: shown until the first admin exists; needs the code `enclave server` printed. */
export function SetupPage() {
  const { state, setup } = useSession();
  const [setupCode, setSetupCode] = useState('');
  const [machineName, setMachineName] = useState('This Mac');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [errors, setErrors] = useState<FieldErrors>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  if (state.status === 'loading') return <Splash />;
  if (state.status === 'signed-in') return <Navigate to="/" replace />;
  if (!state.setupRequired) return <Navigate to="/login" replace />;

  function validate(): FieldErrors {
    const e: FieldErrors = {};
    if (!setupCode.trim()) e.setupCode = 'Enter the setup code from the terminal running enclave server.';
    if (!machineName.trim()) e.machineName = 'Use 1–64 characters.';
    const em = validateEmail(email);
    if (em) e.email = em;
    const pw = validatePassword(password);
    if (pw) e.password = pw;
    return e;
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    const found = validate();
    setErrors(found);
    setFormError(null);
    if (Object.keys(found).length) return;
    setBusy(true);
    try {
      await setup({ setupCode, machineName, email, password });
    } catch (err) {
      if (err instanceof ApiError && err.details.length) {
        setErrors(Object.fromEntries(err.details.map((d) => [d.field, d.message])));
      } else if (err instanceof ApiError && err.code === 'INVALID_SETUP_CODE') {
        setErrors({ setupCode: err.message });
      } else {
        setFormError(err instanceof ApiError ? err.message : 'Something went wrong. Try again.');
      }
      setBusy(false);
    }
  }

  return (
    <AuthLayout title="Let’s set up this machine." lede="Name it and create the admin account. Then add apps, databases and more — from here, the CLI or OpenTofu.">
      <Stack gap={2}>
        <Heading level={2} size="2xl">First-run setup</Heading>
        <Text as="p" tone="muted">This screen appears only until the admin account exists.</Text>
      </Stack>
      <Form onSubmit={submit} noValidate>
        {formError && <Alert>{formError}</Alert>}
        <Field id="setup-code" label="Setup code" hint="Printed where enclave server is running, like K7QD-3MXP-9TZA." error={errors.setupCode}>
          <Input id="setup-code" mono value={setupCode} onChange={(e) => setSetupCode(e.target.value)} autoComplete="one-time-code" spellCheck={false}
            aria-invalid={Boolean(errors.setupCode)} aria-describedby={describedBy('setup-code', errors.setupCode, true)} />
        </Field>
        <Field id="machine" label="Machine name" error={errors.machineName}>
          <Input id="machine" value={machineName} onChange={(e) => setMachineName(e.target.value)} aria-invalid={Boolean(errors.machineName)} />
        </Field>
        <Field id="admin-email" label="Admin email" error={errors.email}>
          <Input id="admin-email" type="email" autoComplete="email" value={email} onChange={(e) => setEmail(e.target.value)}
            placeholder="you@company.com" aria-invalid={Boolean(errors.email)} />
        </Field>
        <Field id="admin-password" label="Password" hint="At least 12 characters." error={errors.password}>
          <Input id="admin-password" type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)}
            aria-invalid={Boolean(errors.password)} aria-describedby={describedBy('admin-password', errors.password, true)} />
        </Field>
        <Button type="submit" variant="dark" size="lg" block disabled={busy}>
          {busy ? 'Creating…' : 'Create admin and open console'}
        </Button>
      </Form>
      {isMock && <Alert tone="info">Demo mode — the setup code is <Text mono>{DEMO_SETUP_CODE}</Text></Alert>}
    </AuthLayout>
  );
}
