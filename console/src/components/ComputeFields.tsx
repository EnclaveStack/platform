import { Button, Checkbox, FieldError, FieldHint, Fieldset, FormRow, Icons, IconButton, Input, Select } from '@enclavestack/ui';
import type { Resource, VMSpec, VolumeMount, VolumeSpec } from '../api/types';

// Field groups of the v0.5 forms (L2.5-002 §12): volume mounts of apps and jobs, a VM's disks.

export interface MountRow {
  volume: string;
  path: string;
  readOnly: boolean;
}

export const mountRows = (ms: VolumeMount[] | undefined): MountRow[] => (ms ?? []).map((m) => ({ volume: m.volume, path: m.path, readOnly: m.readOnly ?? false }));

export function mountsOf(rows: MountRow[]): VolumeMount[] {
  return rows.map((r) => ({ volume: r.volume, path: r.path.trim(), ...(r.readOnly ? { readOnly: true } : {}) }));
}

/** Volume rows of an app or job: a filesystem volume, a path, read-only. */
export function MountEditor({ rows, onChange, volumes, errors }: { rows: MountRow[]; onChange: (rows: MountRow[]) => void; volumes: string[]; errors: Record<string, string> }) {
  const update = (i: number, patch: Partial<MountRow>) => onChange(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  return (
    <Fieldset legend="Volumes">
      <FieldHint>
        {volumes.length ? 'Folders that keep their files across deploys, shared by every replica. Changing them deploys a new revision.' : 'Create a filesystem volume first to keep files across deploys.'}
      </FieldHint>
      {rows.map((row, i) => {
        const f = `spec.volumes[${i}]`;
        const err = errors[`${f}.volume`] ?? errors[`${f}.path`];
        return (
          <FormRow key={i}>
            <Select aria-label={`Volume ${i + 1}`} value={row.volume} onChange={(e) => update(i, { volume: e.target.value })}>
              <option value="">Choose a volume…</option>
              {volumes.map((v) => (
                <option key={v} value={v}>{v}</option>
              ))}
            </Select>
            <Input aria-label={`Volume ${i + 1} path`} mono placeholder="/data" value={row.path} onChange={(e) => update(i, { path: e.target.value })} spellCheck={false} />
            <Checkbox label="Read-only" checked={row.readOnly} onChange={(e) => update(i, { readOnly: e.target.checked })} />
            <IconButton label={`Remove volume ${i + 1}`} icon={<Icons.X />} variant="ghost" onClick={() => onChange(rows.filter((_, j) => j !== i))} />
            {err && <FieldError>{err}</FieldError>}
          </FormRow>
        );
      })}
      {errors['spec.volumes'] && <FieldError>{errors['spec.volumes']}</FieldError>}
      {volumes.length > 0 && (
        <div>
          <Button variant="ghost" size="sm" icon={<Icons.Plus />} onClick={() => onChange([...rows, { volume: '', path: '', readOnly: false }])}>Add volume</Button>
        </div>
      )}
    </Fieldset>
  );
}

/** Disk volumes a VM may attach: free ones, and those already attached to this VM (VOL-01). */
export function freeDisks(volumes: Resource[], vms: Resource[], self?: string): { name: string; takenBy?: string }[] {
  return volumes
    .filter((v) => (v.spec as VolumeSpec).type === 'disk')
    .map((v) => ({ name: v.name, takenBy: vms.find((m) => m.name !== self && ((m.spec as VMSpec).disks ?? []).includes(v.name))?.name }));
}

/** A VM's disks as a checklist; a disk another VM has is listed but can't be picked (L2.5-002 §12). */
export function DiskPicker({ disks, selected, onChange, error }: { disks: { name: string; takenBy?: string }[]; selected: string[]; onChange: (s: string[]) => void; error?: string }) {
  return (
    <Fieldset legend="Disks">
      <FieldHint>{disks.length ? 'Disk volumes attached as extra drives: /dev/disk/by-id/virtio-<name>. Changing them restarts the VM.' : 'No disk volumes yet. Create one to give the VM an extra drive.'}</FieldHint>
      {disks.map((d) => (
        <Checkbox key={d.name} label={d.takenBy ? `${d.name} — attached to ${d.takenBy}` : d.name} disabled={Boolean(d.takenBy)} checked={selected.includes(d.name)}
          onChange={(e) => onChange(e.target.checked ? [...selected, d.name] : selected.filter((s) => s !== d.name))} />
      ))}
      {error && <FieldError>{error}</FieldError>}
    </Fieldset>
  );
}

/** Size presets that fill CPUs and memory, which stay editable (L2.5-002 §12). */
export const VM_SIZES = [
  { id: 'small', title: 'Small', cpus: 1, memoryMiB: 1024 },
  { id: 'medium', title: 'Medium', cpus: 2, memoryMiB: 2048 },
  { id: 'large', title: 'Large', cpus: 4, memoryMiB: 8192 },
] as const;

/** The image catalog in words (IMG-01). */
export const IMAGE_TITLES: Record<string, string> = { 'ubuntu-24.04': 'Ubuntu 24.04', 'debian-12': 'Debian 12' };
