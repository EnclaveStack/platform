import { Alert, Button, Card, ConfirmDialog, Drawer, Field, Form, Icons, Inline, Input, Page, PageHead, Stack, Table, Text, Textarea, useToast, VisuallyHidden } from '@enclavestack/ui';
import { useState, type FormEvent } from 'react';
import { useApi } from '../api/context';
import { RULES } from '../api/generated';
import { ApiError, type Project } from '../api/types';
import { useProject } from '../lib/project';
import { useLoad } from '../lib/useLoad';
import { validateName } from '../lib/validation';

// Projects (CON-45, PRJ-*): machine admins create, describe and delete them. A project holds resources
// whose names only need to be unique inside it; `default` always exists.
export function ProjectsPage() {
  const api = useApi();
  const toast = useToast();
  const { project: current, setProject, projectsChanged } = useProject();
  const projects = useLoad('projects', () => api.listProjects());
  const [editing, setEditing] = useState<Project | 'new' | null>(null);
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [deleting, setDeleting] = useState<Project | null>(null);
  const [deleteError, setDeleteError] = useState<string | null>(null);
  const [left, setLeft] = useState<string[]>([]);

  function open(p: Project | 'new') {
    setEditing(p);
    setError(null);
    setName(p === 'new' ? '' : p.name);
    setDescription(p === 'new' ? '' : p.description);
  }

  async function save(e: FormEvent) {
    e.preventDefault();
    if (!editing) return;
    if (editing === 'new') {
      const bad = validateName(name);
      if (bad) return setError(bad);
    }
    if ([...description].length > RULES.projectDescriptionMax) return setError('Use at most 280 characters.');
    setBusy(true);
    setError(null);
    try {
      if (editing === 'new') {
        await api.createProject(name, description);
        toast(`Created ${name}`);
        projectsChanged();
      } else {
        await api.updateProject(editing.name, description);
        toast(`Saved ${editing.name}`);
      }
      setEditing(null);
      projects.reload();
    } catch (err) {
      setError(err instanceof ApiError ? (err.details[0]?.message ?? err.message) : 'That did not work. Try again.');
    } finally {
      setBusy(false);
    }
  }

  async function remove() {
    if (!deleting) return;
    try {
      await api.deleteProject(deleting.name);
      toast(`Deleted ${deleting.name}`);
      if (current === deleting.name) setProject('');
      projectsChanged();
      setDeleting(null);
      projects.reload();
    } catch (err) {
      // A project with resources lists what's left (PRJ-05).
      setDeleteError(err instanceof ApiError ? err.message : 'That did not work. Try again.');
      setLeft(err instanceof ApiError ? err.details.map((d) => d.field) : []);
    }
  }

  return (
    <Page>
      <PageHead title="Projects" lede="Projects keep resources apart: each has its own names, people and private DNS. Pick one at the top of the sidebar to work in it." />
      <Card title="Projects" action={<Button variant="dark" icon={<Icons.Plus />} onClick={() => open('new')}>New project</Button>}>
        {projects.error && <Alert>{projects.error.message}</Alert>}
        <Table>
          <thead>
            <tr><th scope="col">Name</th><th scope="col">Description</th><th scope="col">Resources</th><th scope="col">Created</th><th scope="col"><VisuallyHidden>Actions</VisuallyHidden></th></tr>
          </thead>
          <tbody>
            {projects.data?.map((p) => (
              <tr key={p.name}>
                <td><Text weight="semibold" mono>{p.name}</Text></td>
                <td><Text tone="muted">{p.description || '—'}</Text></td>
                <td><Text mono>{p.resources}</Text></td>
                <td><Text tone="muted">{new Date(p.createdAt).toLocaleDateString()}</Text></td>
                <td>
                  <Inline gap={1} justify="end">
                    <Button size="sm" onClick={() => setProject(p.name)}>Open</Button>
                    <Button size="sm" onClick={() => open(p)}>Edit</Button>
                    {p.name !== RULES.defaultProject && (
                      <Button size="sm" variant="danger-outline" onClick={() => { setDeleteError(null); setLeft([]); setDeleting(p); }}>Delete</Button>
                    )}
                  </Inline>
                </td>
              </tr>
            ))}
          </tbody>
        </Table>
      </Card>
      {editing && (
        <Drawer title={editing === 'new' ? 'New project' : `Edit ${editing.name}`} onClose={() => setEditing(null)}>
          <Form onSubmit={save} noValidate>
            {error && <Alert>{error}</Alert>}
            <Field id="project-name" label="Name" hint={editing === 'new' ? '3–40 lowercase letters, numbers or hyphens. It can’t change later.' : 'Names can’t change.'}>
              <Input id="project-name" mono value={name} readOnly={editing !== 'new'} onChange={(e) => setName(e.target.value)} autoComplete="off" spellCheck={false} placeholder="shop" />
            </Field>
            <Field id="project-description" label="Description" hint="Optional, up to 280 characters.">
              <Textarea id="project-description" rows={3} value={description} onChange={(e) => setDescription(e.target.value)} />
            </Field>
            <Button type="submit" variant="dark" size="lg" block disabled={busy}>{busy ? 'Saving…' : editing === 'new' ? 'Create project' : 'Save'}</Button>
          </Form>
        </Drawer>
      )}
      {deleting && (
        <ConfirmDialog title={`Delete ${deleting.name}?`} danger confirmLabel="Delete project" typeToConfirm={deleting.name}
          error={deleteError ? <Stack gap={1}><span>{deleteError}</span>{left.map((x) => <Text key={x} mono size="sm">{x}</Text>)}</Stack> : undefined}
          body={<p>Only an empty project can be deleted. People lose their roles in it.</p>} onCancel={() => setDeleting(null)} onConfirm={() => void remove()} />
      )}
    </Page>
  );
}
