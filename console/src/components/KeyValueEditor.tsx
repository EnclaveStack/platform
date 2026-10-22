import { Button, FieldError, FieldHint, Fieldset, Icons, IconButton, Input, KeyValueRow, Select } from '@enclavestack/ui';

export interface Row {
  key: string;
  value: string;
}

interface Props {
  idPrefix: string;
  label: string;
  rows: Row[];
  onChange: (rows: Row[]) => void;
  /** When set, values are chosen from these options (secret names). */
  options?: string[];
  errors: Record<string, string>;
  errorField: (key: string) => string;
  addLabel: string;
  emptyHint: string;
}

/** Environment variables editor: one row per KEY → value (L2.5-002 §1). */
export function KeyValueEditor({ idPrefix, label, rows, onChange, options, errors, errorField, addLabel, emptyHint }: Props) {
  const update = (i: number, patch: Partial<Row>) => onChange(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  return (
    <Fieldset legend={label}>
      {rows.length === 0 && <FieldHint>{emptyHint}</FieldHint>}
      {rows.map((row, i) => {
        const err = row.key ? errors[errorField(row.key)] : undefined;
        return (
          <KeyValueRow key={i}>
            <Input aria-label={`${label} name ${i + 1}`} id={`${idPrefix}-key-${i}`} mono placeholder="NAME" value={row.key}
              onChange={(e) => update(i, { key: e.target.value })} spellCheck={false} />
            {options ? (
              <Select aria-label={`${label} secret ${i + 1}`} value={row.value} onChange={(e) => update(i, { value: e.target.value })}>
                <option value="">Choose a secret…</option>
                {options.map((o) => (
                  <option key={o} value={o}>{o}</option>
                ))}
              </Select>
            ) : (
              <Input aria-label={`${label} value ${i + 1}`} mono placeholder="value" value={row.value} onChange={(e) => update(i, { value: e.target.value })} spellCheck={false} />
            )}
            <IconButton label={`Remove ${row.key || 'row ' + (i + 1)}`} icon={<Icons.X />} variant="ghost" onClick={() => onChange(rows.filter((_, j) => j !== i))} />
            {err && <FieldError>{err}</FieldError>}
          </KeyValueRow>
        );
      })}
      <div>
        <Button variant="ghost" size="sm" icon={<Icons.Plus />} onClick={() => onChange([...rows, { key: '', value: '' }])}>{addLabel}</Button>
      </div>
    </Fieldset>
  );
}

export function toRows(m: Record<string, string> | undefined): Row[] {
  return Object.entries(m ?? {}).map(([key, value]) => ({ key, value }));
}

export function fromRows(rows: Row[]): Record<string, string> | undefined {
  const out: Record<string, string> = {};
  for (const r of rows) if (r.key.trim()) out[r.key.trim()] = r.value;
  return Object.keys(out).length ? out : undefined;
}
