import { Button, Checkbox, Choice, Field, Fieldset, FormRow, Inline, Input, Select, Stack, Text } from '@enclavestack/ui';
import { useState, type ReactNode } from 'react';
import { RULES } from '../api/generated';
import type { CDNSpec, EmailSpec, Kind, Preset, QueueSpec, RelayTls, Resource, SearchSpec, Subscription, TlsMode, TopicSpec, TranscoderSpec } from '../api/types';
import type { FieldErrors } from '../lib/validation';
import { normalizePrefix } from '../lib/validation';
import { PlacementFields } from './NetworkingFields';

// Create and edit forms of the v0.6 kinds (L2-001 CON-38 – CON-43, L2.5-002 §13): queues, topics,
// search, transcoders, CDNs and email. ResourceForm owns the name, submission and errors; this owns the
// kinds' own fields and builds their specs.

export const DATA_KINDS: readonly Kind[] = ['queue', 'topic', 'search', 'transcoder', 'cdn', 'email'];

/** Spec fields that can't change in place (CP-04): shown read-only and never sent. */
export const DATA_IMMUTABLE: Partial<Record<Kind, string[]>> = { search: ['version'], cdn: ['hostname'] };

/** What the forms pick from. */
export interface DataContext {
  live: (k: Kind) => Resource[];
  editing: boolean;
  errors: FieldErrors;
  onBlur: () => void;
}

const PRESET_WORDS: Record<Preset, string> = {
  'mp4-480p': 'MP4, 480p (H.264, AAC)',
  'mp4-720p': 'MP4, 720p (H.264, AAC)',
  'mp4-1080p': 'MP4, 1080p (H.264, AAC)',
  'webm-720p': 'WebM, 720p (VP9, Opus)',
  'hls-720p': 'HLS stream, 720p (a playlist and segments)',
  mp3: 'MP3 audio only',
  thumbnail: 'A JPEG thumbnail',
};

interface SubRow {
  type: 'queue' | 'url';
  queue: string;
  url: string;
  secret: string;
}

const num = (s: string) => (s.trim() === '' ? undefined : Number(s));

/** The v0.6 forms' state and fields; `build` returns the spec to send. */
export function useDataForm(kind: Kind, existing?: Resource) {
  const spec = (existing && DATA_KINDS.includes(kind) ? existing.spec : {}) as Record<string, unknown>;
  const q = spec as QueueSpec;
  const t = spec as TopicSpec;
  const se = spec as SearchSpec;
  const tc = spec as Partial<TranscoderSpec>;
  const c = spec as Partial<CDNSpec>;
  const em = spec as EmailSpec;

  const [visibility, setVisibility] = useState(String(q.visibilityTimeoutSeconds ?? RULES.queueVisibilityDefault));
  const [retention, setRetention] = useState(String((kind === 'topic' ? t.retentionHours : q.retentionHours) ?? (kind === 'topic' ? RULES.topicRetentionDefault : RULES.queueRetentionDefault)));
  const [maxBytes, setMaxBytes] = useState(String(Math.round((q.maxMessageBytes ?? RULES.messageBytesDefault) / 1024)));
  const [maxReceives, setMaxReceives] = useState(String(q.maxReceives ?? 0));
  const [dlq, setDlq] = useState(q.deadLetterQueue ?? '');
  const [subs, setSubs] = useState<SubRow[]>((t.subscriptions ?? []).map((s) => ({ type: s.queue ? 'queue' : 'url', queue: s.queue ?? '', url: s.url ?? '', secret: s.secret ?? '' })));
  const [searchVersion, setSearchVersion] = useState<'2' | '3'>(se.version ?? '3');
  const [searchMemory, setSearchMemory] = useState(String(se.memoryMiB ?? RULES.searchMemoryDefault));
  const placed = spec as { network?: string; subnet?: string };
  const [network, setNetwork] = useState(placed.network ?? '');
  const [subnet, setSubnet] = useState(placed.subnet ?? '');
  const [inBucket, setInBucket] = useState(tc.input?.bucket ?? '');
  const [inPrefix, setInPrefix] = useState(tc.input?.prefix ?? '');
  const [outBucket, setOutBucket] = useState(tc.output?.bucket ?? '');
  const [outPrefix, setOutPrefix] = useState(tc.output?.prefix ?? RULES.transcodeOutputDefault);
  const [presets, setPresets] = useState<Preset[]>(tc.presets ?? ['mp4-720p', 'thumbnail']);
  const [watch, setWatch] = useState(tc.watch !== false);
  const [tcTimeout, setTcTimeout] = useState(String(tc.timeoutSeconds ?? RULES.transcodeTimeoutDefault));
  const [concurrency, setConcurrency] = useState(String(tc.concurrency ?? RULES.concurrencyDefault));
  const [tcMemory, setTcMemory] = useState(String(tc.memoryMiB ?? RULES.transcodeMemoryDefault));
  const [hostname, setHostname] = useState(c.hostname ?? '');
  const [originKind, setOriginKind] = useState<'app' | 'bucket'>(c.origin?.startsWith('bucket/') ? 'bucket' : 'app');
  const [originName, setOriginName] = useState(c.origin?.split('/')[1] ?? '');
  const [originPort, setOriginPort] = useState(c.originPort ? String(c.originPort) : '');
  const [ttl, setTtl] = useState(String(c.defaultTtlSeconds ?? RULES.cdnTtlDefault));
  const [cacheSize, setCacheSize] = useState(String(c.maxSizeMiB ?? RULES.cdnSizeDefault));
  const [tls, setTls] = useState<TlsMode | ''>(existing ? (c.tls ?? '') : '');
  const [zone, setZone] = useState(c.zone ?? '');
  const [mode, setMode] = useState<'capture' | 'relay'>(em.mode ?? 'capture');
  const [relayHost, setRelayHost] = useState(em.relay?.host ?? '');
  const [relayPort, setRelayPort] = useState(String(em.relay?.port ?? RULES.relayPortDefault));
  const [relayTls, setRelayTls] = useState<RelayTls>(em.relay?.tls ?? 'starttls');
  const [relayCreds, setRelayCreds] = useState(em.relay?.credentialsSecret ?? '');
  const [domain, setDomain] = useState(em.domain ?? '');
  const [keepMessages, setKeepMessages] = useState(String(em.keepMessages ?? RULES.keepMessagesDefault));

  const withPlacement = <T extends object>(o: T): T => ({ ...o, ...(network ? { network } : {}), ...(network && subnet ? { subnet } : {}) });

  function build(): Record<string, unknown> {
    switch (kind) {
      case 'queue': {
        const s: QueueSpec = { visibilityTimeoutSeconds: num(visibility), retentionHours: num(retention), maxMessageBytes: (num(maxBytes) ?? 0) * 1024, maxReceives: num(maxReceives) ?? 0 };
        if (dlq) s.deadLetterQueue = dlq;
        return s as Record<string, unknown>;
      }
      case 'topic': {
        const list: Subscription[] = subs.map((r) => (r.type === 'queue' ? { queue: r.queue } : { url: r.url.trim(), ...(r.secret ? { secret: r.secret } : {}) }));
        return { retentionHours: num(retention), subscriptions: list };
      }
      case 'search':
        return withPlacement({ version: searchVersion, memoryMiB: num(searchMemory) });
      case 'transcoder':
        return { input: { bucket: inBucket, prefix: normalizePrefix(inPrefix) }, output: { bucket: outBucket, prefix: normalizePrefix(outPrefix) }, presets, watch,
          timeoutSeconds: num(tcTimeout), concurrency: num(concurrency), memoryMiB: num(tcMemory) };
      case 'cdn': {
        const s: CDNSpec = { hostname: hostname.trim().toLowerCase(), origin: originName ? `${originKind}/${originName}` : '', defaultTtlSeconds: num(ttl), maxSizeMiB: num(cacheSize) };
        if (originKind === 'app' && originPort.trim()) s.originPort = Number(originPort);
        if (zone) {
          s.zone = zone;
          s.tls = 'auto';
        } else if (tls) s.tls = tls;
        return s as unknown as Record<string, unknown>;
      }
      case 'email': {
        const s: EmailSpec = { mode, keepMessages: num(keepMessages) };
        if (mode === 'relay') s.relay = { host: relayHost.trim(), port: num(relayPort), tls: relayTls, credentialsSecret: relayCreds };
        if (domain.trim()) s.domain = domain.trim().toLowerCase();
        return withPlacement(s) as Record<string, unknown>;
      }
    }
    return {};
  }

  function render(ctx: DataContext): ReactNode {
    const { live, editing, errors, onBlur } = ctx;
    const err = (f: string) => errors[f];
    const names = (k: Kind) => live(k).map((r) => r.name);
    const networks = live('network') as Resource<'network'>[];
    const secretNames = names('secret');
    const placement = <PlacementFields networks={networks} network={network} subnet={subnet} errors={errors} onChange={(n, sub) => { setNetwork(n); setSubnet(sub); }} />;
    const numberField = (id: string, label: string, value: string, set: (v: string) => void, field: string, hint?: string) => (
      <Field id={id} label={label} hint={hint} error={err(field)}>
        <Input id={id} inputMode="numeric" value={value} onChange={(e) => set(e.target.value)} onBlur={onBlur} aria-invalid={Boolean(err(field))} />
      </Field>
    );
    switch (kind) {
      case 'queue':
        return (
          <>
            <Fieldset legend="Delivery">
              <FormRow>
                {numberField('f-visibility', 'Visibility timeout (s)', visibility, setVisibility, 'spec.visibilityTimeoutSeconds',
                  'How long a received message stays hidden from other workers before it is delivered again.')}
                {numberField('f-max-receives', 'Maximum receives', maxReceives, setMaxReceives, 'spec.maxReceives', '0: no limit.')}
              </FormRow>
              {Number(maxReceives) > 0 && (
                <Field id="f-dlq" label="Dead-letter queue" hint="Where a message goes once it was received that many times without being deleted." error={err('spec.deadLetterQueue')}>
                  <Select id="f-dlq" value={dlq} onChange={(e) => setDlq(e.target.value)}>
                    <option value="">None: keep it until its retention ends</option>
                    {names('queue').filter((n) => n !== existing?.name).map((n) => (
                      <option key={n} value={n}>{n}</option>
                    ))}
                  </Select>
                </Field>
              )}
            </Fieldset>
            <Fieldset legend="Keeping">
              <FormRow>
                {numberField('f-retention', 'Retention (hours)', retention, setRetention, 'spec.retentionHours', `${RULES.retentionHoursMin}–${RULES.retentionHoursMax}.`)}
                {numberField('f-max-bytes', 'Largest message (KiB)', maxBytes, setMaxBytes, 'spec.maxMessageBytes', '1–1024.')}
              </FormRow>
            </Fieldset>
          </>
        );
      case 'topic':
        return (
          <>
            {numberField('f-retention', 'Retention (hours)', retention, setRetention, 'spec.retentionHours', 'How long messages are kept, so a failing subscription can catch up.')}
            <Fieldset legend="Subscriptions">
              <Stack gap={2}>
                {subs.length === 0 && <Text size="sm" tone="muted">No subscriptions yet: messages reach only apps listening live on the topic.</Text>}
                {subs.map((row, i) => {
                  const f = `spec.subscriptions[${i}]`;
                  const update = (patch: Partial<SubRow>) => setSubs(subs.map((x, j) => (j === i ? { ...x, ...patch } : x)));
                  return (
                    <FormRow key={i}>
                      <Field id={`f-sub-type-${i}`} label="Deliver to" error={err(f)}>
                        <Select id={`f-sub-type-${i}`} value={row.type} onChange={(e) => update({ type: e.target.value as SubRow['type'] })}>
                          <option value="queue">A queue</option>
                          <option value="url">A webhook</option>
                        </Select>
                      </Field>
                      {row.type === 'queue' ? (
                        <Field id={`f-sub-queue-${i}`} label="Queue" error={err(`${f}.queue`)}>
                          <Select id={`f-sub-queue-${i}`} value={row.queue} onChange={(e) => update({ queue: e.target.value })}>
                            <option value="">Choose a queue…</option>
                            {names('queue').map((n) => (
                              <option key={n} value={n}>{n}</option>
                            ))}
                          </Select>
                        </Field>
                      ) : (
                        <>
                          <Field id={`f-sub-url-${i}`} label="URL" hint="https://, or http:// to localhost." error={err(`${f}.url`)}>
                            <Input id={`f-sub-url-${i}`} mono value={row.url} onChange={(e) => update({ url: e.target.value })} onBlur={onBlur} placeholder="https://hooks.example.com/orders" spellCheck={false} />
                          </Field>
                          <Field id={`f-sub-secret-${i}`} label="Signing secret" error={err(`${f}.secret`)}>
                            <Select id={`f-sub-secret-${i}`} value={row.secret} onChange={(e) => update({ secret: e.target.value })}>
                              <option value="">Don't sign</option>
                              {secretNames.map((n) => (
                                <option key={n} value={n}>{n}</option>
                              ))}
                            </Select>
                          </Field>
                        </>
                      )}
                      <Inline align="end">
                        <Button size="sm" variant="ghost" onClick={() => setSubs(subs.filter((_, j) => j !== i))}>Remove</Button>
                      </Inline>
                    </FormRow>
                  );
                })}
                {subs.length < RULES.subscriptionsMax && (
                  <Inline>
                    <Button size="sm" onClick={() => setSubs([...subs, { type: 'queue', queue: '', url: '', secret: '' }])}>Add subscription</Button>
                  </Inline>
                )}
              </Stack>
            </Fieldset>
          </>
        );
      case 'search':
        return (
          <>
            <FormRow>
              <Field id="f-search-version" label="OpenSearch version" hint={editing ? 'Changing the version needs a new search service.' : undefined} error={err('spec.version')}>
                <Select id="f-search-version" value={searchVersion} onChange={(e) => setSearchVersion(e.target.value as '2' | '3')} disabled={editing}>
                  <option value="3">3</option>
                  <option value="2">2</option>
                </Select>
              </Field>
              {numberField('f-search-memory', 'Memory (MiB)', searchMemory, setSearchMemory, 'spec.memoryMiB',
                'Half goes to the search engine’s heap; 2048 MiB suits a few million documents. A change restarts it.')}
            </FormRow>
            {placement}
          </>
        );
      case 'transcoder': {
        const buckets = names('bucket');
        const bucketSelect = (id: string, value: string, set: (v: string) => void, field: string) => (
          <Field id={id} label="Bucket" error={err(field)}>
            <Select id={id} value={value} onChange={(e) => set(e.target.value)} aria-invalid={Boolean(err(field))}>
              <option value="">Choose a bucket…</option>
              {buckets.map((n) => (
                <option key={n} value={n}>{n}</option>
              ))}
            </Select>
          </Field>
        );
        return (
          <>
            <Fieldset legend="Input">
              <FormRow>
                {bucketSelect('f-in-bucket', inBucket, setInBucket, 'spec.input.bucket')}
                <Field id="f-in-prefix" label="Folder" hint="Empty for the whole bucket." error={err('spec.input.prefix')}>
                  <Input id="f-in-prefix" mono value={inPrefix} onChange={(e) => setInPrefix(e.target.value)} onBlur={onBlur} placeholder="videos/" spellCheck={false} />
                </Field>
              </FormRow>
            </Fieldset>
            <Fieldset legend="Output">
              <FormRow>
                {bucketSelect('f-out-bucket', outBucket, setOutBucket, 'spec.output.bucket')}
                <Field id="f-out-prefix" label="Folder" error={err('spec.output.prefix')}>
                  <Input id="f-out-prefix" mono value={outPrefix} onChange={(e) => setOutPrefix(e.target.value)} onBlur={onBlur} spellCheck={false} />
                </Field>
              </FormRow>
              {inBucket && outBucket && (
                <Text size="sm" tone="muted">
                  Files in {inBucket}/{normalizePrefix(inPrefix)} → {outBucket}/{normalizePrefix(outPrefix)}&lt;name&gt;/&lt;preset&gt;.&lt;ext&gt;
                </Text>
              )}
            </Fieldset>
            <Fieldset legend="Presets">
              {err('spec.presets') && <Text size="sm" tone="bad">{err('spec.presets')}</Text>}
              {(RULES.presets as readonly Preset[]).map((p) => (
                <Checkbox key={p} label={`${p} — ${PRESET_WORDS[p]}`} checked={presets.includes(p)}
                  onChange={(e) => setPresets(e.target.checked ? [...presets, p] : presets.filter((x) => x !== p))} />
              ))}
            </Fieldset>
            <Checkbox label="Convert new files automatically (files already there aren't converted)" checked={watch} onChange={(e) => setWatch(e.target.checked)} />
            <FormRow>
              {numberField('f-tc-timeout', 'Timeout per file (s)', tcTimeout, setTcTimeout, 'spec.timeoutSeconds')}
              {numberField('f-tc-concurrency', 'Files at once', concurrency, setConcurrency, 'spec.concurrency', `${RULES.concurrencyMin}–${RULES.concurrencyMax}.`)}
              {numberField('f-tc-memory', 'Memory per file (MiB)', tcMemory, setTcMemory, 'spec.memoryMiB')}
            </FormRow>
          </>
        );
      }
      case 'cdn':
        return (
          <>
            {editing ? (
              <Field id="f-cdn-hostname" label="Hostname" hint="The hostname can't change. Create a new CDN instead.">
                <Input id="f-cdn-hostname" mono value={hostname} readOnly aria-readonly="true" />
              </Field>
            ) : (
              <Field id="f-cdn-hostname" label="Hostname" hint="Like cdn.example.com, or assets.localhost on this machine." error={err('spec.hostname')}>
                <Input id="f-cdn-hostname" mono value={hostname} onChange={(e) => setHostname(e.target.value)} onBlur={onBlur} spellCheck={false} aria-invalid={Boolean(err('spec.hostname'))} />
              </Field>
            )}
            <Fieldset legend="Origin">
              <Choice name="cdn-origin" value="app" checked={originKind === 'app'} onChange={() => { setOriginKind('app'); setOriginName(''); }}
                title="An app" description="Cache what an app serves; the app's firewall lets the cache in." />
              <Choice name="cdn-origin" value="bucket" checked={originKind === 'bucket'} onChange={() => { setOriginKind('bucket'); setOriginName(''); }}
                title="A bucket" description="Serve a bucket's files by key. They become readable by anyone who can reach the CDN." />
              <FormRow>
                <Field id="f-cdn-origin" label={originKind === 'app' ? 'App' : 'Bucket'} error={err('spec.origin')}>
                  <Select id="f-cdn-origin" value={originName} onChange={(e) => setOriginName(e.target.value)} aria-invalid={Boolean(err('spec.origin'))}>
                    <option value="">Choose {originKind === 'app' ? 'an app' : 'a bucket'}…</option>
                    {names(originKind).map((n) => (
                      <option key={n} value={n}>{n}</option>
                    ))}
                  </Select>
                </Field>
                {originKind === 'app' && (
                  <Field id="f-cdn-port" label="Port" hint="Empty: the app's port." error={err('spec.originPort')}>
                    <Input id="f-cdn-port" inputMode="numeric" value={originPort} onChange={(e) => setOriginPort(e.target.value)} onBlur={onBlur} />
                  </Field>
                )}
              </FormRow>
            </Fieldset>
            <Fieldset legend="Caching">
              <FormRow>
                {numberField('f-cdn-ttl', 'Default cache time (s)', ttl, setTtl, 'spec.defaultTtlSeconds', "Used when the origin doesn't say; 0 caches only what it allows.")}
                {numberField('f-cdn-size', 'Cache size (MiB)', cacheSize, setCacheSize, 'spec.maxSizeMiB')}
              </FormRow>
            </Fieldset>
            <FormRow>
              <Field id="f-cdn-zone" label="Get the certificate through DNS" hint="Optional: a DNS zone, so no inbound port is needed." error={err('spec.zone')}>
                <Select id="f-cdn-zone" value={zone} onChange={(e) => setZone(e.target.value)}>
                  <option value="">No: through the edge (HTTP-01) or internal</option>
                  {names('zone').map((n) => (
                    <option key={n} value={n}>{n}</option>
                  ))}
                </Select>
              </Field>
              {!zone && (
                <Field id="f-cdn-tls" label="HTTPS" error={err('spec.tls')}>
                  <Select id="f-cdn-tls" value={tls} onChange={(e) => setTls(e.target.value as TlsMode | '')}>
                    <option value="">Automatic (internal for .localhost)</option>
                    <option value="auto">Public certificate</option>
                    <option value="internal">EnclaveStack's own certificate</option>
                    <option value="off">Off (HTTP only)</option>
                  </Select>
                </Field>
              )}
            </FormRow>
          </>
        );
      case 'email':
        return (
          <>
            <Fieldset legend="Mode">
              <Choice name="email-mode" value="capture" checked={mode === 'capture'} onChange={() => setMode('capture')}
                title="Catch every message" description="Messages are kept here to look at, and never delivered. For testing." />
              <Choice name="email-mode" value="relay" checked={mode === 'relay'} onChange={() => setMode('relay')}
                title="Relay through my provider" description="Messages are delivered through your provider's SMTP (SES, Postmark, Mailgun…) and kept here too." />
            </Fieldset>
            {mode === 'relay' && (
              <Fieldset legend="Relay">
                <FormRow>
                  <Field id="f-relay-host" label="SMTP host" error={err('spec.relay.host')}>
                    <Input id="f-relay-host" mono value={relayHost} onChange={(e) => setRelayHost(e.target.value)} onBlur={onBlur} placeholder="smtp.postmarkapp.com" spellCheck={false} />
                  </Field>
                  {numberField('f-relay-port', 'Port', relayPort, setRelayPort, 'spec.relay.port')}
                  <Field id="f-relay-tls" label="Encryption" error={err('spec.relay.tls')}>
                    <Select id="f-relay-tls" value={relayTls} onChange={(e) => setRelayTls(e.target.value as RelayTls)}>
                      <option value="starttls">STARTTLS</option>
                      <option value="tls">TLS</option>
                      <option value="none">None</option>
                    </Select>
                  </Field>
                </FormRow>
                <Field id="f-relay-creds" label="Credentials" hint={'A secret holding {"username": "…", "password": "…"}.'} error={err('spec.relay.credentialsSecret') ?? err('spec.relay')}>
                  <Select id="f-relay-creds" value={relayCreds} onChange={(e) => setRelayCreds(e.target.value)}>
                    <option value="">Choose a secret…</option>
                    {secretNames.map((n) => (
                      <option key={n} value={n}>{n}</option>
                    ))}
                  </Select>
                </Field>
              </Fieldset>
            )}
            <FormRow>
              <Field id="f-email-domain" label="Sender domain" hint="Optional: the API sends only from this domain." error={err('spec.domain')}>
                <Input id="f-email-domain" mono value={domain} onChange={(e) => setDomain(e.target.value)} onBlur={onBlur} placeholder="example.com" spellCheck={false} />
              </Field>
              {numberField('f-keep', 'Messages kept', keepMessages, setKeepMessages, 'spec.keepMessages', `${RULES.keepMessagesMin}–${RULES.keepMessagesMax}.`)}
            </FormRow>
            {placement}
          </>
        );
    }
    return null;
  }

  return { build, render };
}
