import { RULES } from '../api/generated';
import type { Placed, StackSpec, AlertSpec, ChannelSpec, DashboardSpec, LogExportSpec, SecretStoreSpec, AppSpec, BackupSpec, CacheSpec, CDNSpec, DatabaseSpec, DomainSpec, EmailSpec, FirewallRule, FirewallSpec, GitSource, HealthCheck, JobSpec, Kind, LoadBalancerSpec, NetworkSpec, QueueSpec, RecordSpec, SearchSpec, SecretSpec, SnapshotSpec, TopicSpec, TranscoderSpec, VMSpec, VolumeMount, VolumeSpec, ZoneSpec } from '../api/types';
import { validateAlert, validateChannel, validateDashboard, validateLogExport, validateSecretSource, validateSecretStore } from './operate';
import { parseCron } from './schedule';

// Form validation with the same rules and wording the server uses (L3-002 §4, L2.5-002 §1). The
// numbers and patterns come from generated.ts, so the console can't drift from pkg/api. The server
// still validates everything; this only saves a round trip.

export type FieldErrors = Record<string, string>;

const namePattern = new RegExp(RULES.namePattern);
const imagePattern = new RegExp(RULES.imagePattern);
const envKeyPattern = new RegExp(RULES.envKeyPattern);

export const NAME_RULE = 'Use 3–40 lowercase letters, numbers or hyphens, starting with a letter and not ending with a hyphen.';

export function validateName(name: string): string | undefined {
  if (!name) return 'Enter a name.';
  if (name.length < RULES.nameMin || name.length > RULES.nameMax || !namePattern.test(name)) return NAME_RULE;
  return undefined;
}

const utf8Length = (s: string) => new TextEncoder().encode(s).length;

export function validateApp(s: AppSpec): FieldErrors {
  const e: FieldErrors = {};
  if (s.image && s.source) e['spec.image'] = 'Deploy an image or build from Git — choose one.';
  else if (s.source) Object.assign(e, validateGitSource(s.source));
  else if (!s.image) e['spec.image'] = 'Enter an image, for example nginx:1.27.';
  else if (s.image.length > RULES.imageMax) e['spec.image'] = 'Images can be at most 255 characters.';
  else if (!imagePattern.test(s.image)) e['spec.image'] = 'Use an image reference like nginx:1.27 or ghcr.io/acme/api:1.4.';
  if (s.port !== undefined && (s.port < RULES.portMin || s.port > RULES.portMax || !Number.isInteger(s.port))) e['spec.port'] = 'Use a port from 1 to 65535.';
  if (s.publish && !s.port) e['spec.port'] = 'Set the port to publish.';
  const mem = s.memoryMiB ?? RULES.appMemoryDefault;
  if (mem < RULES.appMemoryMin || mem > RULES.appMemoryMax) e['spec.memoryMiB'] = `Use ${RULES.appMemoryMin}–${RULES.appMemoryMax} MiB.`;
  const env = s.env ?? {};
  const senv = s.secretEnv ?? {};
  if (Object.keys(env).length + Object.keys(senv).length > RULES.envMaxEntries) e['spec.env'] = 'Use at most 100 environment variables.';
  for (const [k, v] of Object.entries(env)) {
    if (!envKeyPattern.test(k)) e[`spec.env.${k}`] = 'Variable names use letters, digits and underscores and cannot start with a digit.';
    else if (v.length > RULES.envValueMax) e[`spec.env.${k}`] = 'Values can be at most 4096 characters.';
  }
  for (const [k, name] of Object.entries(senv)) {
    if (!envKeyPattern.test(k)) e[`spec.secretEnv.${k}`] = 'Variable names use letters, digits and underscores and cannot start with a digit.';
    else if (k in env) e[`spec.secretEnv.${k}`] = 'This variable is already set as a plain variable.';
    else if (validateName(name)) e[`spec.secretEnv.${k}`] = 'Name an existing secret.';
  }
  if (s.strategy !== undefined && !(RULES.strategies as readonly string[]).includes(s.strategy)) e['spec.strategy'] = 'Choose rolling or recreate.';
  if (s.healthCheck) Object.assign(e, validateHealthCheck(s.healthCheck, s.port));
  Object.assign(e, validatePlacement(s.network, s.subnet));
  const replicas = s.replicas ?? RULES.replicasDefault;
  if (!inRange(replicas, RULES.replicasMin, RULES.replicasMax)) e['spec.replicas'] = `Use ${RULES.replicasMin}–${RULES.replicasMax} replicas.`;
  else if (replicas > 1 && s.publish) e['spec.replicas'] = 'An app that publishes a host port runs one replica. Use a load balancer or a domain to serve several.';
  return { ...e, ...validateMounts(s.volumes) };
}

/** The same rules as pkg/api validatePlacement; existence is checked by the server. */
export function validatePlacement(network: string | undefined, subnet: string | undefined): FieldErrors {
  const e: FieldErrors = {};
  if (network && validateName(network)) e['spec.network'] = 'Name an existing network.';
  if (subnet && !network) e['spec.subnet'] = 'Choose the network first.';
  return e;
}

/** The same rules as pkg/api validateGitSource. */
export function validateGitSource(g: GitSource): FieldErrors {
  const e: FieldErrors = {};
  if (!validGitUrl(g.git)) e['spec.source.git'] = 'Use an https:// or ssh:// repository URL, like https://github.com/acme/api.git.';
  if (!g.ref || g.ref.length > RULES.gitRefMax || /\s/.test(g.ref)) e['spec.source.ref'] = 'Enter a branch, tag or commit, like main.';
  const inside = (p: string) => p !== '' && !p.startsWith('/') && !p.includes('\\') && p.length <= 255 && !p.split('/').includes('..');
  if (!inside(g.dockerfile ?? RULES.gitDockerfileDefault)) e['spec.source.dockerfile'] = 'Use a path inside the repository, like docker/Dockerfile.';
  if (!inside(g.context ?? RULES.gitContextDefault)) e['spec.source.context'] = 'Use a path inside the repository, like docker/Dockerfile.';
  if (g.credentialsSecret && validateName(g.credentialsSecret)) e['spec.source.credentialsSecret'] = 'Name an existing secret.';
  const args = g.buildArgs ?? {};
  if (Object.keys(args).length > RULES.buildArgsMax) e['spec.source.buildArgs'] = 'Use at most 50 build arguments.';
  for (const k of Object.keys(args)) {
    if (!envKeyPattern.test(k)) e[`spec.source.buildArgs.${k}`] = 'Argument names use letters, digits and underscores and cannot start with a digit.';
  }
  return e;
}

/** https or ssh, or http for localhost only (pkg/api validateGitSource). */
function validGitUrl(raw: string): boolean {
  try {
    const u = new URL(raw);
    const local = u.hostname === 'localhost' || u.hostname === '127.0.0.1';
    return Boolean(u.host) && raw.length <= RULES.gitUrlMax && (u.protocol === 'https:' || u.protocol === 'ssh:' || (u.protocol === 'http:' && local));
  } catch {
    return false;
  }
}

const inRange = (v: number | undefined, lo: number, hi: number) => v === undefined || (Number.isInteger(v) && v >= lo && v <= hi);

/** The same rules as pkg/api validateHealthCheck, with the server's defaults applied first. */
export function validateHealthCheck(h: HealthCheck, appPort?: number): FieldErrors {
  const e: FieldErrors = {};
  const f = 'spec.healthCheck.';
  if (!(RULES.healthCheckTypes as readonly string[]).includes(h.type)) return { [f + 'type']: 'Choose http, tcp, command or none.' };
  if (h.type === 'http' || h.type === 'tcp') {
    if (h.port !== undefined && !inRange(h.port, RULES.portMin, RULES.portMax)) e[f + 'port'] = 'Use a port from 1 to 65535.';
    else if (!h.port && !appPort) e[f + 'port'] = "Set the app's port, or a port for the health check.";
    const path = h.path ?? '/';
    if (h.type === 'http' && (!path.startsWith('/') || path.length > RULES.healthPathMax || /\s/.test(path))) e[f + 'path'] = 'Use a path that starts with /, like /healthz.';
  }
  if (h.type === 'command' && !h.command?.length) e[f + 'command'] = 'Enter the command to run inside the container.';
  const interval = h.intervalSeconds ?? RULES.healthIntervalDefault;
  const timeout = h.timeoutSeconds ?? Math.min(RULES.healthTimeoutDefault, interval);
  const retries = h.retries ?? RULES.healthRetriesDefault;
  const start = h.startPeriodSeconds ?? 0;
  if (!inRange(interval, RULES.healthIntervalMin, RULES.healthIntervalMax)) e[f + 'intervalSeconds'] = `Use ${RULES.healthIntervalMin}–${RULES.healthIntervalMax} seconds.`;
  if (!inRange(timeout, RULES.healthTimeoutMin, RULES.healthTimeoutMax)) e[f + 'timeoutSeconds'] = `Use ${RULES.healthTimeoutMin}–${RULES.healthTimeoutMax} seconds.`;
  else if (timeout > interval) e[f + 'timeoutSeconds'] = "The timeout can't be longer than the interval.";
  if (!inRange(retries, RULES.healthRetriesMin, RULES.healthRetriesMax)) e[f + 'retries'] = `Use ${RULES.healthRetriesMin}–${RULES.healthRetriesMax} attempts.`;
  if (!inRange(start, 0, RULES.healthStartPeriodMax)) e[f + 'startPeriodSeconds'] = `Use 0–${RULES.healthStartPeriodMax} seconds.`;
  if (!Object.keys(e).length && start + retries * interval > RULES.healthBudgetMaxSeconds) e[f + 'retries'] = 'The start period plus retries × interval can be at most 15 minutes.';
  return e;
}

/** "HTTP /healthz every 5 s, up to 12 times" — how the console names a check. */
export function describeHealthCheck(h: HealthCheck | undefined, port?: number): string {
  if (!h) return port ? `Default: TCP on port ${port}` : 'Default: running for 5 s without restarting';
  const every = `every ${h.intervalSeconds ?? RULES.healthIntervalDefault} s, up to ${h.retries ?? RULES.healthRetriesDefault} times`;
  switch (h.type) {
    case 'http':
      return `HTTP ${h.path ?? '/'} ${every}`;
    case 'tcp':
      return `TCP on port ${h.port ?? port ?? '?'} ${every}`;
    case 'command':
      return `Command ${(h.command ?? []).join(' ')} ${every}`;
    case 'none':
      return 'None: running for 5 s without restarting';
  }
}

export function validateCache(s: CacheSpec): FieldErrors {
  const mem = s.maxMemoryMiB ?? RULES.cacheMemoryDefault;
  const e = validatePlacement(s.network, s.subnet);
  if (mem < RULES.cacheMemoryMin || mem > RULES.cacheMemoryMax) e['spec.maxMemoryMiB'] = `Use ${RULES.cacheMemoryMin}–${RULES.cacheMemoryMax} MiB.`;
  return e;
}

export function validateDatabase(s: DatabaseSpec): FieldErrors {
  const e = validatePlacement(s.network, s.subnet);
  const versions = (RULES.databaseEngineVersions as Record<string, readonly string[]>)[s.engine];
  if (!versions) e['spec.engine'] = 'Choose postgres, mysql, mongodb or clickhouse.';
  else if (!versions.includes(s.version)) e['spec.version'] = `Choose a ${s.engine} version: ${versions.join(' or ')}.`;
  return e;
}

export function validateSecret(s: SecretSpec): FieldErrors {
  if (s.source && s.value) return { 'spec.value': 'A secret has a value or a source in a secret store, not both.' };
  if (s.source) return validateSecretSource(s.source);
  if (!s.value) return { 'spec.value': "Enter the secret's value." };
  if (utf8Length(s.value) > RULES.secretValueMaxBytes) return { 'spec.value': 'Secrets can be at most 64 KiB.' };
  return {};
}

const hostLabel = new RegExp(RULES.hostLabelPattern);

/** The same rules as pkg/api ValidateHostname (L3-002 §6.5). Expects the normalized (lowercase) form. */
export function validateHostname(h: string): string | undefined {
  const msg = 'Use a hostname like api.example.com or web.localhost.';
  if (!h) return 'Enter the hostname, like api.example.com.';
  if (h.length > RULES.hostnameMax) return 'Hostnames can be at most 253 characters.';
  if (h.includes('*')) return "Wildcard hostnames aren't supported yet.";
  const labels = h.split('.');
  if (labels.length < 2 || labels.some((l) => !hostLabel.test(l))) return msg;
  if (labels.every((l) => /^[0-9]+$/.test(l))) return 'Use a hostname, not an IP address.';
  return undefined;
}

export function validateDomain(s: DomainSpec): FieldErrors {
  const e: FieldErrors = {};
  const hostname = (s.hostname ?? '').trim().toLowerCase().replace(/\.$/, '');
  // A leading wildcard label is allowed only when the certificate comes through a zone (CRT-02).
  const h = validateHostname(s.zone && hostname.startsWith('*.') ? hostname.slice(2) : hostname);
  if (h) e['spec.hostname'] = h;
  if (s.zone && s.tls && s.tls !== 'auto') e['spec.tls'] = 'A domain that gets its certificate through DNS uses tls auto.';
  if (!s.app) e['spec.app'] = 'Choose the app this domain serves.';
  if (s.port !== undefined && (!Number.isInteger(s.port) || s.port < RULES.portMin || s.port > RULES.portMax)) e['spec.port'] = 'Use a port from 1 to 65535.';
  if (s.tls !== undefined && !(RULES.tlsModes as readonly string[]).includes(s.tls)) e['spec.tls'] = 'Choose auto, internal or off.';
  else if (s.tls === 'auto' && hostname.endsWith(RULES.localhostSuffix)) e['spec.tls'] = "Public certificates can't be issued for .localhost names. Use internal.";
  return e;
}

function validTimeZone(tz: string): boolean {
  try {
    new Intl.DateTimeFormat('en', { timeZone: tz });
    return true;
  } catch {
    return false;
  }
}

/** The same rules as pkg/api validateJob. */
export function validateJob(s: JobSpec): FieldErrors {
  const e: FieldErrors = {};
  if (!s.schedule?.trim()) e['spec.schedule'] = 'Enter a schedule like 0 3 * * * or @daily.';
  else {
    try {
      parseCron(s.schedule);
    } catch (err) {
      e['spec.schedule'] = `Use 5 cron fields (minute hour day month weekday) or @hourly, @daily, @weekly, @monthly: ${(err as Error).message}.`;
    }
  }
  if (s.timeZone && !validTimeZone(s.timeZone)) e['spec.timeZone'] = 'Use an IANA time zone like Asia/Kolkata or Europe/Berlin.';
  if (Boolean(s.app) === Boolean(s.image)) e['spec.app'] = 'Run the job with an app\'s image, or with its own image — choose one.';
  else if (s.app && validateName(s.app)) e['spec.app'] = 'Name an existing app.';
  else if (s.image && !imagePattern.test(s.image)) e['spec.image'] = 'Use an image reference like nginx:1.27 or ghcr.io/acme/api:1.4.';
  const timeout = s.timeoutSeconds ?? RULES.jobTimeoutDefault;
  if (!Number.isInteger(timeout) || timeout < RULES.jobTimeoutMin || timeout > RULES.jobTimeoutMax) e['spec.timeoutSeconds'] = 'Use 1 second to 24 hours (86400).';
  if (s.overlap !== undefined && !(RULES.overlapPolicies as readonly string[]).includes(s.overlap)) e['spec.overlap'] = 'Choose forbid or allow.';
  const mem = s.memoryMiB ?? RULES.appMemoryDefault;
  if (mem < RULES.appMemoryMin || mem > RULES.appMemoryMax) e['spec.memoryMiB'] = `Use ${RULES.appMemoryMin}–${RULES.appMemoryMax} MiB.`;
  const envErrors = validateApp({ image: 'x', env: s.env, secretEnv: s.secretEnv });
  for (const [k, v] of Object.entries(envErrors)) if (k.startsWith('spec.env') || k.startsWith('spec.secretEnv')) e[k] = v;
  if (s.app && s.network) e['spec.network'] = "A job that runs with an app's image runs in that app's network.";
  else Object.assign(e, validatePlacement(s.network, s.subnet));
  return { ...e, ...validateMounts(s.volumes) };
}

const prefixPattern = new RegExp(RULES.backupPrefixPattern);
const s3BucketPattern = new RegExp(RULES.s3BucketPattern);

/** The same rules as pkg/api validateBackup, with the server's defaults applied first. */
export function validateBackup(s: BackupSpec): FieldErrors {
  const e: FieldErrors = {};
  const src = s.source ?? {};
  if (Boolean(src.database) === Boolean(src.cache)) e['spec.source'] = 'Back up one database or one cache.';
  else if (validateName(src.database ?? src.cache ?? '')) e['spec.source'] = 'Name an existing database or cache.';
  const d = s.destination ?? {};
  if (Boolean(d.bucket) === Boolean(d.s3)) e['spec.destination'] = 'Store backups in one EnclaveStack bucket or one external S3 bucket.';
  else if (d.bucket && validateName(d.bucket)) e['spec.destination.bucket'] = 'Name an existing bucket.';
  else if (d.s3) {
    if (!validEndpoint(d.s3.endpoint)) e['spec.destination.s3.endpoint'] = 'Use an https:// endpoint like https://s3.eu-central-1.amazonaws.com (http only for localhost).';
    if (!s3BucketPattern.test(d.s3.bucket ?? '')) e['spec.destination.s3.bucket'] = 'Use an S3 bucket name: 3–63 lowercase letters, digits, dots or hyphens.';
    if (validateName(d.s3.credentialsSecret ?? '')) e['spec.destination.s3.credentialsSecret'] = 'Name the secret holding {"accessKey","secretKey"}.';
  }
  const prefix = s.prefix ?? RULES.backupPrefixDefault;
  if (!prefixPattern.test(prefix) || prefix.includes('..') || prefix.includes('//')) e['spec.prefix'] = 'Use lowercase letters, digits, /, _ or -, up to 100 characters.';
  if (s.timeZone && !validTimeZone(s.timeZone)) e['spec.timeZone'] = 'Use an IANA time zone like Asia/Kolkata or Europe/Berlin.';
  try {
    parseCron(s.schedule || RULES.backupScheduleDefault);
  } catch (err) {
    e['spec.schedule'] = `Use 5 cron fields (minute hour day month weekday) or @hourly, @daily, @weekly, @monthly: ${(err as Error).message}.`;
  }
  if (!inRange(s.keep ?? RULES.backupKeepDefault, RULES.backupKeepMin, RULES.backupKeepMax)) e['spec.keep'] = 'Keep 1–365 backups.';
  if (!inRange(s.keepDays ?? 0, 0, RULES.backupKeepDaysMax)) e['spec.keepDays'] = 'Use 1–3650 days, or leave empty.';
  if (!inRange(s.timeoutSeconds ?? RULES.backupTimeoutDefault, RULES.backupTimeoutMin, RULES.backupTimeoutMax)) e['spec.timeoutSeconds'] = 'Use 60 seconds to 24 hours (86400).';
  return e;
}

/** https, or http for localhost only; no path (pkg/api validateBackup). */
function validEndpoint(raw: string | undefined): boolean {
  try {
    const u = new URL(raw ?? '');
    const local = u.hostname === 'localhost' || u.hostname === '127.0.0.1';
    return Boolean(u.host) && (u.protocol === 'https:' || (u.protocol === 'http:' && local)) && (u.pathname === '/' || u.pathname === '') && !u.search;
  } catch {
    return false;
  }
}

// v0.4 kinds (L3-002 §7.3), the same rules as pkg/api/networking.go.

/** A canonical IPv4 prefix: [address as a 32-bit number, prefix length], or undefined. */
export function parseCidr(raw: string): [number, number] | undefined {
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})\/(\d{1,2})$/.exec(raw.trim());
  if (!m) return undefined;
  const parts = m.slice(1, 5).map((o) => o ?? '');
  const [a = 0, b = 0, c = 0, d = 0] = parts.map(Number);
  const bits = Number(m[5]);
  if ([a, b, c, d].some((o) => o > 255) || bits > 32 || parts.some((o) => o.length > 1 && o.startsWith('0'))) return undefined;
  const addr = ((a << 24) >>> 0) + (b << 16) + (c << 8) + d;
  const mask = bits === 0 ? 0 : (0xffffffff << (32 - bits)) >>> 0;
  return (addr & mask) >>> 0 === addr ? [addr, bits] : undefined;
}

const contains = (outer: [number, number], inner: [number, number]) => {
  const mask = outer[1] === 0 ? 0 : (0xffffffff << (32 - outer[1])) >>> 0;
  return outer[1] <= inner[1] && ((inner[0] & mask) >>> 0) === outer[0];
};
const overlaps = (a: [number, number], b: [number, number]) => contains(a, b) || contains(b, a);
const PRIVATE = ['10.0.0.0/8', '172.16.0.0/12', '192.168.0.0/16', '100.64.0.0/10'].map((c) => parseCidr(c) as [number, number]);

/** How many addresses a prefix holds, for the subnet editor. */
export const cidrSize = (bits: number) => 2 ** (32 - bits);

export function validateNetwork(s: NetworkSpec, others: string[] = []): FieldErrors {
  const e: FieldErrors = {};
  const cidr = parseCidr(s.cidr ?? '');
  if (!s.cidr) return { 'spec.cidr': "Enter the network's address range, like 10.40.0.0/16." };
  if (!cidr) return { 'spec.cidr': 'Use an IPv4 range like 10.40.0.0/16, with the host part zero.' };
  if (cidr[1] < RULES.networkPrefixMin || cidr[1] > RULES.networkPrefixMax) return { 'spec.cidr': 'Use a range from /16 (65,536 addresses) to /28 (16 addresses).' };
  if (!PRIVATE.some((p) => contains(p, cidr))) return { 'spec.cidr': 'Use a private range: inside 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16 or 100.64.0.0/10.' };
  for (const o of others) {
    const oc = parseCidr(o);
    if (oc && overlaps(cidr, oc)) return { 'spec.cidr': `Overlaps another network (${o}). Choose a range that doesn't.` };
  }
  const subnets = s.subnets ?? [];
  if (subnets.length > RULES.subnetsMax) e['spec.subnets'] = `Use at most ${RULES.subnetsMax} subnets.`;
  const seen: [number, number][] = [];
  const names = new Set<string>();
  subnets.forEach((sub, i) => {
    const f = `spec.subnets[${i}]`;
    if (validateName(sub.name)) e[`${f}.name`] = NAME_RULE;
    else if (names.has(sub.name)) e[`${f}.name`] = 'Another subnet already has this name.';
    names.add(sub.name);
    const p = parseCidr(sub.cidr);
    if (!p) e[`${f}.cidr`] = 'Use an IPv4 range like 10.40.1.0/24, with the host part zero.';
    else if (p[1] < RULES.subnetPrefixMin || p[1] > RULES.subnetPrefixMax) e[`${f}.cidr`] = 'Use a range from /16 to /29.';
    else if (!contains(cidr, p)) e[`${f}.cidr`] = `The subnet must lie inside the network's range ${s.cidr}.`;
    else {
      const j = seen.findIndex((o) => overlaps(o, p));
      if (j >= 0) e[`${f}.cidr`] = `Overlaps subnet ${subnets[j]?.name ?? ''}.`;
    }
    if (p) seen.push(p);
  });
  return e;
}

/** "any", a CIDR (or one address), network/<name> or a resource <kind>/<name> (pkg/api ParsePeer). */
export function validPeer(p: string): boolean {
  if (p === RULES.peerAny) return true;
  const [kind = '', name, extra] = p.split('/');
  if (extra === undefined && name !== undefined && ['network', 'app', 'database', 'cache', 'loadbalancer'].includes(kind) && !validateName(name)) return true;
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(p)) return parseCidr(`${p}/32`) !== undefined;
  return parseCidr(p) !== undefined;
}

/** "80" or "8000-8100" (pkg/api ParsePortRange). */
export function validPortRange(p: string): boolean {
  const m = /^([1-9]\d{0,4})(?:-([1-9]\d{0,4}))?$/.exec(p);
  if (!m) return false;
  const lo = Number(m[1]);
  const hi = m[2] ? Number(m[2]) : lo;
  return lo <= RULES.portMax && hi <= RULES.portMax && lo <= hi;
}

function validateRules(rules: FirewallRule[], field: string, ingress: boolean): FieldErrors {
  const e: FieldErrors = {};
  if (rules.length > RULES.firewallRulesMax) e[field] = `Use at most ${RULES.firewallRulesMax} rules.`;
  rules.forEach((r, i) => {
    const f = `${field}[${i}]`;
    const peers = (ingress ? r.from : r.to) ?? [];
    const peerField = `${f}.${ingress ? 'from' : 'to'}`;
    if (!peers.length) e[peerField] = 'Add at least one: any, a CIDR, network/<name> or a resource like app/api.';
    else if (peers.length > RULES.firewallPeersMax) e[peerField] = `Use at most ${RULES.firewallPeersMax} entries.`;
    peers.forEach((p, j) => {
      if (!validPeer(p)) e[`${peerField}[${j}]`] = 'Use any, an IPv4 CIDR like 10.0.0.0/8, network/<name> or a resource like app/api.';
    });
    const protocol = r.protocol ?? 'tcp';
    if (!(RULES.protocols as readonly string[]).includes(protocol)) e[`${f}.protocol`] = 'Choose tcp, udp, icmp or all.';
    if (protocol === 'icmp' && r.ports?.length) e[`${f}.ports`] = 'ICMP has no ports.';
    (r.ports ?? []).forEach((p, j) => {
      if (!validPortRange(p)) e[`${f}.ports[${j}]`] = 'Use a port like 5432 or a range like 8000-8100.';
    });
  });
  return e;
}

export function validateFirewall(s: FirewallSpec): FieldErrors {
  const e: FieldErrors = {};
  const targets = s.targets ?? [];
  if (!targets.length) e['spec.targets'] = 'Choose the resources this firewall protects.';
  else if (targets.length > RULES.firewallTargetsMax) e['spec.targets'] = `Use at most ${RULES.firewallTargetsMax} targets.`;
  targets.forEach((t, i) => {
    const [kind = '', name] = t.split('/');
    if (!(RULES.firewallKinds as readonly string[]).includes(kind ?? '') || validateName(name ?? '')) e[`spec.targets[${i}]`] = 'Name an app, database, cache, load balancer, VM, search service or email service like app/api.';
    else if (targets.indexOf(t) !== i) e[`spec.targets[${i}]`] = `${t} is listed twice.`;
  });
  const egressDefault = s.egressDefault ?? 'allow';
  if (s.egress?.length && egressDefault !== 'deny') e['spec.egress'] = 'Outgoing rules apply only when other outgoing traffic is denied.';
  return { ...e, ...validateRules(s.ingress ?? [], 'spec.ingress', true), ...validateRules(s.egress ?? [], 'spec.egress', false) };
}

export function validateLoadBalancer(s: LoadBalancerSpec): FieldErrors {
  const e: FieldErrors = {};
  if (!inRange(s.port, RULES.portMin, RULES.portMax) || !s.port) e['spec.port'] = 'Use a port from 1 to 65535.';
  else if (s.port === 61999) e['spec.port'] = "Port 61999 is reserved for the load balancer's statistics. Choose another.";
  const targets = s.targets ?? [];
  if (!targets.length) e['spec.targets'] = 'Choose at least one app to send traffic to.';
  else if (targets.length > RULES.lbTargetsMax) e['spec.targets'] = `Use at most ${RULES.lbTargetsMax} apps.`;
  targets.forEach((t, i) => {
    const f = `spec.targets[${i}]`;
    if (validateName(t.app)) e[`${f}.app`] = 'Name an existing app.';
    else if (targets.findIndex((o) => o.app === t.app) !== i) e[`${f}.app`] = `The app ${t.app} is listed twice.`;
    if (t.port !== undefined && !inRange(t.port, RULES.portMin, RULES.portMax)) e[`${f}.port`] = 'Use a port from 1 to 65535.';
    if (!inRange(t.weight ?? RULES.lbWeightDefault, RULES.lbWeightMin, RULES.lbWeightMax)) e[`${f}.weight`] = `Use a weight from ${RULES.lbWeightMin} to ${RULES.lbWeightMax}.`;
  });
  const h = s.healthCheck;
  if (h?.type === 'http' && h.path !== undefined && (!h.path.startsWith('/') || /\s/.test(h.path))) e['spec.healthCheck.path'] = 'Use a path that starts with /, like /healthz.';
  return e;
}

export function validateZone(s: ZoneSpec): FieldErrors {
  const e: FieldErrors = {};
  if (!(RULES.dnsProviders as readonly string[]).includes(s.provider)) e['spec.provider'] = 'Choose cloudflare or route53.';
  const zone = (s.zone ?? '').trim().toLowerCase().replace(/\.$/, '');
  if (!zone) e['spec.zone'] = "Enter the zone's name, like example.com.";
  else {
    const h = validateHostname(zone);
    if (h) e['spec.zone'] = h;
  }
  if (validateName(s.credentialsSecret ?? '')) e['spec.credentialsSecret'] = "Name the secret holding the provider's credentials.";
  return e;
}

const recordLabel = new RegExp(RULES.recordLabelPattern);
const ipv4 = (v: string) => /^\d{1,3}(\.\d{1,3}){3}$/.test(v) && v.split('.').every((o) => Number(o) <= 255 && !(o.length > 1 && o.startsWith('0')));
const ipv6 = (v: string) => v.includes(':') && /^[0-9a-fA-F:]+$/.test(v) && v.split('::').length <= 2;

export function validateRecord(s: RecordSpec): FieldErrors {
  const e: FieldErrors = {};
  if (validateName(s.zone ?? '')) e['spec.zone'] = 'Choose the zone this record belongs to.';
  const name = (s.name ?? '').trim().toLowerCase();
  if (name !== RULES.recordApex) {
    const rest = name.startsWith('*.') ? name.slice(2) : name;
    if (!name || name.length > RULES.hostnameMax || (name !== '*' && rest.split('.').some((l) => !recordLabel.test(l))))
      e['spec.name'] = 'Use a name inside the zone like api, www.api or *.dev, or @ for the zone itself.';
  }
  const type = (s.type ?? '').toUpperCase();
  if (!(RULES.recordTypes as readonly string[]).includes(type)) return { ...e, 'spec.type': 'Choose A, AAAA, CNAME or TXT.' };
  const values = s.values ?? [];
  if (Boolean(s.machine) === (values.length > 0)) e['spec.values'] = 'Enter the record’s values, or point it at this machine — choose one.';
  else if (s.machine && type !== 'A' && type !== 'AAAA') e['spec.machine'] = 'Only A and AAAA records can point at this machine.';
  else if (values.length > RULES.recordValuesMax) e['spec.values'] = `Use at most ${RULES.recordValuesMax} values.`;
  else if (type === 'CNAME' && values.length !== 1) e['spec.values'] = 'A CNAME record has exactly one value.';
  else if (type === 'CNAME' && name === RULES.recordApex) e['spec.name'] = "The zone itself (@) can't be a CNAME.";
  values.forEach((v, i) => {
    const f = `spec.values[${i}]`;
    if (type === 'A' && !ipv4(v)) e[f] = 'Use an IPv4 address like 203.0.113.7.';
    if (type === 'AAAA' && !ipv6(v)) e[f] = 'Use an IPv6 address like 2001:db8::7.';
    if (type === 'CNAME' && validateHostname(v.toLowerCase().replace(/\.$/, ''))) e[f] = 'Use a hostname like app.example.net.';
    if (type === 'TXT' && (!v || v.length > RULES.txtValueMax || /["\r\n]/.test(v))) e[f] = 'Use 1–255 characters of text, without quotes or line breaks.';
  });
  if (!inRange(s.ttl ?? RULES.recordTtlDefault, RULES.recordTtlMin, RULES.recordTtlMax)) e['spec.ttl'] = 'Use a TTL from 60 seconds to 1 day (86400).';
  return e;
}

/** Buckets have no field that can be invalid (versioning is a boolean). */
export function validateBucket(): FieldErrors {
  return {};
}

export function validateSpec(kind: Kind, spec: unknown): FieldErrors {
  const own = validateKind(kind, spec);
  return (RULES.placedKinds as readonly Kind[]).includes(kind) ? { ...validatePlaced(spec as Placed), ...own } : own;
}

const labelKey = new RegExp(RULES.labelKeyPattern);
const labelValue = new RegExp(RULES.labelValuePattern);

/** Machine labels, as pkg/api ValidateLabels: up to 10, key and value patterns (L3-002 §11.3). */
export function validateLabels(labels: Record<string, string> | undefined, field: string): FieldErrors {
  const e: FieldErrors = {};
  const entries = Object.entries(labels ?? {});
  if (entries.length > RULES.machineLabelsMax) e[field] = `Use at most ${RULES.machineLabelsMax} labels.`;
  if (e[field]) return e;
  for (const [k, v] of entries) {
    if (!labelKey.test(k)) e[`${field}.${k}`] = "A label's key is lowercase letters, digits, dots, hyphens and underscores, up to 63 characters, starting with a letter or digit.";
    else if (!labelValue.test(v)) e[`${field}.${k}`] = "A label's value is letters, digits, dots, hyphens and underscores, up to 63 characters.";
  }
  return e;
}

/** A placed kind's machine and labels (ADR-044). */
export function validatePlaced(p: Placed): FieldErrors {
  const e = validateLabels(p.machineLabels, 'spec.machineLabels');
  const bad = p.machine ? validateName(p.machine) : undefined;
  if (bad) e['spec.machine'] = `Name a machine: ${bad}`;
  return e;
}

/** A stack names exactly one of a catalogue entry and a template body (L3-002 §11.7). */
export function validateStack(s: StackSpec): FieldErrors {
  const e: FieldErrors = {};
  if (Boolean(s.template) === Boolean(s.templateBody)) e['spec.template'] = 'Choose a catalogue entry, or give a template of your own.';
  return e;
}

function validateKind(kind: Kind, spec: unknown): FieldErrors {
  switch (kind) {
    case 'stack':
      return validateStack(spec as StackSpec);
    case 'app':
      return validateApp(spec as AppSpec);
    case 'database':
      return validateDatabase(spec as DatabaseSpec);
    case 'cache':
      return validateCache(spec as CacheSpec);
    case 'bucket':
      return validateBucket();
    case 'secret':
      return validateSecret(spec as SecretSpec);
    case 'domain':
      return validateDomain(spec as DomainSpec);
    case 'job':
      return validateJob(spec as JobSpec);
    case 'backup':
      return validateBackup(spec as BackupSpec);
    case 'network':
      return validateNetwork(spec as NetworkSpec);
    case 'firewall':
      return validateFirewall(spec as FirewallSpec);
    case 'loadbalancer':
      return validateLoadBalancer(spec as LoadBalancerSpec);
    case 'zone':
      return validateZone(spec as ZoneSpec);
    case 'record':
      return validateRecord(spec as RecordSpec);
    case 'vm':
      return validateVM(spec as VMSpec);
    case 'volume':
      return validateVolume(spec as VolumeSpec);
    case 'snapshot':
      return validateSnapshot(spec as SnapshotSpec);
    case 'queue':
      return validateQueue(spec as QueueSpec);
    case 'topic':
      return validateTopic(spec as TopicSpec);
    case 'search':
      return validateSearch(spec as SearchSpec);
    case 'transcoder':
      return validateTranscoder(spec as TranscoderSpec);
    case 'cdn':
      return validateCDN(spec as CDNSpec);
    case 'email':
      return validateEmailService(spec as EmailSpec);
    case 'dashboard':
      return validateDashboard(spec as DashboardSpec);
    case 'alert':
      return validateAlert(spec as AlertSpec);
    case 'channel':
      return validateChannel(spec as ChannelSpec);
    case 'logexport':
      return validateLogExport(spec as LogExportSpec);
    case 'secretstore':
      return validateSecretStore(spec as SecretStoreSpec);
  }
}

// v0.6 (L3-002 §9.4), the same rules as pkg/api data.go.

export function validateQueue(s: QueueSpec): FieldErrors {
  const e: FieldErrors = {};
  if (!inRange(s.visibilityTimeoutSeconds, RULES.queueVisibilityMin, RULES.queueVisibilityMax)) e['spec.visibilityTimeoutSeconds'] = 'Use 1 second to 12 hours (43200 seconds).';
  if (!inRange(s.retentionHours, RULES.retentionHoursMin, RULES.retentionHoursMax)) e['spec.retentionHours'] = 'Use 1–336 hours (two weeks).';
  if (!inRange(s.maxMessageBytes, RULES.messageBytesMin, RULES.messageBytesMax)) e['spec.maxMessageBytes'] = 'Use 1 KiB to 1 MiB (1024–1048576 bytes).';
  if (!inRange(s.maxReceives, 0, RULES.maxReceivesMax)) e['spec.maxReceives'] = 'Use 1–1000, or 0 for no limit.';
  if (s.deadLetterQueue) {
    if (validateName(s.deadLetterQueue)) e['spec.deadLetterQueue'] = 'Name an existing queue.';
    else if (!s.maxReceives) e['spec.maxReceives'] = 'Set how many receives move a message to the dead-letter queue.';
  }
  return e;
}

/** https:// anywhere, http:// only to localhost (TOP-03). */
export function validWebhookUrl(raw: string): boolean {
  try {
    const u = new URL(raw);
    if (raw.length > RULES.webhookUrlMax || u.username || u.password || u.hash) return false;
    return u.protocol === 'https:' || (u.protocol === 'http:' && (u.hostname === 'localhost' || u.hostname === '127.0.0.1'));
  } catch {
    return false;
  }
}

export function validateTopic(s: TopicSpec): FieldErrors {
  const e: FieldErrors = {};
  if (!inRange(s.retentionHours, RULES.retentionHoursMin, RULES.retentionHoursMax)) e['spec.retentionHours'] = 'Use 1–336 hours (two weeks).';
  const subs = s.subscriptions ?? [];
  if (subs.length > RULES.subscriptionsMax) e['spec.subscriptions'] = `Use at most ${RULES.subscriptionsMax} subscriptions.`;
  const seen = new Set<string>();
  subs.forEach((sub, i) => {
    const f = `spec.subscriptions[${i}]`;
    if (Boolean(sub.queue) === Boolean(sub.url)) {
      e[f] = 'Subscribe a queue or a webhook URL — choose one.';
      return;
    }
    if (sub.queue && validateName(sub.queue)) e[`${f}.queue`] = 'Name an existing queue.';
    if (sub.queue && sub.secret) e[`${f}.secret`] = 'Only webhooks are signed.';
    if (sub.url && !validWebhookUrl(sub.url.trim())) e[`${f}.url`] = 'Use an https:// URL (http:// only for localhost), up to 2048 characters.';
    const target = sub.queue ? `queue/${sub.queue}` : (sub.url ?? '').trim();
    if (seen.has(target)) e[f] = 'This subscription is listed twice.';
    seen.add(target);
  });
  return e;
}

export function validateSearch(s: SearchSpec): FieldErrors {
  const e = validatePlacement(s.network, s.subnet);
  if (s.version !== undefined && !(RULES.searchVersions as readonly string[]).includes(s.version)) e['spec.version'] = 'Choose version 3 or 2.';
  const m = s.memoryMiB;
  if (m !== undefined && (!inRange(m, RULES.searchMemoryMin, RULES.searchMemoryMax) || m % RULES.searchMemoryStep !== 0))
    e['spec.memoryMiB'] = `Use ${RULES.searchMemoryMin}–${RULES.searchMemoryMax} MiB in steps of ${RULES.searchMemoryStep}.`;
  return e;
}

/** A folder as the server stores it: "" or "a/b/". */
export function normalizePrefix(p: string | undefined): string {
  const t = (p ?? '').trim().replace(/^\/+/, '');
  return t && !t.endsWith('/') ? `${t}/` : t;
}

const prefixChars = /^[A-Za-z0-9!_.*'()/ +=,@-]*$/;

export function validateTranscoder(s: TranscoderSpec): FieldErrors {
  const e: FieldErrors = {};
  const input = normalizePrefix(s.input?.prefix);
  const output = normalizePrefix(s.output?.prefix || RULES.transcodeOutputDefault);
  for (const [field, f, p] of [['spec.input', s.input, input], ['spec.output', s.output, output]] as const) {
    if (!f?.bucket) e[`${field}.bucket`] = 'Choose a bucket.';
    if (p.length > RULES.prefixMax || !prefixChars.test(p) || p.split('/').includes('..') || p.includes('//'))
      e[`${field}.prefix`] = 'Use a folder like videos/ or media/2026/: letters, digits and - _ . / only, up to 255 characters.';
  }
  if (s.input?.bucket && s.input.bucket === s.output?.bucket && (input.startsWith(output) || output.startsWith(input)))
    e['spec.output.prefix'] = "On the same bucket, the output folder can't be inside the input folder or contain it; the transcoder would convert its own files.";
  if (!s.presets?.length) e['spec.presets'] = 'Choose at least one preset.';
  if (!inRange(s.timeoutSeconds, RULES.transcodeTimeoutMin, RULES.transcodeTimeoutMax)) e['spec.timeoutSeconds'] = 'Use 60 seconds to 24 hours.';
  if (!inRange(s.concurrency, RULES.concurrencyMin, RULES.concurrencyMax)) e['spec.concurrency'] = 'Use 1–4 files at once.';
  if (!inRange(s.memoryMiB, RULES.transcodeMemoryMin, RULES.transcodeMemoryMax)) e['spec.memoryMiB'] = `Use ${RULES.transcodeMemoryMin}–${RULES.transcodeMemoryMax} MiB.`;
  return e;
}

export function validateCDN(s: CDNSpec): FieldErrors {
  const e: FieldErrors = {};
  const hostname = (s.hostname ?? '').trim().toLowerCase().replace(/\.$/, '');
  const h = validateHostname(hostname);
  if (h) e['spec.hostname'] = h;
  if (s.zone && s.tls && s.tls !== 'auto') e['spec.tls'] = 'A domain that gets its certificate through DNS uses tls auto.';
  else if (s.tls === 'auto' && hostname.endsWith(RULES.localhostSuffix)) e['spec.tls'] = "Public certificates can't be issued for .localhost names. Use internal.";
  const [kind, name] = (s.origin ?? '').split('/');
  if (!s.origin) e['spec.origin'] = 'Choose what the CDN serves: app/<name> or bucket/<name>.';
  else if ((kind !== 'app' && kind !== 'bucket') || validateName(name ?? '')) e['spec.origin'] = 'Use app/<name> or bucket/<name>.';
  else if (kind === 'bucket' && s.originPort) e['spec.originPort'] = 'Only an app origin has a port.';
  if (s.originPort !== undefined && !inRange(s.originPort, RULES.portMin, RULES.portMax)) e['spec.originPort'] = 'Use a port from 1 to 65535.';
  if (!inRange(s.defaultTtlSeconds, RULES.cdnTtlMin, RULES.cdnTtlMax)) e['spec.defaultTtlSeconds'] = 'Use 0 seconds to a year (31536000 seconds).';
  if (!inRange(s.maxSizeMiB, RULES.cdnSizeMin, RULES.cdnSizeMax)) e['spec.maxSizeMiB'] = `Use ${RULES.cdnSizeMin}–${RULES.cdnSizeMax} MiB.`;
  return e;
}

export function validateEmailService(s: EmailSpec): FieldErrors {
  const e = validatePlacement(s.network, s.subnet);
  const mode = s.mode ?? 'capture';
  if (mode === 'relay' && !s.relay) e['spec.relay'] = "Give the relay's host and credentials secret.";
  if (mode === 'capture' && s.relay) e['spec.relay'] = "A capturing email service doesn't relay; choose relay mode to use one.";
  if (s.relay) {
    const host = s.relay.host.trim();
    if (!host || (validateHostname(host) && !/^\d{1,3}(\.\d{1,3}){3}$/.test(host))) e['spec.relay.host'] = "Use the provider's SMTP host, like smtp.postmarkapp.com.";
    if (!inRange(s.relay.port, RULES.portMin, RULES.portMax)) e['spec.relay.port'] = 'Use a port from 1 to 65535.';
    if (!s.relay.credentialsSecret) e['spec.relay.credentialsSecret'] = 'Name a secret holding {"username": "…", "password": "…"}.';
  }
  if (s.domain && validateHostname(s.domain.trim().toLowerCase())) e['spec.domain'] = 'Use a domain like example.com.';
  if (!inRange(s.keepMessages, RULES.keepMessagesMin, RULES.keepMessagesMax)) e['spec.keepMessages'] = `Keep ${RULES.keepMessagesMin}–${RULES.keepMessagesMax} messages.`;
  return e;
}

// v0.5 (L3-002 §8.4), the same rules as pkg/api compute.go. Key formats are checked by shape here; the
// server parses them fully (and refuses short RSA keys).

const SSH_KEY = /^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp(256|384|521)|sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com) [A-Za-z0-9+/]+={0,3}( .*)?$/;
const SHA256 = /^[a-f0-9]{64}$/;

function validImageUrl(raw: string): boolean {
  try {
    const u = new URL(raw);
    return u.protocol === 'https:' && Boolean(u.host);
  } catch {
    return false;
  }
}

/** The serial a disk shows inside its VM: its name cut to 20 characters (VOL-01). */
export const diskSerial = (name: string) => name.slice(0, RULES.diskSerialLen);

export function validateSSHKeys(keys: string[], field: string): FieldErrors {
  const e: FieldErrors = {};
  if (!keys.length) e[field] = 'Add at least one SSH public key, like the contents of ~/.ssh/id_ed25519.pub.';
  else if (keys.length > RULES.sshKeysMax) e[field] = `Use at most ${RULES.sshKeysMax} SSH keys.`;
  keys.forEach((k, i) => {
    if (k.includes('PRIVATE KEY')) e[`${field}[${i}]`] = 'This is a private key. Paste the public key instead (the .pub file); never share the private one.';
    else if (k.length > RULES.sshKeyMax || !SSH_KEY.test(k)) e[`${field}[${i}]`] = 'Paste a public key in OpenSSH format, like ssh-ed25519 AAAA… you@laptop (the .pub file, never the private key).';
  });
  return e;
}

export function validateVM(s: VMSpec): FieldErrors {
  const e: FieldErrors = {};
  const image = s.image?.trim() ?? '';
  const snapshot = image.startsWith(RULES.snapshotImagePrefix) ? image.slice(RULES.snapshotImagePrefix.length) : undefined;
  if (!image) e['spec.image'] = 'Choose an image: ubuntu-24.04, debian-12, an https:// URL or snapshot/<name>.';
  else if (snapshot !== undefined) {
    if (validateName(snapshot)) e['spec.image'] = 'Name an existing snapshot, like snapshot/worker-1.';
  } else if (image.startsWith('https://')) {
    if (image.length > RULES.imageUrlMax || !validImageUrl(image)) e['spec.image'] = 'Use an https:// address of a cloud image (qcow2 or raw), up to 2048 characters.';
  } else if (!(RULES.imageCatalog as readonly string[]).includes(image)) e['spec.image'] = 'Choose ubuntu-24.04, debian-12, an https:// URL of a cloud image, or snapshot/<name>.';
  if (s.imageSha256) {
    if (!image.startsWith('https://')) e['spec.imageSha256'] = 'Only an image from a URL takes a checksum; catalog images and snapshots are checked already.';
    else if (!SHA256.test(s.imageSha256)) e['spec.imageSha256'] = "Use the image's SHA-256: 64 hexadecimal characters.";
  }
  if (s.sshKeys?.length && s.sshKeySecret) e['spec.sshKeys'] = 'Give the SSH keys here or in a secret — choose one.';
  else if (s.sshKeySecret) {
    if (validateName(s.sshKeySecret)) e['spec.sshKeySecret'] = 'Name an existing secret.';
  } else Object.assign(e, validateSSHKeys(s.sshKeys ?? [], 'spec.sshKeys'));
  if (s.userData) {
    if (utf8Length(s.userData) > RULES.userDataMaxBytes) e['spec.userData'] = 'The first-boot script can be at most 64 KiB.';
    else if (!s.userData.startsWith('#cloud-config') && !s.userData.startsWith('#!')) e['spec.userData'] = 'Start the first-boot script with #cloud-config or a #! line, like #!/bin/sh.';
  }
  if (!inRange(s.cpus ?? RULES.vmCpusDefault, RULES.vmCpusMin, RULES.vmCpusMax)) e['spec.cpus'] = `Use ${RULES.vmCpusMin}–${RULES.vmCpusMax} CPUs.`;
  const mem = s.memoryMiB ?? RULES.vmMemoryDefault;
  if (!inRange(mem, RULES.vmMemoryMin, RULES.vmMemoryMax) || mem % RULES.vmMemoryStep !== 0) e['spec.memoryMiB'] = `Use ${RULES.vmMemoryMin}–${RULES.vmMemoryMax} MiB in steps of ${RULES.vmMemoryStep}.`;
  if (!inRange(s.diskGiB ?? RULES.vmDiskDefault, RULES.vmDiskMin, RULES.vmDiskMax)) e['spec.diskGiB'] = `Use ${RULES.vmDiskMin}–${RULES.vmDiskMax} GiB.`;
  Object.assign(e, validatePlacement(s.network, s.subnet));
  const disks = s.disks ?? [];
  if (disks.length > RULES.vmDisksMax) e['spec.disks'] = `Attach at most ${RULES.vmDisksMax} disks.`;
  disks.forEach((d, i) => {
    const first = disks.findIndex((o) => diskSerial(o) === diskSerial(d));
    if (validateName(d)) e[`spec.disks[${i}]`] = 'Name an existing disk volume.';
    else if (first < i) e[`spec.disks[${i}]`] = disks[first] === d ? 'This disk is already attached.' : `${disks[first]} and ${d} start with the same 20 characters, so the VM couldn't tell them apart. Rename one.`;
  });
  const publish = s.publish ?? [];
  if (publish.length > RULES.vmPublishMax) e['spec.publish'] = `Publish at most ${RULES.vmPublishMax} ports.`;
  publish.forEach((p, i) => {
    if (!inRange(p, RULES.portMin, RULES.portMax) || !p) e[`spec.publish[${i}]`] = 'Use a port from 1 to 65535.';
    else if (p === RULES.sshPort) e[`spec.publish[${i}]`] = "SSH is already reachable through the VM's SSH address; publish other ports.";
    else if (publish.indexOf(p) < i) e[`spec.publish[${i}]`] = 'This port is listed twice.';
  });
  if (s.power !== undefined && !(RULES.powerStates as readonly string[]).includes(s.power)) e['spec.power'] = 'Choose running or stopped.';
  return e;
}

export function validateVolume(s: VolumeSpec): FieldErrors {
  const e: FieldErrors = {};
  if (!s.type) e['spec.type'] = 'Choose disk (for VMs) or filesystem (for apps and jobs).';
  else if (s.type === 'disk') {
    if (!inRange(s.sizeGiB ?? RULES.volumeSizeDefault, RULES.volumeSizeMin, RULES.volumeSizeMax)) e['spec.sizeGiB'] = `Use ${RULES.volumeSizeMin}–${RULES.volumeSizeMax} GiB.`;
  } else if (s.type === 'filesystem') {
    if (s.sizeGiB) e['spec.sizeGiB'] = 'A filesystem volume grows with its files; it has no size to set.';
  } else e['spec.type'] = 'Choose disk or filesystem.';
  if (s.snapshot && validateName(s.snapshot)) e['spec.snapshot'] = 'Name an existing snapshot.';
  return e;
}

export function validateSnapshot(s: SnapshotSpec): FieldErrors {
  if (!s.source) return { 'spec.source': 'Name what to copy: volume/<name> or vm/<name>.' };
  const [kind = '', name = '', extra] = s.source.split('/');
  if (!['volume', 'vm'].includes(kind) || validateName(name) || extra !== undefined) return { 'spec.source': 'Use volume/<name> or vm/<name>.' };
  return {};
}

/** An app's or job's mounts (L3-002 §8.4); the server checks the volumes exist and are filesystems. */
export function validateMounts(mounts: VolumeMount[] | undefined): FieldErrors {
  const e: FieldErrors = {};
  const ms = mounts ?? [];
  if (ms.length > RULES.mountsMax) e['spec.volumes'] = `Mount at most ${RULES.mountsMax} volumes.`;
  ms.forEach((m, i) => {
    const f = `spec.volumes[${i}]`;
    if (validateName(m.volume)) e[`${f}.volume`] = 'Name an existing filesystem volume.';
    const clean = m.path.replace(/\/+/g, '/').split('/').filter((p) => p && p !== '.');
    const normal = '/' + clean.join('/');
    if (!m.path.startsWith('/') || normal !== m.path || clean.includes('..') || m.path.length > RULES.mountPathMax || /[:\\\n]/.test(m.path)) {
      e[`${f}.path`] = 'Use an absolute path inside the container, like /data or /var/www/uploads.';
    } else if (m.path === '/') e[`${f}.path`] = 'Mount a volume on a folder, not on /.';
    else if (ms.findIndex((o) => o.path === m.path) < i) e[`${f}.path`] = 'Another volume is already mounted here.';
  });
  return e;
}

export function validatePassword(pw: string): string | undefined {
  return [...pw].length < RULES.passwordMin || [...pw].length > RULES.passwordMax ? 'Use at least 12 characters.' : undefined;
}

export function validateEmail(email: string): string | undefined {
  const at = email.indexOf('@');
  return at < 1 || at === email.length - 1 || email.length > RULES.emailMax || /\s/.test(email) ? 'Enter an email address like you@example.com.' : undefined;
}
