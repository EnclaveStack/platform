import { Alert, Button, Card, Chip, ConfirmDialog, Drawer, Field, Form, Grid, Icons, Inline, Input, Page, PageHead, Skeleton, Stack, Text, Textarea, useToast } from '@enclavestack/ui';
import { useState, type ChangeEvent, type FormEvent } from 'react';
import { useNavigate } from 'react-router';
import { useApi } from '../api/context';
import { ApiError, type CatalogueEntry } from '../api/types';
import { useSession } from '../auth/session';
import { StackForm } from '../components/StackFields';
import { useProject } from '../lib/project';
import { isMachineAdmin, needRole } from '../lib/roles';
import { sectionByKind } from '../lib/sections';
import { useLoad } from '../lib/useLoad';

// The catalogue (CON-59, L2.5-002 §15): built-in entries and the machine's own templates as cards;
// Install creates a stack.

export function CataloguePage() {
  const api = useApi();
  const toast = useToast();
  const navigate = useNavigate();
  const { state } = useSession();
  const admin = isMachineAdmin(state.status === 'signed-in' ? state.session : null);
  const { allowed, createIn } = useProject();
  const entries = useLoad('catalogue', () => api.listCatalogue());
  const [installing, setInstalling] = useState<CatalogueEntry | null>(null);
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<CatalogueEntry | null>(null);
  const [removeError, setRemoveError] = useState<string | null>(null);

  async function remove() {
    if (!removing) return;
    try {
      await api.removeTemplate(removing.name);
      toast(`Removed ${removing.title}`);
      setRemoving(null);
      entries.reload();
    } catch (e) {
      setRemoveError(e instanceof ApiError ? e.message + (e.details.length ? ' ' + e.details.map((d) => d.field).join(', ') : '') : 'That did not work. Try again.');
    }
  }

  return (
    <Page>
      <PageHead title="Catalogue" lede="Ready-made products, installed as one stack: an app with its database, storage and domain. Change or delete the stack and its parts follow." />
      {admin && (
        <div><Button icon={<Icons.Upload />} onClick={() => setAdding(true)}>Add template</Button></div>
      )}
      {entries.error && <Alert>{entries.error.message}</Alert>}
      <Grid min="md">
        {entries.loading && !entries.data && [0, 1, 2].map((i) => <Skeleton key={i} shape="card" />)}
        {entries.data?.map((e) => (
          <Card key={e.name} as="article" title={e.title} action={!e.builtIn && <Chip tone="accent">Yours</Chip>}>
            <Stack gap={3}>
              <Text tone="muted">{e.description}</Text>
              <Inline gap={1} wrap>
                {[...new Set(e.members.map((m) => m.kind))].map((k) => <Chip key={k}>{sectionByKind(k).noun}</Chip>)}
              </Inline>
              <Inline justify="between">
                <Text size="xs" tone="subtle" mono>v{e.version}</Text>
                <Inline gap={2}>
                  {admin && !e.builtIn && <Button size="sm" variant="danger-outline" onClick={() => { setRemoveError(null); setRemoving(e); }}>Remove</Button>}
                  {allowed('editor')
                    ? <Button size="sm" variant="dark" icon={<Icons.Plus />} onClick={() => setInstalling(e)}>Install</Button>
                    : <Text size="xs" tone="subtle" title={needRole('editor', createIn)}>View only</Text>}
                </Inline>
              </Inline>
            </Stack>
          </Card>
        ))}
      </Grid>
      {installing && (
        <Drawer title={`Install ${installing.title}`} onClose={() => setInstalling(null)}>
          <StackForm entry={installing} onDone={(r) => {
            setInstalling(null);
            toast(`Installing ${r.name}`);
            navigate(`/stacks/${encodeURIComponent(r.id)}`);
          }} />
        </Drawer>
      )}
      {adding && (
        <Drawer title="Add template" onClose={() => setAdding(false)}>
          <AddTemplateForm onDone={(e) => { setAdding(false); toast(`Added ${e.title} ${e.version}`); entries.reload(); }} />
        </Drawer>
      )}
      {removing && (
        <ConfirmDialog title={`Remove ${removing.title}?`} danger confirmLabel="Remove" error={removeError ?? undefined} onCancel={() => setRemoving(null)} onConfirm={() => void remove()}
          body={<p>Every version of {removing.name} leaves the catalogue. Stacks installed from it must be deleted first.</p>} />
      )}
    </Page>
  );
}

function AddTemplateForm({ onDone }: { onDone: (e: CatalogueEntry) => void }) {
  const api = useApi();
  const [body, setBody] = useState('');
  const [errors, setErrors] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);

  async function readFile(e: ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0];
    if (file) setBody(await file.text());
  }
  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!body.trim()) return setErrors(['Choose a file, or paste the template.']);
    setBusy(true);
    setErrors([]);
    try {
      onDone(await api.addTemplate(body));
    } catch (err) {
      // Template errors carry their line in the field: "body.line 12: resources[2].spec.port" (L3-002 §11.5).
      setErrors(err instanceof ApiError ? (err.details.length ? err.details.map((d) => `${d.field.replace(/^body\./, '')}: ${d.message}`) : [err.message]) : ['That did not work. Try again.']);
      setBusy(false);
    }
  }

  return (
    <Form onSubmit={submit} noValidate>
      <Text tone="muted">A template in YAML or JSON: its parameters and the resources it creates (L3-002 §11.6). A higher version of one of yours adds that version.</Text>
      {errors.length > 0 && <Alert><Stack gap={1}>{errors.map((x) => <Text key={x} size="sm">{x}</Text>)}</Stack></Alert>}
      <Field id="t-file" label="File">
        <Input id="t-file" type="file" accept=".yaml,.yml,.json,text/yaml,application/json" onChange={(e) => void readFile(e)} />
      </Field>
      <Field id="t-body" label="Template">
        <Textarea id="t-body" mono rows={16} value={body} onChange={(e) => setBody(e.target.value)} spellCheck={false} placeholder={'name: shop-worker\ntitle: Shop worker\nversion: 1.0.0\nresources:\n  - kind: app\n    name: ""\n    spec: { image: nginx }'} />
      </Field>
      <Button type="submit" variant="dark" size="lg" block disabled={busy}>{busy ? 'Adding…' : 'Add to the catalogue'}</Button>
    </Form>
  );
}
