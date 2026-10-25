import { Button, Card, FieldError, FieldHint, Fieldset, FormRow, Icons, IconButton, Row, Rows, Select, Stack, Text } from '@enclavestack/ui';
import { Link } from 'react-router';
import { useApi } from '../api/context';
import { RULES } from '../api/generated';
import type { AppSpec, BucketAccessLevel, BucketGrant, JobSpec, Resource } from '../api/types';
import { useLoad } from '../lib/useLoad';

// Bucket access of apps and jobs (L2-011 CON-70): which buckets they reach with keys of their own, and how.

export interface GrantRow {
  bucket: string;
  access: BucketAccessLevel;
}

export const grantRows = (gs: BucketGrant[] | undefined): GrantRow[] => (gs ?? []).map((g) => ({ bucket: g.bucket, access: g.access }));

export const grantsOf = (rows: GrantRow[]): BucketGrant[] => rows.map((r) => ({ bucket: r.bucket, access: r.access }));

/** What each level means, as the editor and the resource page say it. */
export const ACCESS_LABEL: Record<BucketAccessLevel, string> = { read: 'Read', readwrite: 'Read and write' };

/** The variables the app or job receives, named once for every place that shows them. */
export const ACCESS_VARIABLES = RULES.bucketAccessEnv.join(', ');

/** Bucket rows of an app or a job from an image: a bucket of the project and a level. */
export function BucketAccessEditor({ rows, onChange, buckets, errors }: {
  rows: GrantRow[]; onChange: (rows: GrantRow[]) => void; buckets: string[]; errors: Record<string, string>;
}) {
  const update = (i: number, patch: Partial<GrantRow>) => onChange(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  return (
    <Fieldset legend="Bucket access">
      <FieldHint>
        {buckets.length
          ? `Buckets it reaches with keys of its own, given as ${ACCESS_VARIABLES}. S3 clients use them unchanged (with path-style addressing). It reaches no other bucket.`
          : 'Create a bucket first to give it access.'}
      </FieldHint>
      {rows.map((row, i) => {
        const f = `spec.bucketAccess[${i}]`;
        const err = errors[`${f}.bucket`] ?? errors[`${f}.access`];
        return (
          <FormRow key={i}>
            <Select aria-label={`Bucket ${i + 1}`} value={row.bucket} onChange={(e) => update(i, { bucket: e.target.value })}>
              <option value="">Choose a bucket…</option>
              {buckets.map((b) => (
                <option key={b} value={b}>{b}</option>
              ))}
            </Select>
            <Select aria-label={`Bucket ${i + 1} access`} value={row.access} onChange={(e) => update(i, { access: e.target.value as BucketAccessLevel })}>
              {RULES.bucketAccessLevels.map((l) => (
                <option key={l} value={l}>{ACCESS_LABEL[l]}</option>
              ))}
            </Select>
            <IconButton label={`Remove bucket ${i + 1}`} icon={<Icons.X />} variant="ghost" onClick={() => onChange(rows.filter((_, j) => j !== i))} />
            {err && <FieldError>{err}</FieldError>}
          </FormRow>
        );
      })}
      {errors['spec.bucketAccess'] && <FieldError>{errors['spec.bucketAccess']}</FieldError>}
      {buckets.length > 0 && rows.length < RULES.bucketAccessMax && (
        <div>
          <Button variant="ghost" size="sm" icon={<Icons.Plus />} onClick={() => onChange([...rows, { bucket: '', access: 'read' }])}>Add bucket</Button>
        </div>
      )}
    </Fieldset>
  );
}

/** An app's or job's bucket access on its page (L2-011 CON-71): the buckets and levels, its keys and the variables. */
export function BucketAccessCard({ r }: { r: Resource }) {
  const api = useApi();
  const spec = r.spec as AppSpec | JobSpec;
  const viaApp = r.kind === 'job' ? (spec as JobSpec).app : undefined;
  const grants = spec.bucketAccess ?? [];
  const secretName = typeof r.outputs.bucketCredentialsSecret === 'string' ? r.outputs.bucketCredentialsSecret : undefined;
  const secret = useLoad(`bucket-keys:${r.id}:${secretName ?? ''}`, async () =>
    (secretName ? (await api.listResources('secret', r.project)).find((s) => s.name === secretName) ?? null : null));
  // Keyed by the grants, so a change of access loads the buckets it names.
  const buckets = useLoad(`bucket-ids:${r.project}:${grants.map((g) => g.bucket).join(',')}`, () => (grants.length ? api.listResources('bucket', r.project) : Promise.resolve([])));
  if (!grants.length && !viaApp) return null;
  return (
    <Card as="section" title="Bucket access">
      {viaApp ? (
        <Text tone="muted">Runs as the app {viaApp}, so it uses that app's bucket access and keys.</Text>
      ) : (
        <Stack gap={3}>
          <Rows aria-label="Buckets it reaches">
            {grants.map((g) => {
              const b = (buckets.data ?? []).find((x) => x.name === g.bucket);
              return (
                <Row key={g.bucket}>
                  {b ? <Link to={`/buckets/${b.id}`}>{g.bucket}</Link> : <Text>{g.bucket}</Text>}
                  <Text tone="muted">{ACCESS_LABEL[g.access]}</Text>
                </Row>
              );
            })}
          </Rows>
          <Text size="sm" tone="muted">
            Its keys are in the secret {secret.data ? <Link to={`/secrets/${secret.data.id}`}>{secretName}</Link> : (secretName ?? 'made when it is next ready')}.
            Its containers get them as <Text mono size="sm">{ACCESS_VARIABLES}</Text>. It reaches no other bucket.
          </Text>
        </Stack>
      )}
    </Card>
  );
}
