import { Alert, Button, Checkbox, Choice, Field, Fieldset, FormRow, IconButton, Icons, Inline, Input, Select, Stack, Text } from '@enclavestack/ui';
import { useState, type ReactNode } from 'react';
import { KINDS, MACHINE_METRICS, METRICS, RULES } from '../api/generated';
import type {
  AlertCondition, AlertSpec, ChannelSpec, ChannelType, ConditionType, DashboardSpec, Kind, LogDestination, LogExportSpec, Resource, SecretStoreProvider, SecretStoreSpec, Widget,
} from '../api/types';
import { useApi } from '../api/context';
import { logDestinationType, MACHINE_REF, MACHINE_REF_PREFIX, metricOf } from '../lib/operate';
import { useLoad } from '../lib/useLoad';
import type { FieldErrors } from '../lib/validation';

// Create and edit forms of the v0.7 kinds (L2-001 CON-49 – CON-52, L2.5-002 §14): dashboards, alerts,
// channels, log exports and secret stores. ResourceForm owns the name, project, submission and errors;
// this owns the kinds' own fields and builds their specs.

export const OPERATE_KINDS: readonly Kind[] = ['dashboard', 'alert', 'channel', 'logexport', 'secretstore'];

/** Spec fields that can't change in place (CP-04): shown read-only and never sent. */
export const OPERATE_IMMUTABLE: Partial<Record<Kind, string[]>> = { channel: ['type'], secretstore: ['provider'] };

export interface OperateContext {
  /** Live resources of the form's project. */
  live: (k: Kind) => Resource[];
  editing: boolean;
  errors: FieldErrors;
  onBlur: () => void;
}

const num = (s: string) => (s.trim() === '' ? undefined : Number(s));
const CONDITION_WORDS: Record<ConditionType, [string, string]> = {
  metric: ['A metric crosses a line', 'CPU, memory, requests, queue depth, disk… above or below a threshold for some minutes.'],
  crash_loop: ['Keeps restarting', 'An app, database or other service restarts again and again.'],
  failed: ['Fails', 'Any resource whose state becomes Failed.'],
  run_failed: ['A run fails', 'A run of a job, backup or transcoder ends in failure.'],
  machine_offline: ['A machine goes offline', 'Its agent stops answering for some minutes (v0.8).'],
};
const CHANNEL_WORDS: Record<ChannelType, [string, string]> = {
  email: ['Email', 'Through an email service of this project.'],
  webhook: ['Webhook', 'JSON POSTed to your URL, signed if you give a secret.'],
  slack: ['Slack', 'An incoming webhook of a Slack channel.'],
  discord: ['Discord', 'A webhook of a Discord channel.'],
  telegram: ['Telegram', 'A bot posts to a chat.'],
};
const PROVIDER_WORDS: Record<SecretStoreProvider, [string, string]> = {
  vault: ['HashiCorp Vault or OpenBao', 'A KV v2 secrets engine, signed in with a token.'],
  onepassword: ['1Password', 'A 1Password Connect server and one of its vaults.'],
  aws: ['AWS Secrets Manager', 'Secrets of one region, signed in with an access key.'],
  doppler: ['Doppler', 'One config of a project, through a service token.'],
  infisical: ['Infisical', 'One environment of a project, through a machine identity.'],
};
type DestType = 'bucket' | 's3' | 'http' | 'otlp' | 'loki';
const DEST_WORDS: Record<DestType, [string, string]> = {
  bucket: ['A bucket here', 'Gzip files of JSON lines in a bucket of this project.'],
  s3: ['Any S3 bucket', 'Gzip files of JSON lines in AWS S3, R2, B2 or any S3-compatible storage.'],
  http: ['An HTTP endpoint', 'Batches of JSON lines POSTed to your URL.'],
  otlp: ['OpenTelemetry', 'The OTLP/HTTP logs endpoint of a collector or a service.'],
  loki: ['Grafana Loki', "Loki's push API, with a stream per resource."],
};

interface WidgetRow {
  title: string;
  metric: string;
  resources: string[];
}

/** The v0.7 forms' state and fields; `build` returns the spec to send. */
export function useOperateForm(kind: Kind, existing?: Resource) {
  const api = useApi();
  const machines = useLoad(`alert-machines:${kind}`, () => (kind === 'alert' ? api.listMachines() : Promise.resolve([])));
  const spec = (existing && OPERATE_KINDS.includes(kind) ? existing.spec : {}) as Record<string, unknown>;
  const d = spec as Partial<DashboardSpec>;
  const a = spec as Partial<AlertSpec>;
  const ch = spec as Partial<ChannelSpec>;
  const l = spec as Partial<LogExportSpec>;
  const st = spec as Partial<SecretStoreSpec>;

  const [widgets, setWidgets] = useState<WidgetRow[]>((d.widgets ?? [{ title: '', metric: 'cpu', resources: [] }]).map((w) => ({ ...w, resources: [...w.resources] })));
  const c = a.condition;
  const [condType, setCondType] = useState<ConditionType>(c?.type ?? 'metric');
  const [condResources, setCondResources] = useState<string[]>(c?.resources ?? []);
  const [condMetric, setCondMetric] = useState(c?.metric ?? 'cpu');
  const [operator, setOperator] = useState<'above' | 'below'>(c?.operator ?? 'above');
  const [threshold, setThreshold] = useState(c?.threshold !== undefined ? String(c.threshold) : '');
  const [forMinutes, setForMinutes] = useState(String(c?.forMinutes ?? RULES.alertForDefault));
  const [restarts, setRestarts] = useState(String(c?.restarts ?? RULES.alertRestartsDefault));
  const [windowMinutes, setWindowMinutes] = useState(String(c?.windowMinutes ?? RULES.alertWindowDefault));
  const [alertChannels, setAlertChannels] = useState<string[]>(a.channels ?? []);
  const [repeat, setRepeat] = useState(String(a.repeatMinutes ?? 0));
  const [enabled, setEnabled] = useState(a.enabled !== false);
  const [chType, setChType] = useState<ChannelType>(ch.type ?? 'webhook');
  const [emailService, setEmailService] = useState(ch.email?.service ?? '');
  const [emailFrom, setEmailFrom] = useState(ch.email?.from ?? '');
  const [emailTo, setEmailTo] = useState((ch.email?.to ?? []).join('\n'));
  const [hookUrl, setHookUrl] = useState(ch.webhook?.url ?? '');
  const [hookSecret, setHookSecret] = useState(ch.webhook?.secret ?? '');
  const [chSecret, setChSecret] = useState(ch.secret ?? '');
  const [chatId, setChatId] = useState(ch.chatId ?? '');
  const dest = l.destination ?? {};
  const [allSources, setAllSources] = useState(!(l.sources ?? []).length);
  const [sources, setSources] = useState<string[]>(l.sources ?? []);
  const [filter, setFilter] = useState(l.filter ?? '');
  const [flush, setFlush] = useState(l.flushSeconds !== undefined ? String(l.flushSeconds) : '');
  const [destType, setDestType] = useState<DestType>(logDestinationType({ destination: dest } as LogExportSpec) || 'bucket');
  const [destBucket, setDestBucket] = useState(dest.bucket?.bucket ?? '');
  const [destPrefix, setDestPrefix] = useState(dest.bucket?.prefix ?? dest.s3?.prefix ?? 'logs/');
  const [s3Endpoint, setS3Endpoint] = useState(dest.s3?.endpoint ?? '');
  const [s3Region, setS3Region] = useState(dest.s3?.region ?? '');
  const [s3Bucket, setS3Bucket] = useState(dest.s3?.bucket ?? '');
  const [s3Creds, setS3Creds] = useState(dest.s3?.credentialsSecret ?? '');
  const httpLike = dest.http ?? dest.otlp ?? dest.loki;
  const [destUrl, setDestUrl] = useState(dest.otlp?.endpoint ?? httpLike?.url ?? '');
  const [headersSecret, setHeadersSecret] = useState(httpLike?.headersSecret ?? '');
  const [labels, setLabels] = useState(Object.entries(dest.loki?.labels ?? {}).map(([k, v]) => `${k}=${v}`).join('\n'));
  const [provider, setProvider] = useState<SecretStoreProvider>(st.provider ?? 'vault');
  const block = (st.provider ? (st[st.provider] ?? {}) : {}) as Record<string, string | undefined>;
  const [settings, setSettings] = useState<Record<string, string>>(Object.fromEntries(Object.entries(block).map(([k, v]) => [k, v ?? ''])));
  const [refresh, setRefresh] = useState(String(st.refreshSeconds ?? RULES.storeRefreshDefault));
  const setting = (k: string) => settings[k] ?? '';
  const putSetting = (k: string, v: string) => setSettings((s) => ({ ...s, [k]: v }));

  /** A starting point for an alert (L2.5-002 §14). */
  function startFrom(preset: 'disk' | 'backlog', live: (k: Kind) => Resource[]) {
    setCondType('metric');
    setOperator('above');
    setForMinutes('5');
    if (preset === 'disk') {
      setCondMetric('diskPercentFull');
      setCondResources([MACHINE_REF]);
      setThreshold('90');
    } else {
      setCondMetric('queueDepth');
      setCondResources(live('queue').slice(0, 1).map((q) => `queue/${q.name}`));
      setThreshold('1000');
    }
  }

  function build(): Record<string, unknown> {
    switch (kind) {
      case 'dashboard':
        return { widgets: widgets.map((w): Widget => ({ title: w.title.trim(), metric: w.metric, resources: w.resources })) };
      case 'alert': {
        const cond: AlertCondition = { type: condType, resources: condResources };
        if (condType === 'metric') Object.assign(cond, { metric: condMetric, operator, threshold: threshold.trim() === '' ? undefined : Number(threshold), forMinutes: num(forMinutes) });
        if (condType === 'crash_loop') Object.assign(cond, { restarts: num(restarts), windowMinutes: num(windowMinutes) });
        if (condType === 'machine_offline') Object.assign(cond, { forMinutes: num(forMinutes) });
        const out: AlertSpec = { condition: cond, channels: alertChannels, repeatMinutes: num(repeat) ?? 0 };
        if (!enabled) out.enabled = false;
        return out as unknown as Record<string, unknown>;
      }
      case 'channel': {
        const out: ChannelSpec = { type: chType };
        if (chType === 'email') out.email = { service: emailService, from: emailFrom.trim(), to: emailTo.split(/[\s,]+/).filter(Boolean) };
        if (chType === 'webhook') out.webhook = { url: hookUrl.trim(), ...(hookSecret ? { secret: hookSecret } : {}) };
        if (chType === 'slack' || chType === 'discord' || chType === 'telegram') out.secret = chSecret;
        if (chType === 'telegram') out.chatId = chatId.trim();
        return out as unknown as Record<string, unknown>;
      }
      case 'logexport': {
        const destination: LogDestination = {};
        const headers = headersSecret ? { headersSecret } : {};
        if (destType === 'bucket') destination.bucket = { bucket: destBucket, prefix: destPrefix.trim() };
        if (destType === 's3') destination.s3 = { endpoint: s3Endpoint.trim(), ...(s3Region.trim() ? { region: s3Region.trim() } : {}), bucket: s3Bucket.trim(), prefix: destPrefix.trim(), credentialsSecret: s3Creds };
        if (destType === 'http') destination.http = { url: destUrl.trim(), ...headers };
        if (destType === 'otlp') destination.otlp = { endpoint: destUrl.trim(), ...headers };
        if (destType === 'loki') {
          const pairs = labels.split('\n').map((x) => x.trim()).filter(Boolean).map((x) => x.split('=') as [string, string | undefined]);
          destination.loki = { url: destUrl.trim(), ...headers, ...(pairs.length ? { labels: Object.fromEntries(pairs.map(([k, v]) => [k.trim(), (v ?? '').trim()])) } : {}) };
        }
        const out: LogExportSpec = { destination, ...(allSources ? {} : { sources }), ...(filter ? { filter } : {}) };
        const f = num(flush);
        if (f !== undefined) out.flushSeconds = f;
        return out as unknown as Record<string, unknown>;
      }
      case 'secretstore': {
        const keep = Object.fromEntries(Object.entries(settings).map(([k, v]) => [k, v.trim()]).filter(([, v]) => v));
        return { provider, refreshSeconds: num(refresh), [provider]: keep };
      }
    }
    return {};
  }

  function render(ctx: OperateContext): ReactNode {
    const { live, editing, errors, onBlur } = ctx;
    const err = (f: string) => errors[f];
    const names = (k: Kind) => live(k).map((r) => r.name);
    const secretOptions = (id: string, value: string, set: (v: string) => void, none?: string) => (
      <Select id={id} value={value} onChange={(e) => set(e.target.value)}>
        <option value="">{none ?? 'Choose a secret…'}</option>
        {names('secret').map((n) => <option key={n} value={n}>{n}</option>)}
      </Select>
    );
    const numberField = (id: string, label: string, value: string, set: (v: string) => void, field: string, hint?: string) => (
      <Field id={id} label={label} hint={hint} error={err(field)}>
        <Input id={id} inputMode="numeric" value={value} onChange={(e) => set(e.target.value)} onBlur={onBlur} aria-invalid={Boolean(err(field))} />
      </Field>
    );
    const textField = (id: string, label: string, value: string, set: (v: string) => void, field: string, hint?: string, placeholder?: string) => (
      <Field id={id} label={label} hint={hint} error={err(field)}>
        <Input id={id} mono value={value} onChange={(e) => set(e.target.value)} onBlur={onBlur} placeholder={placeholder} spellCheck={false} aria-invalid={Boolean(err(field))} />
      </Field>
    );
    /** Checkboxes of "kind/name" refs; `kinds` limits what's offered. */
    const refPicker = (legend: string, chosen: string[], set: (v: string[]) => void, field: string, kinds: readonly Kind[] | null, machine: boolean, emptyText: string) => {
      const options = [...(machine ? [MACHINE_REF] : []), ...KINDS.filter((k) => !OPERATE_KINDS.includes(k) && (!kinds || kinds.includes(k))).flatMap((k) => live(k)).map((r) => `${r.kind}/${r.name}`)];
      const all = [...new Set([...options, ...chosen])];
      return (
        <Fieldset legend={legend}>
          <Stack gap={1}>
            {all.length === 0 && <Text size="sm" tone="muted">{emptyText}</Text>}
            {all.map((ref) => (
              <Checkbox key={ref} label={<Text mono size="sm">{ref === MACHINE_REF ? 'This machine' : ref}</Text>} checked={chosen.includes(ref)}
                onChange={(e) => set(e.target.checked ? [...chosen, ref] : chosen.filter((x) => x !== ref))} />
            ))}
            {err(field) && <Text size="sm" tone="bad" role="alert">{err(field)}</Text>}
          </Stack>
        </Fieldset>
      );
    };
    const metricSelect = (id: string, value: string, set: (v: string) => void, field: string) => (
      <Field id={id} label="Metric" error={err(field)}>
        <Select id={id} value={value} onChange={(e) => set(e.target.value)}>
          <optgroup label="This machine">
            {MACHINE_METRICS.filter((m) => !METRICS.some((x) => x.name === m.name)).map((m) => <option key={m.name} value={m.name}>{m.title} ({m.name})</option>)}
          </optgroup>
          <optgroup label="Resources">
            {METRICS.map((m) => <option key={m.name} value={m.name}>{m.title} ({m.name})</option>)}
          </optgroup>
        </Select>
      </Field>
    );
    /** Resources that have a metric: kinds from the catalogue, the machine for machine metrics. */
    const kindsWith = (metric: string) => (METRICS.find((m) => m.name === metric)?.kinds ?? []) as readonly Kind[];
    const machineHas = (metric: string) => MACHINE_METRICS.some((m) => m.name === metric);

    switch (kind) {
      case 'dashboard':
        return (
          <Fieldset legend="Widgets">
            <Stack gap={4}>
              {widgets.map((w, i) => {
                const f = `spec.widgets[${i}]`;
                const update = (patch: Partial<WidgetRow>) => setWidgets(widgets.map((x, j) => (j === i ? { ...x, ...patch } : x)));
                const move = (to: number) => {
                  const next = [...widgets];
                  const [row] = next.splice(i, 1);
                  if (row) next.splice(to, 0, row);
                  setWidgets(next);
                };
                return (
                  <Stack key={i} gap={2}>
                    <Inline justify="between">
                      <Text size="sm" weight="semibold">Widget {i + 1}</Text>
                      <Inline gap={1}>
                        <IconButton size="sm" variant="ghost" label={`Move widget ${i + 1} up`} icon={<Icons.ChevronUp />} disabled={i === 0} onClick={() => move(i - 1)} />
                        <IconButton size="sm" variant="ghost" label={`Move widget ${i + 1} down`} icon={<Icons.ChevronDown />} disabled={i === widgets.length - 1} onClick={() => move(i + 1)} />
                        <IconButton size="sm" variant="ghost" label={`Remove widget ${i + 1}`} icon={<Icons.Trash2 />} disabled={widgets.length === 1} onClick={() => setWidgets(widgets.filter((_, j) => j !== i))} />
                      </Inline>
                    </Inline>
                    <FormRow>
                      <Field id={`f-widget-title-${i}`} label="Title" error={err(`${f}.title`)}>
                        <Input id={`f-widget-title-${i}`} value={w.title} onChange={(e) => update({ title: e.target.value })} onBlur={onBlur} placeholder="API CPU" maxLength={RULES.widgetTitleMax} />
                      </Field>
                      {metricSelect(`f-widget-metric-${i}`, w.metric, (m) => update({ metric: m, resources: w.resources.filter((ref) => (ref === MACHINE_REF ? machineHas(m) : kindsWith(m).includes(ref.split('/')[0] as Kind))) }), `${f}.metric`)}
                    </FormRow>
                    {refPicker(`Lines (up to ${RULES.widgetResourcesMax})`, w.resources, (v) => update({ resources: v }), `${f}.resources`, kindsWith(w.metric), machineHas(w.metric),
                      `Nothing in this project has the ${w.metric} metric yet.`)}
                  </Stack>
                );
              })}
              {err('spec.widgets') && <Text size="sm" tone="bad" role="alert">{err('spec.widgets')}</Text>}
              <div>
                <Button size="sm" icon={<Icons.Plus />} disabled={widgets.length >= RULES.widgetsMax} onClick={() => setWidgets([...widgets, { title: '', metric: 'cpu', resources: [] }])}>Add a widget</Button>
              </div>
            </Stack>
          </Fieldset>
        );
      case 'alert':
        return (
          <>
            {!editing && (
              <Inline gap={2} wrap>
                <Text size="sm" tone="muted">Start from:</Text>
                <Button size="sm" onClick={() => startFrom('disk', live)}>Disk nearly full</Button>
                <Button size="sm" onClick={() => startFrom('backlog', live)}>Queue backlog</Button>
              </Inline>
            )}
            <Fieldset legend="When">
              <Stack gap={2}>
                {(Object.keys(CONDITION_WORDS) as ConditionType[]).map((t) => (
                  <Choice key={t} name="f-condition" value={t} checked={condType === t} title={CONDITION_WORDS[t][0]} description={CONDITION_WORDS[t][1]}
                    onChange={() => {
                      setCondType(t);
                      setCondResources([]);
                      if (t === 'machine_offline') setForMinutes('1');
                    }} />
                ))}
              </Stack>
            </Fieldset>
            {condType === 'metric' && (
              <>
                <FormRow>
                  {metricSelect('f-cond-metric', condMetric, (m) => {
                    setCondMetric(m);
                    setCondResources(condResources.filter((ref) => (ref === MACHINE_REF ? machineHas(m) : kindsWith(m).includes(ref.split('/')[0] as Kind))));
                  }, 'spec.condition.metric')}
                  <Field id="f-cond-op" label="Is" error={err('spec.condition.operator')}>
                    <Select id="f-cond-op" value={operator} onChange={(e) => setOperator(e.target.value as 'above' | 'below')}>
                      <option value="above">above</option>
                      <option value="below">below</option>
                    </Select>
                  </Field>
                  <Field id="f-cond-threshold" label={`Threshold${metricOf(condMetric, condResources)?.unit ? ` (${metricOf(condMetric, condResources)?.unit ?? ''})` : ''}`} error={err('spec.condition.threshold')}
                    hint={metricOf(condMetric, condResources)?.unit === 'bytes' ? 'In bytes: 1 GiB is 1073741824.' : undefined}>
                    <Input id="f-cond-threshold" inputMode="decimal" value={threshold} onChange={(e) => setThreshold(e.target.value)} onBlur={onBlur} />
                  </Field>
                  {numberField('f-cond-for', 'For (minutes)', forMinutes, setForMinutes, 'spec.condition.forMinutes', '1–60.')}
                </FormRow>
                {refPicker('Of', condResources, setCondResources, 'spec.condition.resources', kindsWith(condMetric), machineHas(condMetric), `Nothing in this project has the ${condMetric} metric yet.`)}
              </>
            )}
            {condType === 'crash_loop' && (
              <>
                <FormRow>
                  {numberField('f-cond-restarts', 'Restarts', restarts, setRestarts, 'spec.condition.restarts', '1–100.')}
                  {numberField('f-cond-window', 'Within (minutes)', windowMinutes, setWindowMinutes, 'spec.condition.windowMinutes', '1–120.')}
                </FormRow>
                {refPicker('Of', condResources, setCondResources, 'spec.condition.resources', RULES.restartingKinds as readonly Kind[], false, 'Nothing in this project restarts: create an app, database or other service first.')}
              </>
            )}
            {condType === 'failed' && refPicker('Of', condResources, setCondResources, 'spec.condition.resources', null, false, 'This project has no resources yet.')}
            {condType === 'machine_offline' && (
              <>
                <Fieldset legend="Machines">
                  <Stack gap={1}>
                    {(machines.data ?? []).filter((m) => !m.main).map((m) => {
                      const ref = MACHINE_REF_PREFIX + m.name;
                      return <Checkbox key={ref} label={<Text mono size="sm">{m.name}</Text>} checked={condResources.includes(ref)}
                        onChange={(e) => setCondResources(e.target.checked ? [...condResources, ref] : condResources.filter((x) => x !== ref))} />;
                    })}
                    {err('spec.condition.resources') && <Text size="sm" tone="bad" role="alert">{err('spec.condition.resources')}</Text>}
                  </Stack>
                </Fieldset>
                {numberField('f-cond-for', 'For (minutes)', forMinutes, setForMinutes, 'spec.condition.forMinutes', '1–60.')}
              </>
            )}
            {condType === 'run_failed' && refPicker('Of', condResources, setCondResources, 'spec.condition.resources', RULES.runKinds as readonly Kind[], false, 'This project has no jobs, backups or transcoders yet.')}
            <Fieldset legend="Notify">
              <Stack gap={1}>
                {names('channel').length === 0 && <Text size="sm" tone="muted">This project has no channels yet. Create one in Channels first.</Text>}
                {[...new Set([...names('channel'), ...alertChannels])].map((n) => (
                  <Checkbox key={n} label={n} checked={alertChannels.includes(n)} onChange={(e) => setAlertChannels(e.target.checked ? [...alertChannels, n] : alertChannels.filter((x) => x !== n))} />
                ))}
                {err('spec.channels') && <Text size="sm" tone="bad" role="alert">{err('spec.channels')}</Text>}
              </Stack>
            </Fieldset>
            <FormRow>
              <Field id="f-repeat" label="Remind every" error={err('spec.repeatMinutes')}>
                <Select id="f-repeat" value={repeat} onChange={(e) => setRepeat(e.target.value)}>
                  {[...new Set(['0', '15', '30', '60', '240', '1440', repeat])].map((m) => (
                    <option key={m} value={m}>{m === '0' ? 'Never' : Number(m) % 60 === 0 ? `${Number(m) / 60} hour${m === '60' ? '' : 's'}` : `${m} minutes`}</option>
                  ))}
                </Select>
              </Field>
            </FormRow>
            <Checkbox label="On: evaluate and notify" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          </>
        );
      case 'channel':
        return (
          <>
            <Fieldset legend="Type">
              <Stack gap={2}>
                {(editing ? [chType] : (Object.keys(CHANNEL_WORDS) as ChannelType[])).map((t) => (
                  <Choice key={t} name="f-channel-type" value={t} checked={chType === t} disabled={editing} title={CHANNEL_WORDS[t][0]} description={CHANNEL_WORDS[t][1]} onChange={() => setChType(t)} />
                ))}
                {editing && <Text size="sm" tone="subtle">The type can&apos;t change. Create a new channel instead.</Text>}
              </Stack>
            </Fieldset>
            {chType === 'email' && (
              <>
                <Field id="f-ch-service" label="Email service" error={err('spec.email.service')} hint={names('email').length ? undefined : 'This project has no email service yet. Create one in Email first.'}>
                  <Select id="f-ch-service" value={emailService} onChange={(e) => setEmailService(e.target.value)}>
                    <option value="">Choose an email service…</option>
                    {names('email').map((n) => <option key={n} value={n}>{n}</option>)}
                  </Select>
                </Field>
                {textField('f-ch-from', 'From', emailFrom, setEmailFrom, 'spec.email.from', undefined, 'alerts@example.com')}
                <Field id="f-ch-to" label="To" hint="One address per line, up to 20." error={err('spec.email.to') ?? Object.entries(errors).find(([k]) => k.startsWith('spec.email.to['))?.[1]}>
                  <Input id="f-ch-to" mono value={emailTo} onChange={(e) => setEmailTo(e.target.value)} onBlur={onBlur} placeholder="oncall@example.com" />
                </Field>
              </>
            )}
            {chType === 'webhook' && (
              <>
                {textField('f-ch-url', 'URL', hookUrl, setHookUrl, 'spec.webhook.url', 'https://, or http:// to localhost.', 'https://hooks.example.com/enclave')}
                <Field id="f-ch-hook-secret" label="Signing secret" hint="Optional. Bodies are signed with HMAC-SHA256 in X-Enclave-Signature." error={err('spec.webhook.secret')}>
                  {secretOptions('f-ch-hook-secret', hookSecret, setHookSecret, 'None: unsigned')}
                </Field>
              </>
            )}
            {(chType === 'slack' || chType === 'discord' || chType === 'telegram') && (
              <Field id="f-ch-secret" label={chType === 'telegram' ? 'Bot token secret' : 'Webhook URL secret'} error={err('spec.secret')}
                hint={chType === 'telegram' ? 'Create a secret holding the token BotFather gave you.' : `Paste the ${chType === 'slack' ? 'Slack incoming-webhook' : 'Discord webhook'} URL into a secret, then choose it here.`}>
                {secretOptions('f-ch-secret', chSecret, setChSecret)}
              </Field>
            )}
            {chType === 'telegram' && textField('f-ch-chat', 'Chat ID', chatId, setChatId, 'spec.chatId', 'Like -1001234567890, or @channelname.')}
          </>
        );
      case 'logexport': {
        const outside = destType !== 'bucket';
        return (
          <>
            <Fieldset legend="Send the logs of">
              <Stack gap={2}>
                <Choice name="f-sources" value="all" checked={allSources} onChange={() => setAllSources(true)} title="Every resource in this project" description="Resources made later are included too." />
                <Choice name="f-sources" value="some" checked={!allSources} onChange={() => setAllSources(false)} title="Chosen resources" />
              </Stack>
            </Fieldset>
            {!allSources && refPicker('Resources', sources, setSources, 'spec.sources', RULES.logKinds as readonly Kind[], false, 'This project has nothing with logs yet.')}
            <Fieldset legend="To">
              <Stack gap={2}>
                {(Object.keys(DEST_WORDS) as DestType[]).map((t) => (
                  <Choice key={t} name="f-dest" value={t} checked={destType === t} title={DEST_WORDS[t][0]} description={DEST_WORDS[t][1]} onChange={() => setDestType(t)} />
                ))}
                {err('spec.destination') && <Text size="sm" tone="bad" role="alert">{err('spec.destination')}</Text>}
              </Stack>
            </Fieldset>
            {outside && <Alert tone="warn">Logs leave this machine. Anything your apps print, including mistakes like printed passwords, is sent.</Alert>}
            {destType === 'bucket' && (
              <FormRow>
                <Field id="f-dest-bucket" label="Bucket" error={err('spec.destination.bucket.bucket')}>
                  <Select id="f-dest-bucket" value={destBucket} onChange={(e) => setDestBucket(e.target.value)}>
                    <option value="">Choose a bucket…</option>
                    {names('bucket').map((n) => <option key={n} value={n}>{n}</option>)}
                  </Select>
                </Field>
                {textField('f-dest-prefix', 'Folder', destPrefix, setDestPrefix, 'spec.destination.bucket.prefix', 'Like logs/.')}
              </FormRow>
            )}
            {destType === 's3' && (
              <>
                <FormRow>
                  {textField('f-s3-endpoint', 'Endpoint', s3Endpoint, setS3Endpoint, 'spec.destination.s3.endpoint', undefined, 'https://s3.eu-west-1.amazonaws.com')}
                  {textField('f-s3-region', 'Region', s3Region, setS3Region, 'spec.destination.s3.region', 'Optional.')}
                </FormRow>
                <FormRow>
                  {textField('f-s3-bucket', 'Bucket', s3Bucket, setS3Bucket, 'spec.destination.s3.bucket')}
                  {textField('f-dest-prefix', 'Folder', destPrefix, setDestPrefix, 'spec.destination.s3.prefix', 'Like logs/.')}
                </FormRow>
                <Field id="f-s3-creds" label="Credentials secret" hint={'A secret holding {"accessKey","secretKey"}.'} error={err('spec.destination.s3.credentialsSecret')}>
                  {secretOptions('f-s3-creds', s3Creds, setS3Creds)}
                </Field>
              </>
            )}
            {(destType === 'http' || destType === 'otlp' || destType === 'loki') && (
              <>
                {textField('f-dest-url', destType === 'otlp' ? 'Endpoint' : 'URL', destUrl, setDestUrl, `spec.destination.${destType}.${destType === 'otlp' ? 'endpoint' : 'url'}`,
                  destType === 'otlp' ? 'The collector, like https://otel.example.com:4318.' : destType === 'loki' ? 'Like https://logs-prod.grafana.net.' : undefined, 'https://')}
                <Field id="f-dest-headers" label="Headers secret" hint="Optional: a secret holding a JSON object of headers, like an Authorization token." error={err(`spec.destination.${destType}.headersSecret`)}>
                  {secretOptions('f-dest-headers', headersSecret, setHeadersSecret, 'None')}
                </Field>
                {destType === 'loki' && (
                  <Field id="f-dest-labels" label="Extra labels" hint="NAME=value, one per line. EnclaveStack adds project, kind and resource." error={Object.entries(errors).find(([k]) => k.startsWith('spec.destination.loki.labels'))?.[1]}>
                    <Input id="f-dest-labels" mono value={labels} onChange={(e) => setLabels(e.target.value)} onBlur={onBlur} placeholder="env=prod" />
                  </Field>
                )}
              </>
            )}
            <FormRow>
              {textField('f-filter', 'Only lines matching', filter, setFilter, 'spec.filter', 'Optional regular expression, like error|warn.')}
              {numberField('f-flush', 'Send every (seconds)', flush, setFlush, 'spec.flushSeconds', `5–300. Empty: ${destType === 'bucket' || destType === 's3' ? RULES.logFlushBucketDefault : RULES.logFlushDefault}.`)}
            </FormRow>
          </>
        );
      }
      case 'secretstore': {
        const sf = (k: string, label: string, hint?: string, placeholder?: string) => textField(`f-store-${k}`, label, setting(k), (v) => putSetting(k, v), `spec.${provider}.${k}`, hint, placeholder);
        const secretField = (k: string, label: string, hint: string) => (
          <Field id={`f-store-${k}`} label={label} hint={hint} error={err(`spec.${provider}.${k}`)}>
            {secretOptions(`f-store-${k}`, setting(k), (v) => putSetting(k, v))}
          </Field>
        );
        return (
          <>
            <Fieldset legend="Provider">
              <Stack gap={2}>
                {(editing ? [provider] : (Object.keys(PROVIDER_WORDS) as SecretStoreProvider[])).map((p) => (
                  <Choice key={p} name="f-provider" value={p} checked={provider === p} disabled={editing} title={PROVIDER_WORDS[p][0]} description={PROVIDER_WORDS[p][1]}
                    onChange={() => {
                      setProvider(p);
                      setSettings({});
                    }} />
                ))}
                {editing && <Text size="sm" tone="subtle">The provider can&apos;t change. Create a new store instead.</Text>}
              </Stack>
            </Fieldset>
            {provider === 'vault' && (
              <>
                {sf('address', 'Address', undefined, 'https://vault.example.com:8200')}
                <FormRow>
                  {sf('mount', 'KV v2 mount', `Empty: ${RULES.vaultMountDefault}.`)}
                  {sf('namespace', 'Namespace', 'Vault Enterprise and HCP only.')}
                </FormRow>
                {secretField('tokenSecret', 'Token secret', 'A secret holding a Vault token that can read the paths you sync.')}
              </>
            )}
            {provider === 'onepassword' && (
              <>
                {sf('connectUrl', 'Connect server', undefined, 'http://op-connect:8080')}
                {sf('vault', 'Vault', "The vault's name or ID.")}
                {secretField('tokenSecret', 'Token secret', 'A secret holding a Connect access token.')}
              </>
            )}
            {provider === 'aws' && (
              <>
                <FormRow>
                  {sf('region', 'Region', undefined, 'eu-west-1')}
                  {sf('endpoint', 'Endpoint', 'Optional: another API endpoint, like LocalStack.')}
                </FormRow>
                {secretField('credentialsSecret', 'Credentials secret', 'A secret holding {"accessKey","secretKey"} of a user that can read the secrets.')}
              </>
            )}
            {provider === 'doppler' && (
              <>
                {secretField('tokenSecret', 'Service token secret', "A secret holding a service token of the config you sync (dp.st.…).")}
                {sf('endpoint', 'Endpoint', 'Optional: another API endpoint.')}
              </>
            )}
            {provider === 'infisical' && (
              <>
                <FormRow>
                  {sf('projectId', 'Project ID')}
                  {sf('environment', 'Environment', 'The slug, like prod.')}
                </FormRow>
                <FormRow>
                  {sf('siteUrl', 'Site', `Empty: ${RULES.infisicalSiteDefault}.`)}
                  {sf('path', 'Path', 'Empty: /.')}
                </FormRow>
                {secretField('credentialsSecret', 'Credentials secret', 'A secret holding {"clientId","clientSecret"} of a machine identity.')}
              </>
            )}
            {numberField('f-store-refresh', 'Read again every (seconds)', refresh, setRefresh, 'spec.refreshSeconds', '60–86400. Synced secrets pick up changes this often.')}
          </>
        );
      }
    }
    return null;
  }

  return { build, render };
}
