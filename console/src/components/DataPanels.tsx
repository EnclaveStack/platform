import { Alert, Button, Card, Chip, CodeBlock, ConfirmDialog, Drawer, EmptyState, Field, Form, FormRow, HtmlPreview, Inline, Input, Stack, Table, Text, Textarea, useToast } from '@enclavestack/ui';
import { useState, type FormEvent } from 'react';
import { useApi } from '../api/context';
import { ApiError, type DatabaseSpec, type EmailSpec, type ReceivedMessage, type Resource, type TranscoderSpec } from '../api/types';
import { formatBytes } from '../lib/format';
import { useLoad } from '../lib/useLoad';
import { usePolling } from '../lib/usePolling';
import { KeyValueEditor, fromRows, type Row } from './KeyValueEditor';

// Detail-page panels of the v0.6 kinds (L2-001 CON-37 – CON-43, L2.5-002 §13). Message bodies and
// emails are shown as text; HTML email renders in a sandboxed frame that runs nothing and loads nothing.

const STATS_MS = 2000;

/** A connection string for a database's engine, with the password left out (CON-37). */
export function connectionString(r: Resource): string {
  const o = r.outputs;
  const host = String(o.host ?? o.internalHost ?? '');
  const port = String(o.port ?? o.internalPort ?? '');
  const user = String(o.username ?? '');
  const db = String(o.database ?? '');
  switch ((r.spec as DatabaseSpec).engine) {
    case 'mysql':
      return `mysql://${user}:<password>@${host}:${port}/${db}`;
    case 'mongodb':
      return `mongodb://${user}:<password>@${host}:${port}/${db}?authSource=admin`;
    case 'clickhouse':
      return `http://${user}:<password>@${host}:${port}/?database=${db}`;
    default:
      return `postgres://${user}:<password>@${host}:${port}/${db}`;
  }
}

/** How to reach a database, queue, topic, search or email service, password left out (CON-37 – CON-43). */
export function ConnectCard({ r }: { r: Resource }) {
  const o = r.outputs;
  let code: { label: string; code: string }[];
  let note = `The password is in the secret ${String(o.passwordSecret ?? '')}; apps get it as a secret environment variable.`;
  switch (r.kind) {
    case 'database':
      code = [{ label: 'From this machine', code: connectionString(r) }];
      if (o.nativePort) code.push({ label: 'ClickHouse native protocol', code: `clickhouse-client --host ${String(o.host)} --port ${String(o.nativePort)} --user ${String(o.username)}` });
      break;
    case 'queue':
    case 'topic':
      code = [
        { label: 'Inside EnclaveStack networks', code: `${String(o.internalUrl ?? '')} (user ${String(o.username ?? '')}, subject ${String(o.subject ?? '')})` },
        { label: 'Go (nats.go)', code: r.kind === 'queue'
          ? `nc, _ := nats.Connect("${String(o.internalUrl ?? '')}", nats.UserInfo("${String(o.username ?? '')}", os.Getenv("NATS_PASSWORD")))\njs, _ := jetstream.New(nc)\ncons, _ := js.Consumer(ctx, "${String(o.stream ?? '')}", "workers")\nmsgs, _ := cons.Fetch(10)`
          : `nc, _ := nats.Connect("${String(o.internalUrl ?? '')}", nats.UserInfo("${String(o.username ?? '')}", os.Getenv("NATS_PASSWORD")))\njs, _ := jetstream.New(nc)\njs.Publish(ctx, "${String(o.subject ?? '')}", []byte("hello"))` },
      ];
      break;
    case 'search':
      code = [{ label: 'From this machine', code: `curl -u admin:<password> ${String(o.url ?? o.internalUrl ?? '')}/_cluster/health` }];
      break;
    case 'email':
      code = [{ label: 'SMTP inside its network', code: `${String(o.internalHost ?? '')}:${String(o.smtpPort ?? 1025)} (user ${String(o.username ?? '')}, no TLS needed)` }];
      if (o.webUrl) code.push({ label: 'Message viewer on this machine', code: String(o.webUrl) });
      note += ' The viewer asks for the same user and password.';
      break;
    default:
      return null;
  }
  return (
    <Card>
      <Stack gap={3}>
        <Text weight="bold">Connect</Text>
        {code.map((c) => <CodeBlock key={c.label} label={c.label} code={c.code} />)}
        <Text size="sm" tone="muted">{note}</Text>
      </Stack>
    </Card>
  );
}

/** Send, peek, receive and delete a queue's messages (CON-38). */
export function QueueMessages({ queue }: { queue: Resource }) {
  const api = useApi();
  const toast = useToast();
  const list = useLoad(`messages:${queue.id}`, () => api.listMessages(queue.id, 20));
  usePolling(true, STATS_MS, list.reload);
  const [body, setBody] = useState('');
  const [attrs, setAttrs] = useState<Row[]>([]);
  const [received, setReceived] = useState<ReceivedMessage[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function act(fn: () => Promise<unknown>) {
    setBusy(true);
    setError(null);
    try {
      await fn();
      list.reload();
    } catch (e) {
      setError(e instanceof ApiError ? e.message + (e.details.length ? ' ' + e.details.map((d) => d.message).join(' ') : '') : 'That did not work. Try again.');
    } finally {
      setBusy(false);
    }
  }
  const send = (e: FormEvent) => {
    e.preventDefault();
    void act(async () => {
      const sent = await api.sendMessage(queue.id, { body, ...(fromRows(attrs) ? { attributes: fromRows(attrs) } : {}) });
      toast(`Message ${sent.id} sent`);
      setBody('');
    });
  };
  const stats = list.data?.stats;
  return (
    <Stack gap={4}>
      {error && <Alert>{error}</Alert>}
      {stats && (
        <Inline gap={2} wrap>
          <Chip tone="accent">{stats.messages} waiting</Chip>
          <Chip>{stats.inFlight} in flight</Chip>
          {stats.deadLettered > 0 && <Chip tone="warn">{stats.deadLettered} dead-lettered</Chip>}
          {stats.oldestAgeSeconds !== null && <Text size="sm" tone="muted">Oldest: {stats.oldestAgeSeconds} s ago</Text>}
        </Inline>
      )}
      <Card>
        <Form onSubmit={send}>
          <Field id="f-msg-body" label="Message">
            <Textarea id="f-msg-body" mono rows={3} value={body} onChange={(e) => setBody(e.target.value)} spellCheck={false} />
          </Field>
          <KeyValueEditor idPrefix="f-msg-attr" label="Attributes" rows={attrs} onChange={setAttrs} errors={{}} errorField={(k) => `attributes.${k}`}
            addLabel="Add attribute" emptyHint="Optional name–value pairs that travel with the message." />
          <Inline>
            <Button type="submit" variant="dark" disabled={busy}>Send</Button>
          </Inline>
        </Form>
      </Card>
      <Card padding="flush" title="Waiting">
        {list.data && list.data.items.length === 0 ? <EmptyState>No messages are waiting.</EmptyState> : (
          <Table aria-label="Waiting messages">
            <thead><tr><th scope="col">ID</th><th scope="col">Sent</th><th scope="col">Body</th></tr></thead>
            <tbody>
              {list.data?.items.map((m) => (
                <tr key={m.id}><td><Text mono size="sm">{m.id}</Text></td><td><Text size="sm">{new Date(m.sentAt).toLocaleString()}</Text></td><td><Text mono size="sm" breakAnywhere>{m.body}</Text></td></tr>
              ))}
            </tbody>
          </Table>
        )}
      </Card>
      <Card title="Receive" action={<Button size="sm" disabled={busy} onClick={() => void act(async () => setReceived(await api.receiveMessages(queue.id, 10, 0)))}>Receive up to 10</Button>}>
        {received.length === 0 ? <Text size="sm" tone="muted">Received messages are hidden from other workers until deleted or until the visibility timeout ends.</Text> : (
          <Table aria-label="Received messages">
            <thead><tr><th scope="col">ID</th><th scope="col">Received</th><th scope="col">Body</th><th scope="col" /></tr></thead>
            <tbody>
              {received.map((m) => (
                <tr key={m.receipt}>
                  <td><Text mono size="sm">{m.id}</Text></td>
                  <td><Text size="sm">{m.receiveCount}×</Text></td>
                  <td><Text mono size="sm" breakAnywhere>{m.body}</Text></td>
                  <td><Button size="sm" disabled={busy} onClick={() => void act(async () => {
                    const res = await api.deleteMessages(queue.id, [m.receipt]);
                    if (res.failed.length) throw new ApiError('VALIDATION_FAILED', res.failed[0]?.message ?? 'Not deleted.', 422);
                    setReceived(received.filter((x) => x.receipt !== m.receipt));
                  })}>Delete</Button></td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </Card>
    </Stack>
  );
}

/** Publish to a topic and see each subscription's deliveries (CON-39). */
export function TopicDeliveries({ topic }: { topic: Resource }) {
  const api = useApi();
  const toast = useToast();
  const stats = useLoad(`deliveries:${topic.id}`, () => api.getDeliveries(topic.id));
  usePolling(true, STATS_MS, stats.reload);
  const [body, setBody] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  async function publish(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const sent = await api.sendMessage(topic.id, { body });
      toast(`Message ${sent.id} published`);
      setBody('');
      stats.reload();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'That did not work. Try again.');
    } finally {
      setBusy(false);
    }
  }
  return (
    <Stack gap={4}>
      {error && <Alert>{error}</Alert>}
      <Card>
        <Form onSubmit={publish}>
          <Field id="f-pub-body" label="Message">
            <Textarea id="f-pub-body" mono rows={3} value={body} onChange={(e) => setBody(e.target.value)} spellCheck={false} />
          </Field>
          <Inline>
            <Button type="submit" variant="dark" disabled={busy}>Publish</Button>
            {stats.data && <Text size="sm" tone="muted">{stats.data.published} published so far</Text>}
          </Inline>
        </Form>
      </Card>
      <Card padding="flush" title="Subscriptions">
        {stats.data && stats.data.items.length === 0 ? <EmptyState>No subscriptions. Edit the topic to add queues or webhooks.</EmptyState> : (
          <Table aria-label="Deliveries">
            <thead><tr><th scope="col">Subscription</th><th scope="col">Delivered</th><th scope="col">Pending</th><th scope="col">Gave up</th><th scope="col">Last error</th></tr></thead>
            <tbody>
              {stats.data?.items.map((s) => (
                <tr key={s.target}>
                  <td><Text mono size="sm" breakAnywhere>{s.target}</Text></td>
                  <td>{s.delivered}</td>
                  <td>{s.pending}</td>
                  <td>{s.failed > 0 ? <Chip tone="bad">{s.failed}</Chip> : 0}</td>
                  <td><Text size="sm" tone="muted">{s.lastError}</Text></td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </Card>
    </Stack>
  );
}

/** An email service's kept messages, a viewer, and a test send (CON-43). */
export function EmailMessages({ email }: { email: Resource }) {
  const api = useApi();
  const list = useLoad(`emails:${email.id}`, () => api.listEmails(email.id, 50));
  usePolling(true, STATS_MS * 2, list.reload);
  const [open, setOpen] = useState<string | null>(null);
  const [composing, setComposing] = useState(false);
  return (
    <Stack gap={4}>
      {list.error && <Alert>{list.error.message}</Alert>}
      <Inline justify="between" wrap>
        <Text size="sm" tone="muted">{list.data ? `${list.data.total} kept` : ''}</Text>
        <Button size="sm" onClick={() => setComposing(true)}>Send a test message</Button>
      </Inline>
      <Card padding="flush">
        {list.data && list.data.items.length === 0 ? <EmptyState>No messages yet. Apps send through the SMTP address in Overview.</EmptyState> : (
          <Table aria-label="Messages">
            <thead><tr><th scope="col">Sent</th><th scope="col">From</th><th scope="col">To</th><th scope="col">Subject</th><th scope="col">Status</th></tr></thead>
            <tbody>
              {list.data?.items.map((m) => (
                <tr key={m.id}>
                  <td><Text size="sm">{new Date(m.sentAt).toLocaleString()}</Text></td>
                  <td><Text mono size="sm">{m.from}</Text></td>
                  <td><Text mono size="sm">{m.to.join(', ')}</Text></td>
                  <td><Button size="sm" variant="ghost" onClick={() => setOpen(m.id)}>{m.subject || '(no subject)'}</Button></td>
                  <td><Chip tone={m.relay === 'relayed' ? 'ok' : 'neutral'}>{m.relay === 'relayed' ? 'Relayed' : 'Captured'}</Chip></td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </Card>
      {open && <EmailViewer email={email} id={open} onClose={() => setOpen(null)} />}
      {composing && <EmailComposer email={email} onClose={() => { setComposing(false); list.reload(); }} />}
    </Stack>
  );
}

function EmailViewer({ email, id, onClose }: { email: Resource; id: string; onClose: () => void }) {
  const api = useApi();
  const msg = useLoad(`email:${email.id}:${id}`, () => api.getEmail(email.id, id));
  const m = msg.data;
  return (
    <Drawer title={m?.subject || 'Message'} onClose={onClose}>
      <Stack gap={3}>
        {msg.error && <Alert>{msg.error.message}</Alert>}
        {m && (
          <>
            <Text size="sm">From <Text mono>{m.from}</Text> to <Text mono>{[...m.to, ...m.cc].join(', ')}</Text></Text>
            {m.bcc.length > 0 && <Text size="sm" tone="muted">Bcc {m.bcc.join(', ')}</Text>}
            <Text size="xs" tone="subtle">{new Date(m.sentAt).toLocaleString()} · {formatBytes(m.sizeBytes)} · {m.relay === 'relayed' ? 'Handed to the relay' : 'Captured, not delivered'}</Text>
            {m.html && <HtmlPreview title="HTML body" html={m.html} />}
            {m.text && <CodeBlock label="Text" code={m.text} />}
          </>
        )}
      </Stack>
    </Drawer>
  );
}

function EmailComposer({ email, onClose }: { email: Resource; onClose: () => void }) {
  const api = useApi();
  const toast = useToast();
  const domain = (email.spec as EmailSpec).domain;
  const [from, setFrom] = useState(domain ? `test@${domain}` : '');
  const [to, setTo] = useState('');
  const [subject, setSubject] = useState('A test from EnclaveStack');
  const [text, setText] = useState('Hello from EnclaveStack.');
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  async function send(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      await api.sendEmail(email.id, { from: from.trim(), to: to.split(',').map((x) => x.trim()).filter(Boolean), subject, text });
      toast('Message sent');
      onClose();
    } catch (err) {
      setErrors(err instanceof ApiError ? Object.fromEntries(err.details.map((d) => [d.field.replace(/\[\d+\]$/, ''), d.message])) : { from: 'That did not work. Try again.' });
      setBusy(false);
    }
  }
  return (
    <Drawer title="Send a test message" onClose={onClose}>
      <Form onSubmit={send}>
        <FormRow>
          <Field id="f-mail-from" label="From" error={errors.from}><Input id="f-mail-from" mono value={from} onChange={(e) => setFrom(e.target.value)} /></Field>
          <Field id="f-mail-to" label="To" hint="Separate several with commas." error={errors.to}><Input id="f-mail-to" mono value={to} onChange={(e) => setTo(e.target.value)} /></Field>
        </FormRow>
        <Field id="f-mail-subject" label="Subject" error={errors.subject}><Input id="f-mail-subject" value={subject} onChange={(e) => setSubject(e.target.value)} /></Field>
        <Field id="f-mail-text" label="Text" error={errors.text}><Textarea id="f-mail-text" rows={4} value={text} onChange={(e) => setText(e.target.value)} /></Field>
        <Inline><Button type="submit" variant="dark" disabled={busy}>Send</Button></Inline>
      </Form>
    </Drawer>
  );
}

/** Asks for a file to convert now (CON-41). */
export function TranscodeDialog({ transcoder, onDone, onClose }: { transcoder: Resource; onDone: () => void; onClose: () => void }) {
  const api = useApi();
  const prefix = (transcoder.spec as TranscoderSpec).input.prefix ?? '';
  const [key, setKey] = useState(prefix);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  return (
    <ConfirmDialog title="Transcode a file" confirmLabel="Transcode" busy={busy} error={error}
      body={(
        <Field id="f-tc-key" label="Object key" hint={`A file in ${(transcoder.spec as TranscoderSpec).input.bucket}/${prefix}`}>
          <Input id="f-tc-key" mono value={key} onChange={(e) => setKey(e.target.value)} spellCheck={false} />
        </Field>
      )}
      onConfirm={async () => {
        setBusy(true);
        try {
          await api.transcode(transcoder.id, key.trim());
          onDone();
        } catch (e) {
          setError(e instanceof ApiError ? (e.details[0]?.message ?? e.message) : 'That did not work. Try again.');
          setBusy(false);
        }
      }}
      onCancel={onClose} />
  );
}
