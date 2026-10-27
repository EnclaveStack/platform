import { MACHINE_METRICS, METRICS, RULES, type MetricUnit } from '../api/generated';
import type { AlertSpec, ChannelSpec, DashboardSpec, Kind, LogExportSpec, LogHttp, SecretSource, SecretStoreSpec } from '../api/types';
import { formatBytes } from './format';
import { normalizePrefix, validateEmail, validateName, validWebhookUrl, type FieldErrors } from './validation';

// v0.7 (L3-002 §10.6): validation of the operate kinds with the same rules and wording as pkg/api
// operate.go, metric formatting, and the metric catalogue's lookups.

export const MACHINE_REF = RULES.machineRef;
/** A machine in a machine_offline condition: machine/NAME (v0.8). */
export const MACHINE_REF_PREFIX = RULES.machineRefPrefix;

const inRange = (v: number | undefined, lo: number, hi: number) => v !== undefined && Number.isInteger(v) && v >= lo && v <= hi;

/** "kind/name" → [kind, name], or undefined. */
export function parseRef(ref: string): [Kind, string] | undefined {
  const [kind, name, extra] = ref.split('/');
  if (!kind || !name || extra !== undefined || validateName(name)) return undefined;
  return [kind as Kind, name];
}

export function metricInfo(name: string, kind?: Kind | 'machine') {
  if (kind === 'machine') return MACHINE_METRICS.find((m) => m.name === name);
  return METRICS.find((m) => m.name === name && (!kind || (m.kinds as readonly string[]).includes(kind)));
}

/** A metric as a condition or widget names it: from the machine's catalogue when its resources are the machine. */
export const metricOf = (metric: string, refs: readonly string[]) => metricInfo(metric, refs.length === 1 && refs[0] === MACHINE_REF ? 'machine' : undefined) ?? metricInfo(metric, 'machine');

/** The metrics a kind has, in catalogue order. */
export const metricsOf = (kind: Kind) => METRICS.filter((m) => (m.kinds as readonly string[]).includes(kind));

function validateRefs(refs: string[], field: string, min: number, max: number, kinds: readonly string[] | null): FieldErrors {
  if (refs.length < min || refs.length > max) return { [field]: min === 0 ? `Name at most ${max} resources.` : `Name ${min}–${max} resources as kind/name, like app/api.` };
  const e: FieldErrors = {};
  refs.forEach((r, i) => {
    const f = `${field}[${i}]`;
    const p = parseRef(r);
    if (!p) e[f] = 'Use kind/name, like app/api.';
    else if (kinds && !kinds.includes(p[0])) e[f] = `Choose one of: ${kinds.join(', ')}.`;
    else if (refs.indexOf(r) !== i) e[f] = `${r} is listed twice.`;
  });
  return e;
}

function validateMetricRefs(metric: string, refs: string[], metricField: string, refsField: string, max: number): FieldErrors {
  if (refs.length === 1 && refs[0] === MACHINE_REF) {
    return metricInfo(metric, 'machine') ? {} : { [metricField]: "The machine's metrics are cpu, memory, diskFree, diskPercentFull and resourcesFailed." };
  }
  const e: FieldErrors = {};
  if (!METRICS.some((m) => m.name === metric)) e[metricField] = 'Choose a metric from the list, like cpu, memory or requests.';
  Object.assign(e, validateRefs(refs, refsField, 1, max, null));
  if (!Object.keys(e).length) {
    refs.forEach((r, i) => {
      const p = parseRef(r);
      if (p && !metricInfo(metric, p[0])) e[`${refsField}[${i}]`] = `A ${p[0]} has no ${metric} metric.`;
    });
  }
  return e;
}

export function validateDashboard(s: DashboardSpec): FieldErrors {
  const e: FieldErrors = {};
  const widgets = s.widgets ?? [];
  if (!widgets.length || widgets.length > RULES.widgetsMax) e['spec.widgets'] = 'Use 1–24 widgets.';
  widgets.forEach((w, i) => {
    const f = `spec.widgets[${i}]`;
    const n = [...w.title.trim()].length;
    if (!n || n > RULES.widgetTitleMax) e[`${f}.title`] = 'Use a title of 1–80 characters.';
    Object.assign(e, validateMetricRefs(w.metric, w.resources, `${f}.metric`, `${f}.resources`, RULES.widgetResourcesMax));
  });
  return e;
}

function validateNames(names: string[], field: string, min: number, max: number, what: string): FieldErrors {
  if (names.length < min || names.length > max) return { [field]: `Choose ${min}–${max} ${what}.` };
  const e: FieldErrors = {};
  names.forEach((n, i) => {
    if (validateName(n)) e[`${field}[${i}]`] = 'Name an existing one.';
    else if (names.indexOf(n) !== i) e[`${field}[${i}]`] = `${n} is listed twice.`;
  });
  return e;
}

export function validateAlert(s: AlertSpec): FieldErrors {
  const e: FieldErrors = {};
  const c = s.condition;
  const refs = c?.resources ?? [];
  switch (c?.type) {
    case 'metric':
      Object.assign(e, validateMetricRefs(c.metric ?? '', refs, 'spec.condition.metric', 'spec.condition.resources', RULES.alertResourcesMax));
      if (c.operator !== 'above' && c.operator !== 'below') e['spec.condition.operator'] = 'Choose above or below.';
      if (c.threshold === undefined || !Number.isFinite(c.threshold)) e['spec.condition.threshold'] = 'Enter the threshold, a number.';
      if (!inRange(c.forMinutes ?? RULES.alertForDefault, RULES.alertForMin, RULES.alertForMax)) e['spec.condition.forMinutes'] = 'Use 1–60 minutes.';
      break;
    case 'crash_loop':
      Object.assign(e, validateRefs(refs, 'spec.condition.resources', 1, RULES.alertResourcesMax, RULES.restartingKinds));
      if (!inRange(c.restarts ?? RULES.alertRestartsDefault, RULES.alertRestartsMin, RULES.alertRestartsMax)) e['spec.condition.restarts'] = 'Use 1–100 restarts.';
      if (!inRange(c.windowMinutes ?? RULES.alertWindowDefault, RULES.alertWindowMin, RULES.alertWindowMax)) e['spec.condition.windowMinutes'] = 'Use 1–120 minutes.';
      break;
    case 'failed':
      Object.assign(e, validateRefs(refs, 'spec.condition.resources', 1, RULES.alertResourcesMax, null));
      break;
    case 'run_failed':
      Object.assign(e, validateRefs(refs, 'spec.condition.resources', 1, RULES.alertResourcesMax, RULES.runKinds));
      break;
    case 'machine_offline':
      // v0.8 (MCH-06): machines, as machine/NAME.
      if (refs.length < 1 || refs.length > RULES.alertResourcesMax) e['spec.condition.resources'] = `Name 1–${RULES.alertResourcesMax} machines, as machine/NAME.`;
      refs.forEach((ref, i) => {
        if (!ref.startsWith(MACHINE_REF_PREFIX) || validateName(ref.slice(MACHINE_REF_PREFIX.length))) e[`spec.condition.resources[${i}]`] = 'Name a machine as machine/NAME.';
      });
      if (!inRange(c.forMinutes ?? 1, RULES.alertForMin, RULES.alertForMax)) e['spec.condition.forMinutes'] = 'Use 1–60 minutes.';
      break;
    case 'event': {
      // v0.10 (L2-016 EVN-01, EVN-02): each matching event is sent once, as it happens.
      Object.assign(e, validateRefs(refs, 'spec.condition.resources', 1, RULES.alertResourcesMax, null));
      const events = c.events ?? [];
      if (events.length < 1 || events.length > RULES.alertEventsMax) e['spec.condition.events'] = `Choose 1–${RULES.alertEventsMax} events.`;
      const seen = new Set<string>();
      events.forEach((ev, i) => {
        if (!(RULES.notifiableEvents as readonly string[]).includes(ev)) e[`spec.condition.events[${i}]`] = `Choose one of ${RULES.notifiableEvents.join(', ')}.`;
        else if (seen.has(ev)) e[`spec.condition.events[${i}]`] = `${ev} is listed twice.`;
        seen.add(ev);
      });
      if ((s.repeatMinutes ?? 0) !== 0) e['spec.repeatMinutes'] = 'An event alert sends each event once; it never repeats.';
      break;
    }
    default:
      e['spec.condition.type'] = 'Choose metric, crash_loop, failed, run_failed, machine_offline or event.';
  }
  if (c.type !== 'event' && c.events?.length) e['spec.condition.events'] = 'Events are for event conditions only.';
  Object.assign(e, validateNames(s.channels ?? [], 'spec.channels', 1, RULES.alertChannelsMax, 'channels'));
  if (!inRange(s.repeatMinutes ?? 0, 0, RULES.alertRepeatMax)) e['spec.repeatMinutes'] = 'Use 0 (never) to 1440 minutes.';
  return e;
}

export function validateChannel(s: ChannelSpec): FieldErrors {
  const e: FieldErrors = {};
  switch (s.type) {
    case 'email': {
      const m = s.email;
      if (!m) return { 'spec.email': 'Choose the email service, the sender and the recipients.' };
      if (validateName(m.service)) e['spec.email.service'] = 'Choose an email service of this project.';
      if (validateEmail(m.from.trim())) e['spec.email.from'] = 'Enter the sender, like alerts@example.com.';
      if (!m.to.length || m.to.length > RULES.channelRecipientsMax) e['spec.email.to'] = 'Add 1–20 recipients.';
      m.to.forEach((to, i) => {
        if (validateEmail(to.trim())) e[`spec.email.to[${i}]`] = 'Enter an address like oncall@example.com.';
      });
      break;
    }
    case 'webhook':
      if (!s.webhook || !validWebhookUrl(s.webhook.url)) e['spec.webhook.url'] = 'Use an https:// URL (http:// only for localhost).';
      else if (s.webhook.secret && validateName(s.webhook.secret)) e['spec.webhook.secret'] = 'Name an existing secret.';
      break;
    case 'slack':
    case 'discord':
    case 'telegram':
      if (validateName(s.secret ?? '')) e['spec.secret'] = `Name the secret holding ${s.type === 'telegram' ? 'the bot token' : 'the incoming-webhook URL'}.`;
      if (s.type === 'telegram' && (!s.chatId || s.chatId.length > RULES.chatIdMax || /\s/.test(s.chatId))) e['spec.chatId'] = 'Enter the chat ID, like -1001234567890 or @channelname.';
      break;
    default:
      return { 'spec.type': 'Choose email, webhook, slack, discord or telegram.' };
  }
  return e;
}

/** http:// or https:// with a host (pkg/api ValidHTTPURL). */
export function validHttpUrl(raw: string): boolean {
  try {
    const u = new URL(raw);
    return (u.protocol === 'http:' || u.protocol === 'https:') && Boolean(u.host) && !u.username && !u.password && raw.length <= 2048;
  } catch {
    return false;
  }
}

const prefixChars = /^[A-Za-z0-9!_.*'()/ +=,@-]*$/;
const PREFIX_RULE = 'Use a folder like videos/ or media/2026/: letters, digits and - _ . / only, up to 255 characters.';
const validPrefix = (p: string) => p.length <= RULES.prefixMax && prefixChars.test(p) && !p.split('/').includes('..') && !p.includes('//');
const s3BucketPattern = new RegExp(RULES.s3BucketPattern);
const lokiLabel = /^[a-zA-Z_][a-zA-Z0-9_]*$/;

function validEndpoint(raw: string): boolean {
  try {
    const u = new URL(raw);
    const local = u.hostname === 'localhost' || u.hostname === '127.0.0.1';
    return Boolean(u.host) && (u.protocol === 'https:' || (u.protocol === 'http:' && local)) && (u.pathname === '/' || u.pathname === '');
  } catch {
    return false;
  }
}

function validateLogHttp(h: LogHttp, field: string, urlField: 'url' | 'endpoint', labels: boolean): FieldErrors {
  const e: FieldErrors = {};
  if (!validHttpUrl((urlField === 'url' ? h.url : h.endpoint) ?? '')) e[`${field}.${urlField}`] = 'Use an http:// or https:// URL.';
  if (h.headersSecret && validateName(h.headersSecret)) e[`${field}.headersSecret`] = 'Name an existing secret.';
  const keys = Object.keys(h.labels ?? {});
  if (!labels && keys.length) e[`${field}.labels`] = 'Only Loki takes labels.';
  if (keys.length > RULES.logLabelsMax) e[`${field}.labels`] = 'Use at most 10 labels.';
  for (const k of keys) if (!lokiLabel.test(k)) e[`${field}.labels.${k}`] = 'Label names use letters, digits and _, starting with a letter or _.';
  return e;
}

/** The destination's type: the one key that is set. */
export function logDestinationType(s: LogExportSpec): 'bucket' | 's3' | 'http' | 'otlp' | 'loki' | '' {
  const d = s.destination ?? {};
  return d.bucket ? 'bucket' : d.s3 ? 's3' : d.http ? 'http' : d.otlp ? 'otlp' : d.loki ? 'loki' : '';
}

export function validateLogExport(s: LogExportSpec): FieldErrors {
  const e: FieldErrors = validateRefs(s.sources ?? [], 'spec.sources', 0, RULES.logSourcesMax, RULES.logKinds);
  if ((s.filter ?? '').length > RULES.logFilterMax) e['spec.filter'] = 'Use at most 512 characters.';
  else if (s.filter) {
    try {
      new RegExp(s.filter);
    } catch (err) {
      e['spec.filter'] = `That isn't a valid regular expression: ${(err as Error).message}.`;
    }
  }
  const type = logDestinationType(s);
  const flush = s.flushSeconds ?? (type === 'bucket' || type === 's3' ? RULES.logFlushBucketDefault : RULES.logFlushDefault);
  if (!inRange(flush, RULES.logFlushMin, RULES.logFlushMax)) e['spec.flushSeconds'] = 'Use 5–300 seconds.';
  const d = s.destination ?? {};
  if ([d.bucket, d.s3, d.http, d.otlp, d.loki].filter(Boolean).length !== 1) {
    e['spec.destination'] = 'Choose one destination: a bucket, S3, HTTP, OpenTelemetry or Loki.';
    return e;
  }
  if (d.bucket) {
    if (!d.bucket.bucket) e['spec.destination.bucket.bucket'] = 'Choose a bucket.';
    else if (validateName(d.bucket.bucket)) e['spec.destination.bucket.bucket'] = 'Name an existing bucket.';
    if (!validPrefix(normalizePrefix(d.bucket.prefix))) e['spec.destination.bucket.prefix'] = PREFIX_RULE;
  } else if (d.s3) {
    if (!validEndpoint(d.s3.endpoint)) e['spec.destination.s3.endpoint'] = 'Use an https:// endpoint like https://s3.eu-central-1.amazonaws.com (http only for localhost).';
    if (!s3BucketPattern.test(d.s3.bucket)) e['spec.destination.s3.bucket'] = 'Use an S3 bucket name: 3–63 lowercase letters, digits, dots or hyphens.';
    if (validateName(d.s3.credentialsSecret)) e['spec.destination.s3.credentialsSecret'] = 'Name the secret holding {"accessKey","secretKey"}.';
    if (!validPrefix(normalizePrefix(d.s3.prefix))) e['spec.destination.s3.prefix'] = PREFIX_RULE;
  } else if (d.http) Object.assign(e, validateLogHttp(d.http, 'spec.destination.http', 'url', false));
  else if (d.otlp) Object.assign(e, validateLogHttp(d.otlp, 'spec.destination.otlp', 'endpoint', false));
  else if (d.loki) Object.assign(e, validateLogHttp(d.loki, 'spec.destination.loki', 'url', true));
  return e;
}

const storePath = /^[\x21-\x7e]{1,512}$/;
const awsRegion = /^[a-z]{2}(-[a-z]+)+-\d$/;

export function validateSecretStore(s: SecretStoreSpec): FieldErrors {
  if (!(RULES.secretStoreProviders as readonly string[]).includes(s.provider)) return { 'spec.provider': 'Choose vault, onepassword, aws, doppler or infisical.' };
  const e: FieldErrors = {};
  for (const p of RULES.secretStoreProviders) {
    if (p === s.provider && !s[p]) e[`spec.${p}`] = `Fill in the ${p} settings.`;
    if (p !== s.provider && s[p]) e[`spec.${p}`] = `Only the ${s.provider} settings belong here.`;
  }
  if (!inRange(s.refreshSeconds ?? RULES.storeRefreshDefault, RULES.storeRefreshMin, RULES.storeRefreshMax)) e['spec.refreshSeconds'] = 'Use 60 seconds to a day (86400).';
  const url = (raw: string | undefined, field: string, optional: boolean) => {
    if ((raw || !optional) && !validHttpUrl(raw ?? '')) e[field] = 'Use an http:// or https:// URL.';
  };
  const secret = (name: string, field: string) => {
    if (validateName(name)) e[field] = 'Name the secret EnclaveStack signs in with.';
  };
  const path = (p: string | undefined, field: string) => {
    if (p && (!storePath.test(p) || p.split('/').includes('..'))) e[field] = 'Use printable characters without spaces or ..; up to 512.';
  };
  if (s.provider === 'vault' && s.vault) {
    url(s.vault.address, 'spec.vault.address', false);
    path(s.vault.mount, 'spec.vault.mount');
    path(s.vault.namespace, 'spec.vault.namespace');
    secret(s.vault.tokenSecret, 'spec.vault.tokenSecret');
  } else if (s.provider === 'onepassword' && s.onepassword) {
    url(s.onepassword.connectUrl, 'spec.onepassword.connectUrl', false);
    const n = [...s.onepassword.vault].length;
    if (!n || n > 128) e['spec.onepassword.vault'] = "Enter the vault's name or ID.";
    secret(s.onepassword.tokenSecret, 'spec.onepassword.tokenSecret');
  } else if (s.provider === 'aws' && s.aws) {
    if (!awsRegion.test(s.aws.region)) e['spec.aws.region'] = 'Use a region like eu-west-1.';
    url(s.aws.endpoint, 'spec.aws.endpoint', true);
    secret(s.aws.credentialsSecret, 'spec.aws.credentialsSecret');
  } else if (s.provider === 'doppler' && s.doppler) {
    url(s.doppler.endpoint, 'spec.doppler.endpoint', true);
    secret(s.doppler.tokenSecret, 'spec.doppler.tokenSecret');
  } else if (s.provider === 'infisical' && s.infisical) {
    url(s.infisical.siteUrl ?? RULES.infisicalSiteDefault, 'spec.infisical.siteUrl', false);
    if (!s.infisical.projectId || s.infisical.projectId.length > 128) e['spec.infisical.projectId'] = 'Enter the Infisical project ID.';
    if (!s.infisical.environment || s.infisical.environment.length > 64) e['spec.infisical.environment'] = 'Enter the environment slug, like prod.';
    path(s.infisical.path, 'spec.infisical.path');
    secret(s.infisical.credentialsSecret, 'spec.infisical.credentialsSecret');
  }
  return e;
}

export function validateSecretSource(src: SecretSource): FieldErrors {
  const e: FieldErrors = {};
  if (validateName(src.store)) e['spec.source.store'] = 'Choose a secret store of this project.';
  const n = [...src.key].length;
  if (!n || n > RULES.secretKeyMax || /[\r\n]/.test(src.key)) e['spec.source.key'] = 'Enter the key: a path, item or secret name, up to 512 characters.';
  if ([...(src.field ?? '')].length > RULES.secretFieldMax || /[\r\n]/.test(src.field ?? '')) e['spec.source.field'] = 'Use at most 128 characters.';
  return e;
}

// ─── Formatting ─────────────────────────────────────────────────────────

const short = (v: number) => (Number.isInteger(v) ? String(v) : Math.abs(v) >= 100 ? v.toFixed(0) : Math.abs(v) >= 10 ? v.toFixed(1) : Math.abs(v) >= 0.01 || v === 0 ? v.toFixed(2) : v.toExponential(1));

/** A value in its unit, like "1.2 GiB", "0.50 cores" or "120 ms". */
export function formatMetric(v: number, unit: MetricUnit): string {
  switch (unit) {
    case 'bytes':
      return formatBytes(v);
    case 'bytesPerSecond':
      return `${formatBytes(v)}/s`;
    case 'cores':
      return `${short(v)} cores`;
    case 'perSecond':
      return `${short(v)}/s`;
    case 'count':
      return Number.isInteger(v) ? String(v) : short(v);
    case 'seconds':
      return v >= 120 ? `${(v / 60).toFixed(0)} min` : `${short(v)} s`;
    case 'milliseconds':
      return `${short(v)} ms`;
    case 'percent':
      return `${short(v)} %`;
  }
}

/** The metric ranges of the Metrics tab and dashboards (L2.5-002 §14). */
export const RANGES = [
  { id: '1h', label: '1h', seconds: 3600 },
  { id: '6h', label: '6h', seconds: 6 * 3600 },
  { id: '24h', label: '24h', seconds: 86400 },
  { id: '7d', label: '7d', seconds: 7 * 86400 },
  { id: '30d', label: '30d', seconds: 30 * 86400 },
] as const;

export type RangeId = (typeof RANGES)[number]['id'];
