import { Alert, Button, Card, Checkbox, Chip, describedBy, Field, Form, Inline, Input, Rows, Row, Text } from '@enclavestack/ui';
import { useState, type FormEvent } from 'react';
import { Link } from 'react-router';
import { useApi } from '../api/context';
import { ApiError, type CatalogueEntry, type Resource, type StackMember, type StackSpec, type TemplateParameter } from '../api/types';
import { useProject } from '../lib/project';
import { sectionByKind } from '../lib/sections';
import { validateName, type FieldErrors } from '../lib/validation';
import { PhaseDot } from './StatusBadge';

// Installing and changing stacks (CON-59, CON-60; L2.5-002 §15): a form generated from a catalogue
// entry's parameters, and a stack's members.

/** The value a parameter field starts with: the stack's, else the default. */
function initial(p: TemplateParameter, spec?: StackSpec): string {
  const v = spec?.parameters?.[p.name] ?? p.default;
  return v === undefined ? '' : String(v);
}

/** Parameter values typed as the template expects; empty optional ones are left out. */
function typed(entry: CatalogueEntry, values: Record<string, string>): { parameters: Record<string, string | number | boolean>; secrets: Record<string, string> } {
  const parameters: Record<string, string | number | boolean> = {};
  const secrets: Record<string, string> = {};
  for (const p of entry.parameters) {
    const v = (values[p.name] ?? '').trim();
    if (p.type === 'secret') {
      if (v) secrets[p.name] = values[p.name] ?? '';
    } else if (p.type === 'boolean') parameters[p.name] = v === 'true';
    else if (v) parameters[p.name] = p.type === 'number' ? Number(v) : p.type === 'hostname' ? v.toLowerCase().replace(/\.$/, '') : v;
  }
  return { parameters, secrets };
}

export function validateParameters(entry: CatalogueEntry, values: Record<string, string>, editing: boolean): FieldErrors {
  const e: FieldErrors = {};
  for (const p of entry.parameters) {
    const v = (values[p.name] ?? '').trim();
    const field = `spec.${p.type === 'secret' ? 'secretParameters' : 'parameters'}.${p.name}`;
    if (p.required && !v && !(editing && p.type === 'secret')) e[field] = `${p.title} is required.`;
    else if (v && p.type === 'number' && !Number.isFinite(Number(v))) e[field] = `${p.title} is a number.`;
    else if (v && p.type === 'hostname' && !/^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+\.?$/i.test(v)) e[field] = `${p.title} is a host name, like blog.example.com.`;
  }
  return e;
}

/** Install (no existing) or edit the parameters of a stack. */
export function StackForm({ entry, existing, onDone }: { entry: CatalogueEntry; existing?: Resource<'stack'>; onDone: (r: Resource) => void }) {
  const api = useApi();
  const { createIn } = useProject();
  const [name, setName] = useState(existing?.name ?? '');
  const [values, setValues] = useState<Record<string, string>>(Object.fromEntries(entry.parameters.map((p) => [p.name, p.type === 'secret' ? '' : initial(p, existing?.spec)])));
  const [errors, setErrors] = useState<FieldErrors>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const stackName = name.trim() || '<stack>';

  async function submit(e: FormEvent) {
    e.preventDefault();
    const found = validateParameters(entry, values, Boolean(existing));
    if (!existing) {
      const n = validateName(name.trim());
      if (n) found.name = n;
    }
    setErrors(found);
    setFormError(null);
    if (Object.keys(found).length) return;
    const { parameters, secrets } = typed(entry, values);
    setBusy(true);
    try {
      if (existing) {
        // Parameters left empty are cleared with null (L3-002 §2).
        const cleared = Object.keys(existing.spec.parameters ?? {}).filter((k) => !(k in parameters));
        onDone(await api.updateResource(existing.id, { parameters: { ...parameters, ...Object.fromEntries(cleared.map((k) => [k, null])) }, ...(Object.keys(secrets).length ? { secretParameters: secrets } : {}) },
          existing.generation));
      } else {
        const spec: StackSpec = { template: entry.name, version: entry.version, ...(Object.keys(parameters).length ? { parameters } : {}), ...(Object.keys(secrets).length ? { secretParameters: secrets } : {}) };
        onDone(await api.createResource('stack', name.trim(), spec, createIn));
      }
    } catch (err) {
      if (err instanceof ApiError) {
        const fields = Object.fromEntries(err.details.map((d) => [d.field, d.message]));
        setErrors(fields);
        if (!err.details.some((d) => d.field === 'name' || d.field.startsWith('spec.parameters') || d.field.startsWith('spec.secretParameters'))) {
          setFormError(err.message + (err.details.length ? ' ' + err.details.map((d) => d.message).join(' ') : ''));
        }
      } else setFormError('That did not work. Try again.');
      setBusy(false);
    }
  }

  return (
    <Form onSubmit={submit} noValidate>
      <Text tone="muted">{entry.description}</Text>
      {formError && <Alert>{formError}</Alert>}
      {!existing && (
        <Field id="s-name" label="Name" hint={`Creates in ${createIn}. The stack's members are named after it.`} error={errors.name}>
          <Input id="s-name" mono value={name} onChange={(e) => setName(e.target.value)} autoComplete="off" spellCheck={false} aria-invalid={Boolean(errors.name)}
            aria-describedby={describedBy('s-name', errors.name, true)} />
        </Field>
      )}
      {entry.parameters.map((p) => {
        const id = `s-p-${p.name}`;
        const field = `spec.${p.type === 'secret' ? 'secretParameters' : 'parameters'}.${p.name}`;
        const set = (v: string) => setValues({ ...values, [p.name]: v });
        if (p.type === 'boolean') return <Checkbox key={p.name} label={p.title} checked={values[p.name] === 'true'} onChange={(e) => set(e.target.checked ? 'true' : 'false')} />;
        return (
          <Field key={p.name} id={id} label={`${p.title}${p.required ? '' : ' (optional)'}`} error={errors[field]}
            hint={p.type === 'secret' && existing ? 'Set. Leave empty to keep it.' : p.description}>
            <Input id={id} mono={p.type !== 'string'} type={p.type === 'secret' ? 'password' : p.type === 'number' ? 'number' : 'text'} value={values[p.name] ?? ''}
              onChange={(e) => set(e.target.value)} autoComplete="off" spellCheck={false} aria-invalid={Boolean(errors[field])}
              placeholder={p.type === 'hostname' ? 'blog.example.com' : undefined} />
          </Field>
        );
      })}
      <Card title={existing ? 'Its members' : 'Creates'}>
        <Inline gap={1} wrap>
          {entry.members.map((m) => (
            <Chip key={`${m.kind}/${m.name}`} mono>{sectionByKind(m.kind).noun} {m.name.replace('<stack>', stackName)}{m.optional ? ' (with some values)' : ''}</Chip>
          ))}
        </Inline>
      </Card>
      <Button type="submit" variant="dark" size="lg" block disabled={busy}>{busy ? (existing ? 'Saving…' : 'Installing…') : existing ? 'Save parameters' : `Install ${entry.title}`}</Button>
    </Form>
  );
}

/** A stack's members with their phases, linked (CON-60). */
export function StackMembers({ stack }: { stack: Resource }) {
  const members = (stack.outputs.members ?? []) as unknown as StackMember[];
  if (!members.length) return <Text tone="subtle">No members yet.</Text>;
  return (
    <Rows>
      {members.map((m) => (
        <Row key={m.id}>
          <PhaseDot phase={m.phase} />
          <Link to={`/${sectionByKind(m.kind).slug}/${encodeURIComponent(m.id)}`}><Text weight="semibold">{m.name}</Text></Link>
          <Chip>{sectionByKind(m.kind).noun}</Chip>
          <Text size="sm" tone="subtle">{m.phase}</Text>
        </Row>
      ))}
    </Rows>
  );
}
