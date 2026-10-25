import { Alert, Button, buttonClass, Card, Chip, ConfirmDialog, Drawer, Field, Form, Grid, Heading, Icons, Inline, Input, Page, PageHead, Skeleton, Stack, Text, Textarea, useToast } from '@enclavestack/ui';
import { useState, type ChangeEvent, type FormEvent } from 'react';
import { useNavigate, useSearchParams } from 'react-router';
import { useApi } from '../api/context';
import { ApiError, type CatalogueEntry } from '../api/types';
import { useSession } from '../auth/session';
import { providerOfEntry, WAYS_IN } from '../components/PublicAccessFields';
import { StackForm } from '../components/StackFields';
import { useProject } from '../lib/project';
import { isMachineAdmin, needRole } from '../lib/roles';
import { sectionByKind } from '../lib/sections';
import { useLoad } from '../lib/useLoad';

// The catalogue (CON-59, L2.5-002 §15): built-in entries and the machine's own templates as cards;
// Install creates a stack. Public access entries are ways in from the internet (CON-80, L2.5-002 §21): their
// Install opens the new tunnel form with that way in chosen, and Sign up links to the service. Connections hold an
// outside service's address and keys as secrets (CON-81, L2.5-002 §22).

const GROUPS = [
  { id: 'apps', title: 'Apps', lede: 'Ready-made products, installed as one stack: an app with its database, storage and domain.' },
  { id: 'public-app', title: 'Public access · An address for one app', lede: 'Reach one app from any device, without opening your router.' },
  { id: 'public-domain', title: 'Public access · Your own domain', lede: 'Serve your domains, like stage.example.com, from this machine.' },
  { id: 'connections', title: 'Connections', lede: 'Services your apps use elsewhere, like a hosted Supabase project: their address and keys kept as secrets, ready to attach to any app.' },
] as const;

const groupOf = (e: CatalogueEntry): (typeof GROUPS)[number]['id'] => {
  if (e.category === 'connections') return 'connections';
  if (e.category !== 'public-access') return 'apps';
  const way = WAYS_IN.find((w) => w.entry === e.name);
  return way?.family === 'domain' ? 'public-domain' : 'public-app';
};

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
  const [params] = useSearchParams();
  // `?category=public-access` shows only the ways in: Make public's Install another way in (CON-80).
  const only = params.get('category');
  const install = (e: CatalogueEntry) => {
    const provider = e.builtIn ? providerOfEntry(e.name) : undefined;
    if (provider) navigate(`/tunnels?new=1&provider=${provider}`);
    else setInstalling(e);
  };

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
      <PageHead title="Catalogue" lede="Ready-made products installed as one stack, the ways people on the internet reach your apps, and connections to services elsewhere. Change or delete a stack and its parts follow." />
      {admin && (
        <div><Button icon={<Icons.Upload />} onClick={() => setAdding(true)}>Add template</Button></div>
      )}
      {entries.error && <Alert>{entries.error.message}</Alert>}
      {entries.loading && !entries.data && <Grid min="md">{[0, 1, 2].map((i) => <Skeleton key={i} shape="card" />)}</Grid>}
      {GROUPS.filter((g) => !only || (only === 'public-access' ? g.id.startsWith('public-') : g.id === only)).map((g) => {
        const list = (entries.data ?? []).filter((e) => groupOf(e) === g.id);
        if (!list.length) return null;
        return (
          <Stack key={g.id} as="section" gap={3} aria-label={g.title}>
            <Stack gap={1}>
              <Heading level={2}>{g.title}</Heading>
              <Text tone="muted">{g.lede}</Text>
            </Stack>
            <Grid min="md">
              {list.map((e) => (
                <Card key={e.name} as="article" title={e.title} action={!e.builtIn && <Chip tone="accent">Yours</Chip>}>
                  <Stack gap={3}>
                    <Text tone="muted">{e.description}</Text>
                    <Inline gap={1} wrap>
                      {[...new Set(e.members.map((m) => m.kind))].map((k) => <Chip key={k}>{sectionByKind(k).noun}</Chip>)}
                    </Inline>
                    <Inline justify="between">
                      <Text size="xs" tone="subtle" mono>v{e.version}</Text>
                      <Inline gap={2}>
                        {e.signup && <a href={e.signup} target="_blank" rel="noreferrer" className={buttonClass({ size: 'sm', variant: 'ghost' })}><Icons.ExternalLink /> Sign up</a>}
                        {admin && !e.builtIn && <Button size="sm" variant="danger-outline" onClick={() => { setRemoveError(null); setRemoving(e); }}>Remove</Button>}
                        {allowed('editor')
                          ? <Button size="sm" variant="dark" icon={<Icons.Plus />} onClick={() => install(e)}>Install</Button>
                          : <Text size="xs" tone="subtle" title={needRole('editor', createIn)}>View only</Text>}
                      </Inline>
                    </Inline>
                  </Stack>
                </Card>
              ))}
            </Grid>
          </Stack>
        );
      })}
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
      <Field id="t-file" hint="A YAML or JSON template file from your computer." label="File">
        <Input id="t-file" type="file" accept=".yaml,.yml,.json,text/yaml,application/json" onChange={(e) => void readFile(e)} />
      </Field>
      <Field id="t-body" hint="Or paste the template here." label="Template">
        <Textarea id="t-body" mono rows={16} value={body} onChange={(e) => setBody(e.target.value)} spellCheck={false} placeholder={'name: shop-worker\ntitle: Shop worker\nversion: 1.0.0\nresources:\n  - kind: app\n    name: ""\n    spec: { image: nginx }'} />
      </Field>
      <Button type="submit" variant="dark" size="lg" block disabled={busy}>{busy ? 'Adding…' : 'Add to the catalogue'}</Button>
    </Form>
  );
}
