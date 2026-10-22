import type { Kind } from '../api/types';

/** Sidebar sections, one per kind (L2-001 CON-04, CON-26 – CON-30, CON-32 – CON-35, CON-38 – CON-43, CON-49 – CON-52). The slug is the URL segment. */
export const SECTIONS = [
  { slug: 'apps', kind: 'app', label: 'Apps', noun: 'app', blurb: 'Long-running containers from any image — your services, APIs and workers.' },
  { slug: 'databases', kind: 'database', label: 'Databases', noun: 'database', blurb: 'PostgreSQL, MySQL, MongoDB or ClickHouse, with a managed owner password, kept on a persistent volume.' },
  { slug: 'caches', kind: 'cache', label: 'Caches', noun: 'cache', blurb: 'Redis for sessions, rate limits and queues, persisted to disk.' },
  { slug: 'buckets', kind: 'bucket', label: 'Buckets', noun: 'bucket', blurb: 'S3-compatible object storage for uploads, media and archives.' },
  { slug: 'secrets', kind: 'secret', label: 'Secrets', noun: 'secret', blurb: 'Encrypted values your apps receive as environment variables.' },
  { slug: 'jobs', kind: 'job', label: 'Jobs', noun: 'job', blurb: 'Commands that run on a schedule — cleanups, reports, syncs — with every run kept.' },
  { slug: 'backups', kind: 'backup', label: 'Backups', noun: 'backup', blurb: 'Scheduled dumps of your databases and caches to a bucket or external S3, with one-click restore.' },
  { slug: 'domains', kind: 'domain', label: 'Domains', noun: 'domain', blurb: 'Hostnames served over HTTPS and routed to your apps, with certificates handled for you.' },
  { slug: 'networks', kind: 'network', label: 'Networks', noun: 'network', blurb: 'Private networks with their own address ranges and subnets. Resources in different networks can’t reach each other.' },
  { slug: 'firewalls', kind: 'firewall', label: 'Firewalls', noun: 'firewall', blurb: 'Allow rules for apps, databases, caches, load balancers and VMs: once a firewall names a resource, only what its rules allow gets in.' },
  { slug: 'load-balancers', kind: 'loadbalancer', label: 'Load balancers', noun: 'load balancer', blurb: 'TCP or HTTP traffic spread across your apps’ replicas, with health checks and weights.' },
  { slug: 'vms', kind: 'vm', label: 'VMs', noun: 'VM', blurb: 'Whole machines with an operating system of their own, run by QEMU on this machine — reached over SSH and placed in your networks.' },
  { slug: 'volumes', kind: 'volume', label: 'Volumes', noun: 'volume', blurb: 'Storage that outlives what uses it: disks for VMs, folders for apps and jobs that keep files across deploys.' },
  { slug: 'snapshots', kind: 'snapshot', label: 'Snapshots', noun: 'snapshot', blurb: 'Full copies of volumes and VM disks, taken while in use. New volumes and VMs can start from them.' },
  { slug: 'queues', kind: 'queue', label: 'Queues', noun: 'queue', blurb: 'Messages that wait for a worker, with retries and a dead-letter queue — for jobs that must not be lost.' },
  { slug: 'topics', kind: 'topic', label: 'Topics', noun: 'topic', blurb: 'Messages delivered to every subscription: queues, and webhooks signed with a secret.' },
  { slug: 'search', kind: 'search', label: 'Search', noun: 'search service', blurb: 'OpenSearch with a password, for full-text search and analytics over your data.' },
  { slug: 'transcoders', kind: 'transcoder', label: 'Transcoders', noun: 'transcoder', blurb: 'Video and audio dropped in a bucket folder, converted to MP4, HLS, WebM, MP3 and thumbnails.' },
  { slug: 'cdn', kind: 'cdn', label: 'CDN', noun: 'CDN', blurb: 'A cache with its own hostname in front of an app or a bucket, served over HTTPS.' },
  { slug: 'email', kind: 'email', label: 'Email', noun: 'email service', blurb: 'SMTP for your apps: every message kept to look at, and relayed through your provider when you want it delivered.' },
  { slug: 'zones', kind: 'zone', label: 'DNS', noun: 'zone', blurb: 'Zones at Cloudflare or Route 53 and the records EnclaveStack keeps in them — pointed at this machine if you like, and put back if changed elsewhere.' },
  // v0.7 (CON-49 – CON-52): watching and wiring what runs.
  { slug: 'monitoring', kind: 'dashboard', label: 'Monitoring', noun: 'dashboard', blurb: "Graphs of CPU, memory, requests and queues: this machine's own, and dashboards you put together.", group: 'operate' },
  { slug: 'alerts', kind: 'alert', label: 'Alerts', noun: 'alert', blurb: 'Rules that notify your channels when a metric crosses a line, something keeps restarting, or fails.', group: 'operate' },
  { slug: 'channels', kind: 'channel', label: 'Channels', noun: 'channel', blurb: 'Where alerts go: email, a webhook, Slack, Discord or Telegram.', group: 'operate' },
  { slug: 'log-exports', kind: 'logexport', label: 'Log exports', noun: 'log export', blurb: 'Logs sent on, continuously: to a bucket, any S3, an HTTP endpoint, OpenTelemetry or Grafana Loki.', group: 'operate' },
  { slug: 'secret-stores', kind: 'secretstore', label: 'Secret stores', noun: 'secret store', blurb: 'Vault, 1Password, AWS Secrets Manager, Doppler or Infisical: secrets read from them and kept in sync.', group: 'operate' },
  // Records are reached from their zone (L2.5-002 §11); the section keeps their pages addressable.
  { slug: 'records', kind: 'record', label: 'DNS records', noun: 'record', blurb: 'DNS records kept at your zone’s provider — or pointed at this machine — and put back if changed elsewhere.', inZone: true },
] as const satisfies readonly { slug: string; kind: Kind; label: string; noun: string; blurb: string; inZone?: boolean; group?: 'operate' }[];

export type Section = (typeof SECTIONS)[number];

/** The sections the sidebar lists under Resources: records live inside their zone. */
export const NAV_SECTIONS = SECTIONS.filter((s) => !('inZone' in s) && !('group' in s));

/** The sidebar's Operate group (v0.7). */
export const OPERATE_SECTIONS = SECTIONS.filter((s) => 'group' in s);

export function sectionBySlug(slug: string | undefined): Section | undefined {
  return SECTIONS.find((s) => s.slug === slug);
}

export function sectionByKind(kind: Kind): Section {
  return SECTIONS.find((s) => s.kind === kind) ?? SECTIONS[0];
}
