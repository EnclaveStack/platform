import { Button, Field, Fieldset, FormRow, Inline, Input, Select, Stack, Text } from '@enclavestack/ui';
import { RULES } from '../api/generated';
import type { FunctionTrigger } from '../api/types';
import { describeSchedule } from '../lib/schedule';
import type { FieldErrors } from '../lib/validation';

// A function's triggers (L2-016 CON-82, L2.5-002 §23): a schedule, a queue or a bucket folder each.
// Topics send to a function from the topic's subscriptions, and calls come through Invoke.

export type TriggerType = 'schedule' | 'queue' | 'bucket';

export interface TriggerRow {
  type: TriggerType;
  /** The schedule, the queue or the bucket, by type. */
  value: string;
  /** A schedule's time zone, or a bucket's folder. */
  extra: string;
}

export function triggerRows(ts: FunctionTrigger[] | undefined): TriggerRow[] {
  return (ts ?? []).map((t): TriggerRow => (t.schedule ? { type: 'schedule', value: t.schedule, extra: t.timeZone ?? '' }
    : t.queue ? { type: 'queue', value: t.queue, extra: '' } : { type: 'bucket', value: t.bucket ?? '', extra: t.prefix ?? '' }));
}

export function triggersOf(rows: TriggerRow[]): FunctionTrigger[] {
  return rows.map((r): FunctionTrigger => {
    const value = r.value.trim();
    const extra = r.extra.trim();
    if (r.type === 'schedule') return { schedule: value, ...(extra ? { timeZone: extra } : {}) };
    if (r.type === 'queue') return { queue: value };
    return { bucket: value, ...(extra ? { prefix: extra } : {}) };
  });
}

const WORDS: Record<TriggerType, string> = { schedule: 'A schedule', queue: 'Messages on a queue', bucket: 'New files in a bucket' };

interface Props {
  rows: TriggerRow[];
  onChange: (rows: TriggerRow[]) => void;
  queues: string[];
  buckets: string[];
  errors: FieldErrors;
  onBlur: () => void;
}

export function TriggerEditor({ rows, onChange, queues, buckets, errors, onBlur }: Props) {
  const update = (i: number, patch: Partial<TriggerRow>) => onChange(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  const hasSchedule = rows.some((r) => r.type === 'schedule');
  return (
    <Fieldset legend="Triggers">
      <Stack gap={2}>
        <Text size="sm" tone="muted">
          Besides these, a function runs when you invoke it, and for each message of a topic that subscribes it.
        </Text>
        {rows.map((row, i) => {
          const f = `spec.triggers[${i}]`;
          return (
            <FormRow key={i}>
              <Field id={`f-trig-type-${i}`} label="Run on" error={errors[f]}>
                <Select id={`f-trig-type-${i}`} value={row.type} onChange={(e) => update(i, { type: e.target.value as TriggerType, value: '', extra: '' })}>
                  {(Object.keys(WORDS) as TriggerType[]).filter((t) => t !== 'schedule' || row.type === 'schedule' || !hasSchedule).map((t) => (
                    <option key={t} value={t}>{WORDS[t]}</option>
                  ))}
                </Select>
              </Field>
              {row.type === 'schedule' && (
                <>
                  <Field id={`f-trig-sched-${i}`} label="Schedule" error={errors[`${f}.schedule`]}
                    hint={row.value.trim() ? describeSchedule(row.value, row.extra.trim() || undefined) : 'Like */5 * * * * (every 5 minutes) or @hourly.'}>
                    <Input id={`f-trig-sched-${i}`} mono value={row.value} onChange={(e) => update(i, { value: e.target.value })} onBlur={onBlur} placeholder="@hourly" spellCheck={false} />
                  </Field>
                  <Field id={`f-trig-tz-${i}`} label="Time zone" hint="IANA name. Empty: the machine's." error={errors[`${f}.timeZone`]}>
                    <Input id={`f-trig-tz-${i}`} value={row.extra} onChange={(e) => update(i, { extra: e.target.value })} onBlur={onBlur} placeholder="Europe/Berlin" spellCheck={false} />
                  </Field>
                </>
              )}
              {row.type === 'queue' && (
                <Field id={`f-trig-queue-${i}`} label="Queue" hint="Each message starts a run; it is deleted when the run succeeds." error={errors[`${f}.queue`]}>
                  <Select id={`f-trig-queue-${i}`} value={row.value} onChange={(e) => update(i, { value: e.target.value })}>
                    <option value="">Choose a queue…</option>
                    {queues.map((n) => (
                      <option key={n} value={n}>{n}</option>
                    ))}
                  </Select>
                </Field>
              )}
              {row.type === 'bucket' && (
                <>
                  <Field id={`f-trig-bucket-${i}`} label="Bucket" hint="Each new file starts a run." error={errors[`${f}.bucket`]}>
                    <Select id={`f-trig-bucket-${i}`} value={row.value} onChange={(e) => update(i, { value: e.target.value })}>
                      <option value="">Choose a bucket…</option>
                      {buckets.map((n) => (
                        <option key={n} value={n}>{n}</option>
                      ))}
                    </Select>
                  </Field>
                  <Field id={`f-trig-prefix-${i}`} label="Folder" hint="Only files in this folder, like uploads/. Empty: the whole bucket." error={errors[`${f}.prefix`]}>
                    <Input id={`f-trig-prefix-${i}`} mono value={row.extra} onChange={(e) => update(i, { extra: e.target.value })} onBlur={onBlur} placeholder="uploads/" spellCheck={false} />
                  </Field>
                </>
              )}
              <Inline>
                <Button size="sm" variant="ghost" onClick={() => onChange(rows.filter((_, j) => j !== i))}>Remove</Button>
              </Inline>
            </FormRow>
          );
        })}
        {errors['spec.triggers'] && <Text size="sm" tone="bad" role="alert">{errors['spec.triggers']}</Text>}
        {rows.length < RULES.functionTriggersMax && (
          <Inline>
            <Button size="sm" onClick={() => onChange([...rows, { type: hasSchedule ? 'queue' : 'schedule', value: '', extra: '' }])}>Add trigger</Button>
          </Inline>
        )}
      </Stack>
    </Fieldset>
  );
}
