import { KINDS, RULES } from './generated';
import type { CatalogueEntry, FieldError, Kind, MachineSummary, Placed, TemplateMember } from './types';

// The mock's machines and catalogue (v0.8, L3-002 §11): the built-in entries as the server lists them,
// placement by the server's rule, and a mock rendering of templates into members.

const GiB = 2 ** 30;

const domainParam = (required: boolean, description: string) => ({ name: 'domain', title: 'Domain', description, type: 'hostname' as const, ...(required ? { required: true } : {}) });
const member = (kind: Kind, name: string, optional = false): TemplateMember => ({ kind, name: name ? `<stack>-${name}` : '<stack>', optional });

const appParam = { name: 'app', title: 'App', description: 'The app to make public.', type: 'string' as const, required: true };
const secretParam = (name: string, title: string, description: string, required = true) => ({ name, title, description, type: 'secret' as const, ...(required ? { required: true } : {}) });

/** The ways in from the internet (L2-014 TUN-15): the catalogue's Public access category. */
const PUBLIC_ACCESS = [
  { name: 'quick-link', title: 'Quick link',
    description: 'An address for one app with no account and no domain: a random trycloudflare.com link. It changes when it restarts and handles at most 200 requests at once, so it is for demos.',
    parameters: [appParam] },
  { name: 'tailscale', title: 'Tailscale', signup: 'https://login.tailscale.com/start',
    description: "A stable name.ts.net address for one app, free. Private: only devices in your tailnet open it. Public: anyone can, with Funnel, behind EnclaveStack's gate. Needs a free Tailscale account with MagicDNS and HTTPS certificates on.",
    parameters: [appParam, secretParam('authKey', 'Auth key', 'A reusable auth key from the Tailscale admin console (Settings › Keys).'),
      { name: 'mode', title: 'Who can open it', description: "private (your tailnet's devices) or public (anyone, with Funnel).", type: 'string' as const, default: 'private' }] },
  { name: 'ngrok', title: 'ngrok', signup: 'https://dashboard.ngrok.com/signup',
    description: 'An address for one app from a free ngrok account: one address per account, about 1 GB and 20,000 requests a month, and a warning page ngrok shows visitors first. Paid plans can name a reserved domain.',
    parameters: [appParam, secretParam('authToken', 'Authtoken', 'Your Authtoken, from the ngrok dashboard.'),
      { name: 'domain', title: 'Reserved domain', description: "A domain reserved in your ngrok account (paid plans); empty uses the account's free address.", type: 'string' as const, default: '' }] },
  { name: 'custom-tunnel', title: 'Custom connector',
    description: 'Any other tunnel service, from its own container image. EnclaveStack sets ENCLAVE_UPSTREAM, where the connector sends traffic; the gate still stands in front of your app. Add its command and environment on the tunnel\'s page.',
    parameters: [appParam, { name: 'image', title: 'Image', description: "The connector's image, like ghcr.io/acme/tunnel:1.", type: 'string' as const, required: true },
      { name: 'url', title: 'Public address', description: "The https:// address the service gives, to show; empty if you don't know it yet.", type: 'string' as const, default: '' }] },
  { name: 'cloudflare-tunnel', title: 'Cloudflare Tunnel', signup: 'https://dash.cloudflare.com/sign-up',
    description: "Your own domain, like stage.example.com, through your free Cloudflare account; the domain's DNS must be at Cloudflare. Cloudflare serves the certificate, and can check visitors' emails with Cloudflare Access. Add public domains through it afterwards.",
    parameters: [secretParam('apiToken', 'API token', 'A token with Account › Cloudflare Tunnel › Edit and Zone › DNS › Edit (and Access › Apps and Policies › Edit for Access).'),
      { name: 'accountId', title: 'Account ID', description: "Your account ID, on the domain's overview page at Cloudflare.", type: 'string' as const, required: true }] },
  { name: 'own-server', title: 'Your own server',
    description: 'Your own domain through a small server you rent (about $4–6 a month) and join as an edge machine. No third party sees your traffic: it passes encrypted bytes and never holds a key. Add the edge machine first, then public domains through this.',
    parameters: [{ name: 'machine', title: 'Edge machine', description: 'The edge machine the relay runs on (Machines › Add an edge machine).', type: 'string' as const, required: true }] },
].map((e) => ({ ...e, category: 'public-access' as const, members: [member('tunnel', '')], outputs: [] as string[] }));

/** Outside services apps use (L2-015 CNX-02): the catalogue's Connections category. */
const CONNECTIONS = [
  { name: 'supabase', title: 'Supabase', signup: 'https://supabase.com/dashboard/sign-up',
    description: "Your hosted Supabase project's address and keys, kept as secrets for your apps to use. It runs nothing here: your apps reach Supabase over the internet.",
    parameters: [
      { name: 'project', title: 'Project address', description: "Your project's address without https://, like abcd.supabase.co: the Project URL in your project's settings.", type: 'hostname' as const, required: true },
      secretParam('anonKey', 'anon key', 'The anon key, or the newer publishable key (sb_publishable_…), under Project Settings › API Keys. Safe to use in browsers.'),
      secretParam('serviceKey', 'service_role key', 'The service_role key, or the newer secret key (sb_secret_…), under Project Settings › API Keys. For servers only: it bypasses row-level security.', false),
      secretParam('dbUrl', 'Database connection string', "For apps that use Postgres directly: the pooler's connection string with your password in it, under Connect. The direct one needs IPv6.", false),
    ],
    members: [member('secret', 'url')], outputs: ['SUPABASE_URL', 'SUPABASE_ANON_KEY', 'SUPABASE_SERVICE_ROLE_KEY', 'DATABASE_URL'] },
].map((e) => ({ ...e, category: 'connections' as const }));

/** The secret parameter each connection output names (L3-002 §17); outputs of keys not given are left out (CNX-05). */
const CONNECTION_OUTPUTS: Record<string, string> = { SUPABASE_URL: '', SUPABASE_ANON_KEY: 'anonKey', SUPABASE_SERVICE_ROLE_KEY: 'serviceKey', DATABASE_URL: 'dbUrl' };

/** The thirteen built-in entries (internal/catalogue/builtin), as GET /catalogue lists them: by name. */
export const BUILTIN_CATALOGUE: CatalogueEntry[] = ([
  { name: 'wordpress', title: 'WordPress', description: 'A WordPress site with its MySQL database and a volume for uploads, themes and plugins.',
    parameters: [domainParam(false, 'Serve the site at this host name, with HTTPS.'), { name: 'memoryMiB', title: 'Memory (MiB)', type: 'number' as const, default: 512 }],
    members: [member('database', 'db'), member('volume', 'content'), member('app', ''), member('domain', 'site', true)], outputs: ['url', 'admin'] },
  { name: 'ghost', title: 'Ghost', description: 'A Ghost publication with its MySQL database and a volume for images and themes.',
    parameters: [domainParam(true, 'Ghost needs the address readers open it at.')],
    members: [member('database', 'db'), member('volume', 'content'), member('app', ''), member('domain', 'site')], outputs: ['url', 'admin'] },
  { name: 'n8n', title: 'n8n', description: 'n8n workflow automation with its PostgreSQL database and a volume for its settings.',
    parameters: [domainParam(false, 'Serve n8n and its webhooks at this host name, with HTTPS.')],
    members: [member('database', 'db'), member('secret', 'key'), member('volume', 'data'), member('app', ''), member('domain', 'site', true)], outputs: ['url'] },
  { name: 'uptime-kuma', title: 'Uptime Kuma', description: 'A status page and uptime monitor, keeping its data in a volume.',
    parameters: [domainParam(false, 'Serve the status page at this host name, with HTTPS.')],
    members: [member('volume', 'data'), member('app', ''), member('domain', 'site', true)], outputs: ['url'] },
  { name: 'gitea', title: 'Gitea', description: 'Git hosting with its PostgreSQL database and a volume for repositories.',
    parameters: [domainParam(false, 'Serve Gitea at this host name, with HTTPS.')],
    members: [member('database', 'db'), member('volume', 'data'), member('app', ''), member('domain', 'site', true)], outputs: ['url'] },
  { name: 'plausible', title: 'Plausible Analytics', description: 'Privacy-friendly web analytics, with PostgreSQL for settings and ClickHouse for events.',
    parameters: [domainParam(true, 'Plausible needs the address it is opened at.')],
    members: [member('database', 'db'), member('database', 'events'), member('secret', 'key'), member('secret', 'database-url'), member('secret', 'events-url'), member('app', ''), member('domain', 'site')],
    outputs: ['url'] },
].map((e) => ({ ...e, category: 'apps' as const })) as Omit<CatalogueEntry, 'version' | 'versions' | 'builtIn'>[])
  .concat(PUBLIC_ACCESS, CONNECTIONS).map((e) => ({ ...e, version: '1.0.0', versions: ['1.0.0'], builtIn: true })).sort((a, b) => a.name.localeCompare(b.name));

/** A member as the mock creates it. */
export interface MockMember {
  kind: Kind;
  name: string;
  spec: Record<string, unknown>;
  /** A secret member's value. */
  value?: string;
}

/** The members of a stack of entry, with the template's outputs; or the refusal's details. */
export function renderStack(entry: CatalogueEntry, stack: string, params: Record<string, unknown>, secrets: Record<string, string>):
  { members: MockMember[]; outputs: Record<string, string> } | FieldError[] {
  const details: FieldError[] = [];
  for (const k of Object.keys(params)) {
    if (!entry.parameters.some((p) => p.name === k && p.type !== 'secret')) details.push({ field: `spec.parameters.${k}`, code: 'NOT_ALLOWED', message: `${entry.title} has no parameter ${k}.` });
  }
  for (const p of entry.parameters) {
    const v = p.type === 'secret' ? secrets[p.name] : params[p.name];
    const field = `spec.${p.type === 'secret' ? 'secretParameters' : 'parameters'}.${p.name}`;
    if (p.required && (v === undefined || v === '')) details.push({ field, code: 'REQUIRED', message: `${p.title} is required.` });
    else if (v !== undefined && v !== '' && p.type === 'number' && !Number.isFinite(Number(v))) details.push({ field, code: 'INVALID_FORMAT', message: `${p.title} is a number.` });
    else if (v !== undefined && v !== '' && p.type === 'hostname' && !/^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$/.test(String(v).toLowerCase())) {
      details.push({ field, code: 'INVALID_FORMAT', message: `${p.title} is a host name, like blog.example.com.` });
    }
  }
  if (stack.length + 1 + Math.max(0, ...entry.members.map((m) => m.name.replace('<stack>', '').length)) > RULES.nameMax) {
    details.push({ field: 'name', code: 'OUT_OF_RANGE', message: `Use at most ${RULES.nameMax - Math.max(...entry.members.map((m) => m.name.replace('<stack>', '').length))} characters: the stack's members add to the name.` });
  }
  if (details.length) return details;
  const value = (name: string) => {
    const p = entry.parameters.find((x) => x.name === name);
    return params[name] ?? p?.default;
  };
  const domain = String(value('domain') ?? '').toLowerCase();
  const members: MockMember[] = [];
  for (const m of entry.members) {
    if (m.optional && !domain) continue;
    const name = m.name.replace('<stack>', stack);
    const local = m.name.replace('<stack>-', '').replace('<stack>', '');
    switch (m.kind) {
      case 'database':
        members.push({ kind: m.kind, name, spec: local === 'events' ? { engine: 'clickhouse', version: '26.3' } : entry.name === 'wordpress' || entry.name === 'ghost' ? { engine: 'mysql', version: '8.4' } : { engine: 'postgres', version: '16' } });
        break;
      case 'volume':
        members.push({ kind: m.kind, name, spec: { type: 'filesystem' } });
        break;
      case 'secret':
        members.push({ kind: m.kind, name, spec: {}, value: entry.category === 'connections' ? `https://${String(value('project') ?? '').toLowerCase()}`
          : Math.random().toString(36).slice(2) + Math.random().toString(36).slice(2) });
        break;
      case 'domain':
        members.push({ kind: m.kind, name, spec: { hostname: domain, app: stack, tls: 'auto', redirectHttp: true } });
        break;
      case 'app': {
        const volume = entry.members.find((x) => x.kind === 'volume')?.name.replace('<stack>', stack);
        members.push({ kind: m.kind, name, spec: { image: `${entry.name}:latest`, port: 80, memoryMiB: Number(value('memoryMiB') ?? RULES.appMemoryDefault), strategy: 'recreate', replicas: 1,
          ...(volume ? { volumes: [{ volume, path: '/data' }] } : {}) } });
        break;
      }
      default:
        members.push({ kind: m.kind, name, spec: {} });
    }
  }
  for (const [k, v] of Object.entries(secrets)) members.unshift({ kind: 'secret', name: `${stack}-${k.toLowerCase()}`, spec: {}, value: v });
  const outputs: Record<string, string> = {};
  if (entry.category === 'connections') {
    for (const o of entry.outputs) {
      const p = CONNECTION_OUTPUTS[o];
      if (p === '') outputs[o] = `${stack}-url`;
      else if (p && secrets[p]) outputs[o] = `${stack}-${p.toLowerCase()}`;
    }
    return { members, outputs };
  }
  for (const o of entry.outputs) outputs[o] = o === 'admin' ? `https://${domain}${entry.name === 'ghost' ? '/ghost/' : '/wp-admin/'}` : o === 'app' ? stack : `https://${domain}`;
  return { members, outputs };
}

/** Reads a template of one's own the way the mock can: its name, title, version, description and members. */
export function parseTemplate(body: string): CatalogueEntry | FieldError[] {
  const top = (key: string) => body.match(new RegExp(`^${key}:\\s*(.+)$`, 'm'))?.[1]?.trim().replace(/^["']|["']$/g, '');
  const lineOf = (key: string) => body.split('\n').findIndex((l) => l.startsWith(`${key}:`)) + 1;
  const details: FieldError[] = [];
  const name = top('name');
  const version = top('version');
  if (!body.trim()) details.push({ field: 'body', code: 'REQUIRED', message: 'Give the template, in YAML or JSON.' });
  else {
    if (!name) details.push({ field: 'body.line 1: name', code: 'REQUIRED', message: 'A template has a name.' });
    if (!version || !/^\d+\.\d+\.\d+$/.test(version)) details.push({ field: `body.line ${lineOf('version') || 1}: version`, code: 'INVALID_FORMAT', message: 'The version is MAJOR.MINOR.PATCH, like 1.0.0.' });
  }
  const kinds = [...body.matchAll(/kind:\s*([a-z]+)\s*,?\s*(?:\n\s*)?name:\s*"?([a-z0-9-]*)"?/g)];
  body.split('\n').forEach((l, i) => {
    const k = l.match(/kind:\s*([a-z]+)/)?.[1];
    if (k && !(KINDS as readonly string[]).includes(k)) details.push({ field: `body.line ${i + 1}: resources.kind`, code: 'NOT_ALLOWED', message: `${k} isn't a kind.` });
  });
  if (!details.length && kinds.length === 0) details.push({ field: 'body.line 1: resources', code: 'REQUIRED', message: 'A template has at least one resource.' });
  if (details.length) return details;
  return { name: name ?? '', title: top('title') ?? name ?? '', description: top('description') ?? '', version: version ?? '', versions: [version ?? ''], builtIn: false, category: 'apps',
    parameters: [], members: kinds.map(([, kind, local]) => member(kind as Kind, local ?? '')), outputs: [] };
}

/** The demo's main machine: an ID made at first start and a name from the computer's own (MCH-01). */
export const MAIN = { id: 'mch_mainmachine0000', name: 'this-mac' } as const;

/** The main machine, as GET /machines lists it. */
export function mainMachine(title: string, reservedBytes: number, resources: number): MachineSummary {
  return { id: MAIN.id, name: MAIN.name, title, main: true, role: 'worker', state: 'online', since: new Date(Date.now() - 3 * 86_400_000).toISOString(), labels: {},
    schedulable: true, address: null, os: 'darwin', arch: 'arm64', agentVersion: '0.8.0-mock', engine: { driver: 'mock', available: true, version: 'mock', message: '' },
    cpus: 8, memoryTotalBytes: 16 * GiB, memoryReservedBytes: reservedBytes, vms: { available: true, message: 'VMs can run: QEMU 11.1 with HVF.' }, lastSeenAt: new Date().toISOString(),
    resources, versionMismatch: false, links: [], message: '' };
}

/** A machine waiting for its agent. */
export function waitingMachine(id: string, name: string, labels: Record<string, string>): MachineSummary {
  return { id, name, title: name, main: false, role: 'worker', state: 'waiting', since: new Date().toISOString(), labels, schedulable: true, address: null, os: '', arch: '', agentVersion: '',
    engine: { driver: '', available: false, version: '', message: '' }, cpus: 0, memoryTotalBytes: 0, memoryReservedBytes: 0, vms: { available: false, message: 'Not joined yet.' },
    lastSeenAt: null, resources: 0, versionMismatch: false, links: [], message: 'Waiting for its agent to join.' };
}

/** A waiting machine once its agent joins: a Linux box with 8 GiB. */
export function joined(m: MachineSummary, address: string): MachineSummary {
  return { ...m, state: 'online', since: new Date().toISOString(), address, os: 'linux', arch: 'amd64', agentVersion: '0.8.0-mock',
    engine: { driver: 'docker', available: true, version: '29.0.1', message: '' }, cpus: 4, memoryTotalBytes: 8 * GiB, vms: { available: true, message: 'VMs can run: QEMU 11.1 with KVM.' },
    lastSeenAt: new Date().toISOString(), message: '' };
}

/** The server's placement rule (ADR-044): the named machine, else the one with the most free memory among online, schedulable machines with the labels, main on a tie. */
export function choose(p: Placed, machines: MachineSummary[], needBytes: number): { machine: string } | { reason: string } {
  if (p.machine) {
    const m = machines.find((x) => x.name === p.machine);
    if (!m) return { reason: `There's no machine named ${p.machine}.` };
    return m.state === 'online' ? { machine: m.name } : { reason: `${m.name} is ${m.state}.` };
  }
  const labels = Object.entries(p.machineLabels ?? {});
  const fit = machines.filter((m) => m.state === 'online' && m.schedulable && labels.every(([k, v]) => m.labels[k] === v));
  if (!fit.length) return { reason: labels.length ? `No machine has the labels ${labels.map(([k, v]) => `${k}=${v}`).join(', ')}.` : 'No machine can take it.' };
  const room = fit.filter((m) => m.memoryTotalBytes - m.memoryReservedBytes >= needBytes);
  if (!room.length) return { reason: `No machine has ${Math.round(needBytes / 2 ** 20)} MiB free.` };
  room.sort((a, b) => (b.memoryTotalBytes - b.memoryReservedBytes) - (a.memoryTotalBytes - a.memoryReservedBytes) || Number(b.main) - Number(a.main));
  return { machine: room[0]?.name ?? MAIN.name };
}
