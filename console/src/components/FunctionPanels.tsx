import { Alert, Button, Card, Checkbox, Chip, CodeBlock, Drawer, EmptyState, Field, Form, FormRow, Inline, Input, Stack, Table, Terminal, TerminalLine, TerminalText, Text, Textarea } from '@enclavestack/ui';
import { useState, type FormEvent } from 'react';
import { Link } from 'react-router';
import { useApi } from '../api/context';
import { RULES } from '../api/generated';
import { ApiError, type AppSpec, type Resource, type Run, type RunDetail } from '../api/types';
import { formatAgo } from '../lib/format';
import { useLoad } from '../lib/useLoad';
import { usePolling } from '../lib/usePolling';
import { StatusBadge } from './StatusBadge';

// Detail-page panels of v0.10 (L2-016 CON-82, CON-84, L2.5-002 §23): a function's Invoke drawer, and an
// app's previews with its webhook.

const POLL_MS = 1000;

/** Sends a function an event and shows the run (FN-06). */
export function InvokeDrawer({ fn, onClose, onStarted }: { fn: Resource; onClose: () => void; onStarted: () => void }) {
  const api = useApi();
  const [body, setBody] = useState('{\n  "hello": "world"\n}');
  const [waitForEnd, setWaitForEnd] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<Run | RunDetail | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    let event: unknown;
    if (body.trim()) {
      try {
        event = JSON.parse(body);
      } catch {
        setError("The event isn't JSON. Send any JSON value: an object, a list, a string or a number.");
        return;
      }
    }
    setError(null);
    setBusy(true);
    try {
      setResult(await api.invoke(fn.id, event, waitForEnd));
      onStarted();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'That did not work. Try again.');
    } finally {
      setBusy(false);
    }
  }

  const tail = result && 'outputTail' in result ? result.outputTail : undefined;
  return (
    <Drawer title={`Invoke ${fn.name}`} onClose={onClose}>
      <Form onSubmit={submit} noValidate>
        {error && <Alert>{error}</Alert>}
        <Field id="f-invoke-body" label="Event" hint={`Any JSON, up to 256 KiB. The run reads it from ${RULES.eventPath} as { "source": "invoke", "body": … }.`}>
          <Textarea id="f-invoke-body" mono rows={8} value={body} onChange={(e) => setBody(e.target.value)} spellCheck={false} />
        </Field>
        <Checkbox label="Wait for the run to end (up to a minute)" checked={waitForEnd} onChange={(e) => setWaitForEnd(e.target.checked)} />
        <Inline gap={2}>
          <Button type="submit" variant="dark" disabled={busy}>{busy ? 'Running…' : 'Invoke'}</Button>
        </Inline>
      </Form>
      {result && (
        <Stack gap={3}>
          <Inline gap={2} wrap>
            <Chip tone={result.status === 'succeeded' ? 'ok' : result.status === 'running' ? 'accent' : 'bad'}>{result.status === 'running' ? 'Running' : result.status === 'succeeded' ? 'Succeeded' : 'Failed'}</Chip>
            <Text mono size="sm">{result.id}</Text>
            {result.message && <Text tone="muted">{result.message}</Text>}
          </Inline>
          {tail !== undefined ? (
            <Terminal label="Run output">
              {tail ? tail.split('\n').map((line, i) => <TerminalLine key={i}>{line}</TerminalLine>) : <TerminalLine><TerminalText level="dim">No output.</TerminalText></TerminalLine>}
            </Terminal>
          ) : (
            <Text size="sm" tone="muted">The run goes on; its output is under Runs.</Text>
          )}
          <CodeBlock label="The same from the CLI" code={`enclave function invoke ${fn.name} --data '${body.replace(/\s+/g, ' ').trim().replace(/'/g, `'\\''`)}'${waitForEnd ? ' --wait' : ''}`} />
        </Stack>
      )}
    </Drawer>
  );
}

/** An app's previews: its webhook, the list, and Create preview (PRV-06, PRV-07). */
export function Previews({ app, canEdit }: { app: Resource; canEdit: boolean }) {
  const api = useApi();
  const spec = app.spec as AppSpec;
  const list = useLoad(`previews:${app.id}:${app.updatedAt}`, () => api.listPreviews(app.id));
  const [ref, setRef] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  usePolling(Boolean(list.data?.some((p) => p.phase !== 'ready' && p.phase !== 'failed')), POLL_MS, list.reload);

  async function create(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.createPreview(app.id, ref.trim());
      setRef('');
      list.reload();
    } catch (err) {
      setError(err instanceof ApiError ? (err.details[0]?.message ?? err.message) : 'That did not work. Try again.');
    } finally {
      setBusy(false);
    }
  }

  return (
    <Stack gap={4}>
      <Card title="Webhook">
        <Stack gap={3}>
          {app.outputs.webhookUrl ? (
            <>
              <CodeBlock label="Payload URL" code={String(app.outputs.webhookUrl)} />
              <Text size="sm" tone="muted">
                In your repository's webhook settings, use this address with content type application/json, and the value of the secret {spec.source?.webhookSecret} as its secret (GitHub) or token (GitLab).
                Send pushes{spec.previews?.enabled ? ' and pull requests' : ''}.
              </Text>
              <Text size="sm">{spec.source?.deployOnPush ? `A push to ${spec.source.ref} redeploys ${app.name}.` : `Pushes to ${spec.source?.ref ?? ''} don't redeploy ${app.name}; turn it on in Edit.`}</Text>
            </>
          ) : (
            <Text size="sm" tone="muted">Choose a webhook secret in Edit to get pushes and pull requests from your Git host. Until then, make previews here or with enclave app preview create.</Text>
          )}
        </Stack>
      </Card>
      {!spec.previews?.enabled ? (
        <EmptyState>Previews are off. Turn them on in Edit to make an app for each pull request.</EmptyState>
      ) : (
        <Card padding="flush" title="Previews">
          {list.error && <Alert>{list.error.message}</Alert>}
          {list.data?.length === 0 && <Text tone="muted">No previews yet. They appear when a pull request opens, or make one from a branch below.</Text>}
          {Boolean(list.data?.length) && (
            <Table aria-label="Previews">
              <thead>
                <tr><th scope="col">Preview</th><th scope="col">Built from</th><th scope="col">State</th><th scope="col">Address</th><th scope="col">Deleted after</th></tr>
              </thead>
              <tbody>
                {list.data?.map((p) => (
                  <tr key={p.id}>
                    <td><Link to={`/apps/${encodeURIComponent(p.id)}`}><Text mono weight="semibold">{p.name}</Text></Link></td>
                    <td><Text mono size="sm">{p.pullRequest ? `#${p.pullRequest}` : p.ref}</Text></td>
                    <td><StatusBadge phase={p.phase} /></td>
                    <td>{p.url ? <a href={p.url} target="_blank" rel="noreferrer"><Text mono size="sm">{p.url}</Text></a> : <Text tone="subtle" size="sm">No domain</Text>}</td>
                    <td><Text size="sm" tone="muted">{p.expiresAt ? `${new Date(p.expiresAt).toLocaleString()} (${formatAgo(p.expiresAt)})` : ''}</Text></td>
                  </tr>
                ))}
              </tbody>
            </Table>
          )}
          {canEdit && (
            <Form onSubmit={create} noValidate>
              {error && <Alert>{error}</Alert>}
              <FormRow>
                <Field id="f-preview-ref" label="Branch or ref" hint="Makes a preview of it, or redeploys the one already made.">
                  <Input id="f-preview-ref" mono value={ref} onChange={(e) => setRef(e.target.value)} placeholder="feature/login" spellCheck={false} />
                </Field>
              </FormRow>
              <Inline>
                <Button type="submit" disabled={busy || !ref.trim()}>Create preview</Button>
              </Inline>
            </Form>
          )}
        </Card>
      )}
    </Stack>
  );
}
