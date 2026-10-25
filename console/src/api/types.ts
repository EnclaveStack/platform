// Wire shapes of L3-002. Kinds, phases, error codes and rules come from generated.ts (from pkg/api) —
// they are never re-declared here (ADR-002).
import type { ErrorCode, FieldCode, Kind, MetricUnit, Phase, ROLES, RULES, StepState } from './generated';

export type { ErrorCode, FieldCode, Kind, MetricUnit, Phase, StepState } from './generated';

export interface Step {
  name: string;
  state: StepState;
}

export interface Status {
  phase: Phase;
  message: string;
  observedGeneration: number;
  attempts: number;
  steps: Step[];
  lastTransitionAt: string;
}

export type HealthCheckType = 'http' | 'tcp' | 'command' | 'none';
export type Strategy = 'rolling' | 'recreate';

/** When a new revision may take traffic (ADR-016, L3-002 §6.2). */
export interface HealthCheck {
  type: HealthCheckType;
  path?: string;
  port?: number;
  command?: string[];
  intervalSeconds?: number;
  timeoutSeconds?: number;
  retries?: number;
  startPeriodSeconds?: number;
}

/** Build the app from Git with its Dockerfile (ADR-019). */
export interface GitSource {
  git: string;
  ref: string;
  dockerfile?: string;
  context?: string;
  /** A secret holding {"token"} (https) or {"privateKey","knownHosts"} (ssh). */
  credentialsSecret?: string;
  /** Not for secrets: build arguments end up in image metadata. */
  buildArgs?: Record<string, string>;
}

/** Where a placed kind runs (ADR-044, L3-002 §11.3): a machine, or labels the machine must have; neither lets EnclaveStack choose. */
export interface Placed {
  machine?: string;
  machineLabels?: Record<string, string>;
}

/** Exactly one of image and source. */
export interface AppSpec extends Placed {
  image?: string;
  source?: GitSource;
  port?: number;
  command?: string[];
  env?: Record<string, string>;
  secretEnv?: Record<string, string>;
  publish?: boolean;
  memoryMiB?: number;
  healthCheck?: HealthCheck;
  strategy?: Strategy;
  network?: string;
  subnet?: string;
  /** Containers per revision, 1–10 (ADR-022). */
  replicas?: number;
  /** Filesystem volumes mounted into every container (ADR-027). */
  volumes?: VolumeMount[];
  /** Buckets it reaches with keys of its own (L2-011); the list replaces the old one. */
  bucketAccess?: BucketGrant[];
}
export type TlsMode = 'auto' | 'internal' | 'off';

/** A hostname routed to an app through the edge proxy (ADR-015, L3-002 §6.2). */
export interface DomainSpec {
  hostname: string;
  app: string;
  port?: number;
  tls?: TlsMode;
  redirectHttp?: boolean;
  /** A zone resource: the certificate comes through DNS-01, and a wildcard hostname is allowed (ADR-024). */
  zone?: string;
  /** A cloudflare or relay tunnel: the domain is served to the internet through it (L2-014 PUB-01). */
  tunnel?: string;
  /** Who may pass on a public domain; absent: signin (GATE-01). */
  gate?: Gate;
  /** Through a Cloudflare tunnel: Cloudflare Access checks these people first (GATE-09). */
  cloudflareAccess?: CloudflareAccess;
}

export type GateMode = 'signin' | 'password' | 'none';

/** What stands between the internet and a public app (L2-014 GATE-01 – GATE-06). */
export interface Gate {
  mode: GateMode;
  /** With mode password: a secret holding {"username","password"}. */
  passwordSecret?: string;
  /** Path prefixes that skip the gate, like /webhooks/. */
  openPaths?: string[];
}

export interface CloudflareAccess {
  emails?: string[];
  emailDomains?: string[];
}

export type TunnelProvider = 'quick' | 'tailscale' | 'ngrok' | 'custom' | 'cloudflare' | 'relay';
export type TailscaleMode = 'private' | 'public';

/** A way in from the internet (L2-014 TUN-01 – TUN-16, L3-002 §16). App links give one app an address; cloudflare and relay serve public domains. */
export interface TunnelSpec {
  provider: TunnelProvider;
  /** cloudflare, tailscale, ngrok: the secret holding the token, as the value or {"apiToken"} / {"authKey"} / {"authToken"}. */
  credentialsSecret?: string;
  /** cloudflare: the account the tunnel is made in. */
  accountId?: string;
  /** relay: the edge machine it runs on. */
  machine?: string;
  /** App links: the app and its port (default the app's). */
  app?: string;
  port?: number;
  /** App links: absent is signin (none for a private Tailscale link). */
  gate?: Gate;
  mode?: TailscaleMode;
  hostname?: string;
  /** ngrok: a domain reserved in the account. */
  domain?: string;
  image?: string;
  command?: string[];
  env?: Record<string, string>;
  secretEnv?: Record<string, string>;
  /** custom: the https:// address the service gives. */
  url?: string;
}

export type Overlap = 'forbid' | 'allow';

/** A command on a schedule (ADR-017, L3-002 §6.2). Exactly one of app or image. */
export interface JobSpec extends Placed {
  schedule: string;
  timeZone?: string;
  app?: string;
  image?: string;
  command?: string[];
  env?: Record<string, string>;
  secretEnv?: Record<string, string>;
  timeoutSeconds?: number;
  overlap?: Overlap;
  memoryMiB?: number;
  /** Image jobs only; an app job runs in its app's network. */
  network?: string;
  subnet?: string;
  volumes?: VolumeMount[];
  /** A job from an image only; a job with app uses the app's (L2-011 BKA-05). */
  bucketAccess?: BucketGrant[];
}

/** Scheduled dumps of a database or cache (ADR-018, L3-002 §6.2). Exactly one source, one destination. */
export interface BackupSource {
  database?: string;
  cache?: string;
}
export interface S3Destination {
  endpoint: string;
  region?: string;
  bucket: string;
  /** A secret holding {"accessKey","secretKey"}. */
  credentialsSecret: string;
}
export interface BackupDestination {
  bucket?: string;
  s3?: S3Destination;
}
export interface BackupSpec {
  source: BackupSource;
  destination: BackupDestination;
  prefix?: string;
  schedule?: string;
  timeZone?: string;
  keep?: number;
  keepDays?: number;
  timeoutSeconds?: number;
}

export type RestoreMode = 'new' | 'replace';
/** POST …/actions/restore. "new" needs name; "replace" needs confirmName = the source's name. */
export interface RestoreRequest {
  runId: string;
  mode: RestoreMode;
  name?: string;
  confirmName?: string;
}

/** What a backup run stored. */
export interface Artifact {
  key: string;
  sizeBytes: number;
  sha256: string;
  destination: string;
}

export type RunStatus = 'running' | 'succeeded' | 'failed' | 'timed_out' | 'skipped';
export type RunTrigger = 'schedule' | 'manual' | 'catch_up' | 'pre_restore' | 'watch';

export interface Run {
  id: string;
  trigger: RunTrigger;
  status: RunStatus;
  scheduledFor: string | null;
  startedAt: string;
  finishedAt: string | null;
  exitCode: number | null;
  message: string;
  skippedCount?: number;
  /** Backups only: the stored object, when the run succeeded. */
  artifact?: Artifact;
  /** Transcoders only: the input object and the keys the run wrote. */
  key?: string;
  outputs?: string[];
}

export interface RunDetail extends Run {
  outputTail: string;
}

export type DatabaseEngine = 'postgres' | 'mysql' | 'mongodb' | 'clickhouse';
export interface DatabaseSpec extends Placed {
  engine: DatabaseEngine;
  version: string;
  network?: string;
  subnet?: string;
}
export interface CacheSpec extends Placed {
  engine: 'redis';
  version: string;
  maxMemoryMiB?: number;
  network?: string;
  subnet?: string;
}

// v0.4 (L3-002 §7.2).

export interface Subnet {
  name: string;
  cidr: string;
}
/** A private network; every field is immutable (ADR-020). */
export interface NetworkSpec {
  cidr: string;
  subnets?: Subnet[];
  internet?: boolean;
}

export type Protocol = 'tcp' | 'udp' | 'icmp' | 'all';
/** Ingress rules name sources in `from`, egress rules destinations in `to`. */
export interface FirewallRule {
  from?: string[];
  to?: string[];
  protocol?: Protocol;
  ports?: string[];
}
export interface FirewallSpec {
  targets: string[];
  ingress?: FirewallRule[];
  egressDefault?: 'allow' | 'deny';
  egress?: FirewallRule[];
}

export type Algorithm = 'round_robin' | 'least_conn' | 'source';
export interface LoadBalancerTarget {
  app: string;
  port?: number;
  weight?: number;
}
export interface LoadBalancerCheck {
  type: 'tcp' | 'http';
  path?: string;
  intervalSeconds?: number;
  timeoutSeconds?: number;
  healthyThreshold?: number;
  unhealthyThreshold?: number;
}
export interface LoadBalancerSpec extends Placed {
  port: number;
  protocol?: 'http' | 'tcp';
  targets: LoadBalancerTarget[];
  algorithm?: Algorithm;
  healthCheck?: LoadBalancerCheck;
  publish?: boolean;
}

export type DnsProvider = 'cloudflare' | 'route53';
export interface ZoneSpec {
  provider: DnsProvider;
  zone: string;
  credentialsSecret: string;
}
export type RecordType = 'A' | 'AAAA' | 'CNAME' | 'TXT';
/** Exactly one of values and machine. */
export interface RecordSpec {
  zone: string;
  name: string;
  type: RecordType;
  values?: string[];
  machine?: boolean;
  ttl?: number;
}
// v0.5 (L3-002 §8.2).

/** A filesystem volume mounted into an app's or job's containers. */
/** One bucket an app or job reaches, and how (L2-011, L3-002 §12). */
export interface BucketGrant {
  bucket: string;
  access: BucketAccessLevel;
}

export type BucketAccessLevel = (typeof RULES.bucketAccessLevels)[number];

export interface VolumeMount {
  volume: string;
  path: string;
  readOnly?: boolean;
}
export type Power = 'running' | 'stopped';
/** A VM run by QEMU (ADR-025). Image, keys and the first-boot script never change. */
export interface VMSpec extends Placed {
  /** ubuntu-24.04, debian-12, an https:// URL, or snapshot/<name>. */
  image: string;
  imageSha256?: string;
  /** Exactly one of sshKeys and sshKeySecret. */
  sshKeys?: string[];
  sshKeySecret?: string;
  userData?: string;
  cpus?: number;
  memoryMiB?: number;
  diskGiB?: number;
  network?: string;
  subnet?: string;
  /** Disk volumes, in attach order. */
  disks?: string[];
  publish?: number[];
  power?: Power;
}
export type VolumeType = 'disk' | 'filesystem';
export interface VolumeSpec extends Placed {
  type: VolumeType;
  /** Disks only; grows only. */
  sizeGiB?: number;
  snapshot?: string;
}
/** volume/<name> or vm/<name>; never changes. */
export interface SnapshotSpec {
  source: string;
}

// v0.6 (L3-002 §9.2).

export interface QueueSpec {
  visibilityTimeoutSeconds?: number;
  retentionHours?: number;
  maxMessageBytes?: number;
  /** 0: no limit. */
  maxReceives?: number;
  deadLetterQueue?: string;
}
/** Exactly one of queue and url; secret signs webhook bodies. */
export interface Subscription {
  queue?: string;
  url?: string;
  secret?: string;
}
export interface TopicSpec {
  retentionHours?: number;
  subscriptions?: Subscription[];
}
export interface SearchSpec extends Placed {
  version?: '2' | '3';
  memoryMiB?: number;
  network?: string;
  subnet?: string;
}
export type Preset = 'mp4-480p' | 'mp4-720p' | 'mp4-1080p' | 'webm-720p' | 'hls-720p' | 'mp3' | 'thumbnail';
/** A bucket and a folder in it: "" or "a/b/". */
export interface BucketFolder {
  bucket: string;
  prefix?: string;
}
export interface TranscoderSpec {
  input: BucketFolder;
  output: BucketFolder;
  presets?: Preset[];
  watch?: boolean;
  timeoutSeconds?: number;
  concurrency?: number;
  memoryMiB?: number;
  /** Outputs of the user's own and the "done" message (L2-012). */
  renditions?: Rendition[];
  hls?: HLSLadder;
  notify?: TranscodeNotify;
}
export type RenditionContainer = 'mp4' | 'webm';
/** One file of a chosen size and bitrate: <name>.<container> in the run's folder (TRX-02). */
export interface Rendition {
  name: string;
  container: RenditionContainer;
  maxWidth: number;
  maxHeight: number;
  videoKbps: number;
  audioKbps?: number;
  gopSeconds?: number;
}
export interface HLSRung {
  name: string;
  maxWidth: number;
  maxHeight: number;
  videoKbps: number;
}
/** An adaptive stream: <name>/master.m3u8 and each rung's playlist and segments (TRX-03). */
export interface HLSLadder {
  name?: string;
  segmentSeconds?: number;
  gopSeconds?: number;
  audioKbps?: number;
  rungs: HLSRung[];
}
/** Where a run's message goes when it ends (TRX-05). */
export interface TranscodeNotify {
  webhook?: { url: string; secret?: string };
  topic?: string;
}
/** A cache in front of app/<name> or bucket/<name>, on a hostname served like a domain's. */
export interface CDNSpec {
  hostname: string;
  origin: string;
  originPort?: number;
  defaultTtlSeconds?: number;
  maxSizeMiB?: number;
  tls?: TlsMode;
  redirectHttp?: boolean;
  zone?: string;
}
export type EmailMode = 'capture' | 'relay';
export type RelayTls = 'starttls' | 'tls' | 'none';
export interface EmailRelay {
  host: string;
  port?: number;
  tls?: RelayTls;
  /** A secret holding {"username","password"}. */
  credentialsSecret: string;
}
export interface EmailSpec extends Placed {
  mode?: EmailMode;
  relay?: EmailRelay;
  domain?: string;
  keepMessages?: number;
  network?: string;
  subnet?: string;
}

/** Data endpoints (L3-002 §9.3). */
export interface SendMessage {
  body: string;
  attributes?: Record<string, string>;
  deduplicationId?: string;
}
export interface SentMessage {
  id: string;
  duplicate: boolean;
}
export interface QueueStats {
  messages: number;
  inFlight: number;
  oldestAgeSeconds: number | null;
  deadLettered: number;
}
export interface PeekedMessage {
  id: string;
  body: string;
  attributes: Record<string, string>;
  sentAt: string;
}
export interface ReceivedMessage extends PeekedMessage {
  receipt: string;
  receiveCount: number;
}
export interface MessageList {
  stats: QueueStats;
  items: PeekedMessage[];
}
export interface DeletedMessages {
  deleted: number;
  failed: { receipt: string; message: string }[];
}
export interface SubscriptionStats {
  target: string;
  delivered: number;
  pending: number;
  failed: number;
  lastError: string;
}
export interface DeliveryStats {
  published: number;
  items: SubscriptionStats[];
}
export interface SendEmail {
  from: string;
  to: string[];
  cc?: string[];
  bcc?: string[];
  replyTo?: string;
  subject: string;
  text?: string;
  html?: string;
}
export interface EmailSummary {
  id: string;
  from: string;
  to: string[];
  subject: string;
  sentAt: string;
  sizeBytes: number;
  /** relayed: handed to the relay; a refusal shows in the service's logs. */
  relay: 'captured' | 'relayed';
}
export interface EmailList {
  total: number;
  items: EmailSummary[];
}
export interface Email extends EmailSummary {
  cc: string[];
  bcc: string[];
  replyTo: string;
  text: string;
  html: string;
  headers: Record<string, string[]>;
}

export interface BucketSpec {
  versioning?: boolean;
}
/** The value is write-only: responses carry an empty object, or the source of a synced secret (ADR-041). */
export interface SecretSpec {
  value?: string;
  source?: SecretSource;
}

export interface SpecByKind {
  app: AppSpec;
  database: DatabaseSpec;
  cache: CacheSpec;
  bucket: BucketSpec;
  secret: SecretSpec;
  domain: DomainSpec;
  job: JobSpec;
  backup: BackupSpec;
  network: NetworkSpec;
  firewall: FirewallSpec;
  loadbalancer: LoadBalancerSpec;
  zone: ZoneSpec;
  record: RecordSpec;
  vm: VMSpec;
  volume: VolumeSpec;
  snapshot: SnapshotSpec;
  queue: QueueSpec;
  topic: TopicSpec;
  search: SearchSpec;
  transcoder: TranscoderSpec;
  cdn: CDNSpec;
  email: EmailSpec;
  dashboard: DashboardSpec;
  alert: AlertSpec;
  channel: ChannelSpec;
  logexport: LogExportSpec;
  secretstore: SecretStoreSpec;
  stack: StackSpec;
  tunnel: TunnelSpec;
}

/** A stack: a catalogue entry or a template of one's own, installed as one thing (ADR-048, L3-002 §11.7). Exactly one of template and templateBody. */
export interface StackSpec {
  template?: string;
  templateBody?: string;
  version?: string;
  parameters?: Record<string, string | number | boolean>;
  /** Write-only: they become the stack's secrets and are never returned. */
  secretParameters?: Record<string, string>;
}

export interface StackMember {
  kind: Kind;
  name: string;
  id: string;
  phase: Phase;
}

export type ParameterType = 'string' | 'number' | 'boolean' | 'secret' | 'hostname';

export interface TemplateParameter {
  name: string;
  title: string;
  description?: string;
  type: ParameterType;
  default?: string | number | boolean;
  required?: boolean;
}

/** A resource a template creates, named as created ("<stack>" stands for the stack). */
export interface TemplateMember {
  kind: Kind;
  name: string;
  optional: boolean;
}

export type CatalogueCategory = 'apps' | 'public-access' | 'connections';

export interface CatalogueEntry {
  name: string;
  title: string;
  description: string;
  version: string;
  versions: string[];
  builtIn: boolean;
  /** apps; public-access, a way in from the internet (L2-014 TUN-15); or connections, an outside service apps use (L2-015 CNX-01). */
  category: CatalogueCategory;
  /** Where to make the account a way in needs. */
  signup?: string;
  parameters: TemplateParameter[];
  members: TemplateMember[];
  outputs: string[];
}

export type MachineState = 'waiting' | 'online' | 'offline';
export type MachineRole = 'worker' | 'edge';

/** An edge machine is added with its public address, and its agent's port (default 7071) (EDG-02). */
export interface EdgeMachineInput {
  address: string;
  agentPort?: number;
}

export interface MachineLink {
  machine: string;
  connected: boolean;
  lastHandshakeAt: string | null;
}

/** One machine of GET /machines (L3-002 §11.2). */
export interface MachineSummary {
  id: string;
  name: string;
  title: string;
  main: boolean;
  /** edge: a server with a public address that only relays visitors (L2-014 EDG-01). */
  role: MachineRole;
  state: MachineState;
  since: string;
  labels: Record<string, string>;
  schedulable: boolean;
  address: string | null;
  os: string;
  arch: string;
  agentVersion: string;
  engine: { driver: string; available: boolean; version: string; message: string };
  cpus: number;
  memoryTotalBytes: number;
  memoryReservedBytes: number;
  vms: { available: boolean; message: string; qemuVersion?: string; accelerator?: string };
  lastSeenAt: string | null;
  resources: number;
  versionMismatch: boolean;
  links: MachineLink[];
  message: string;
}

/** The join code is shown once. */
export interface AddedMachine {
  machine: MachineSummary;
  joinCode: string;
  joinCommand: string;
  expiresAt: string;
}

/** Absent fields stay; labels {} clears them. */
export interface UpdateMachineInput {
  labels?: Record<string, string>;
  schedulable?: boolean;
  /** The display name; only the main machine has one of its own (MCH-10). */
  title?: string;
}

/** GET /setup: whether first-run setup is needed, and the computer's own name to start the form with. */
export interface SetupStatus {
  required: boolean;
  suggestedMachineName: string;
}

/** "kind/name", with "project/" outside default. */
export interface DrainResult {
  moving: string[];
  pinned: string[];
}

export type Outputs = Record<string, string | number | boolean>;

export interface Resource<K extends Kind = Kind> {
  id: string;
  /** The namespace it lives in (ADR-035); "default" for everything made before v0.7. */
  project: string;
  kind: K;
  name: string;
  generation: number;
  ownerId: string | null;
  /** The ID of the stack it belongs to (ADR-048), or null. */
  stack: string | null;
  spec: SpecByKind[K];
  status: Status;
  outputs: Outputs;
  createdAt: string;
  updatedAt: string;
}

export interface ResourceList {
  items: Resource[];
  nextCursor: string | null;
}

export interface Session {
  user: { id: string; email: string };
  machineName: string;
  authMethod: 'session' | 'token';
  scope: 'read' | 'write' | 'full';
  /** The user's roles (ADR-036); a token's limits when one is used. */
  machineRole: Role | null;
  projectRoles: Record<string, Role>;
  token: { role: Role; projects: string[] | null } | null;
}

export interface Token {
  id: string;
  name: string;
  prefix: string;
  /** Kept for v0.6 clients; role and projects say what it can do. */
  scope: 'read' | 'write';
  role: Role;
  projects: string[] | null;
  userEmail: string;
  createdAt: string;
  lastUsedAt: string | null;
  expiresAt: string | null;
}

export interface CreatedToken {
  token: Token;
  secret: string;
}

export interface Machine {
  /** The main machine's ID (mch_…) and the name commands use for it (MCH-01). */
  id: string;
  handle: string;
  name: string;
  hostname: string;
  version: string;
  os: string;
  arch: string;
  cpus: number;
  memoryTotalBytes: number;
  memoryReservedBytes: number;
  engine: { driver: string; available: boolean; version: string; message: string };
  publicAddress: string | null;
  acmeEmail: string | null;
  /** How invites are emailed; null when links are only shown (IAM-04). */
  inviteEmail: InviteEmail | null;
  edge: { available: boolean; message: string };
  /** Whether VMs can run here; when not, message names what's missing and the fix (VM-11). */
  vms: { available: boolean; message: string; qemuVersion?: string; accelerator?: string };
  reservedCpus: number;
  images: { name: string; arch: string; sizeBytes: number; usedBy: number }[];
  snapshotBytes: number;
  /** The message broker behind queues and topics (ADR-029). */
  broker: { available: boolean; message: string };
}

/** PATCH /machine: null or "" clears a setting. */
export interface MachineSettings {
  publicAddress: string | null;
  acmeEmail: string | null;
  /** Absent keeps it; null turns emailed invites off (L3-002 §10.3). */
  inviteEmail?: InviteEmail | null;
}

/** The email service that sends invite and reset links (IAM-04). */
export interface InviteEmail {
  project?: string;
  service: string;
  from: string;
}

export interface Event {
  id: number;
  type: string;
  message: string;
  createdAt: string;
}

export interface LogLine {
  time: string;
  stream: 'stdout' | 'stderr';
  message: string;
}

export interface SetupInput {
  setupCode: string;
  machineName: string;
  email: string;
  password: string;
}

export interface CreateTokenInput {
  name: string;
  /** Omitted: the owner's strongest role. */
  role?: Role;
  /** Omitted: every project. */
  projects?: string[];
  expiresInDays?: number;
}

export interface FieldError {
  field: string;
  code: FieldCode;
  message: string;
}

/** The L0-003 error envelope as an Error. `network` means the server could not be reached. */
export class ApiError extends Error {
  readonly code: ErrorCode | 'NETWORK';
  readonly status: number;
  readonly requestId: string;
  readonly details: FieldError[];
  readonly retryAfterSeconds: number;

  constructor(code: ErrorCode | 'NETWORK', message: string, status: number, opts: { requestId?: string; details?: FieldError[]; retryAfterSeconds?: number } = {}) {
    super(message);
    this.name = 'ApiError';
    this.code = code;
    this.status = status;
    this.requestId = opts.requestId ?? '';
    this.details = opts.details ?? [];
    this.retryAfterSeconds = opts.retryAfterSeconds ?? 0;
  }

  /** The message for one field, if the server reported one. */
  fieldMessage(field: string): string | undefined {
    return this.details.find((d) => d.field === field)?.message;
  }
}

// v0.7 (L3-002 §10): projects, people, roles, metrics and the operate kinds.

export type Role = (typeof ROLES)[number];

export interface Project {
  name: string;
  description: string;
  createdAt: string;
  resources: number;
}

export interface UserSummary {
  id: string;
  email: string;
  machineRole: Role | null;
  projectRoles: Record<string, Role>;
  status: 'active' | 'invited';
  createdAt: string;
  lastSignInAt: string | null;
}

export interface InviteUserInput {
  email: string;
  machineRole?: Role;
  projectRoles?: Record<string, Role>;
}

/** Present keys change; null removes a role. */
export interface UpdateUserInput {
  machineRole?: Role | null;
  projectRoles?: Record<string, Role | null>;
}

export interface Invitation {
  user: UserSummary;
  url: string;
  expiresAt: string;
  /** The link was also emailed; emailError says why not. */
  emailed: boolean;
  emailError?: string;
}

export interface InviteInfo {
  email: string;
  kind: 'invite' | 'reset';
}

export interface AuditEntry {
  id: number;
  at: string;
  actor: string;
  action: string;
  target: string;
  detail: string;
  clientIp: string;
}

export interface MetricPoint {
  t: number;
  v: number;
  max?: number;
}

export interface MetricSeries {
  metric: string;
  unit: MetricUnit;
  points: MetricPoint[];
}

export interface MetricData {
  from: string;
  to: string;
  stepSeconds: number;
  items: MetricSeries[];
}

export interface Widget {
  title: string;
  metric: string;
  /** "kind/name" of the same project. */
  resources: string[];
}

export interface DashboardSpec {
  widgets: Widget[];
}

export type ConditionType = 'metric' | 'crash_loop' | 'failed' | 'run_failed' | 'machine_offline';

export interface AlertCondition {
  type: ConditionType;
  resources: string[];
  metric?: string;
  operator?: 'above' | 'below';
  threshold?: number;
  forMinutes?: number;
  restarts?: number;
  windowMinutes?: number;
}

export interface AlertSpec {
  condition: AlertCondition;
  channels: string[];
  repeatMinutes?: number;
  enabled?: boolean;
}

export type ChannelType = 'email' | 'webhook' | 'slack' | 'discord' | 'telegram';

export interface ChannelSpec {
  type: ChannelType;
  email?: { service: string; from: string; to: string[] };
  webhook?: { url: string; secret?: string };
  /** Slack and Discord: the incoming-webhook URL; Telegram: the bot token. */
  secret?: string;
  chatId?: string;
}

export interface LogHttp {
  url?: string;
  endpoint?: string;
  headersSecret?: string;
  labels?: Record<string, string>;
}

export interface LogDestination {
  bucket?: { bucket: string; prefix?: string };
  s3?: { endpoint: string; region?: string; bucket: string; prefix?: string; credentialsSecret: string };
  http?: LogHttp;
  otlp?: LogHttp;
  loki?: LogHttp;
}

export interface LogExportSpec {
  sources?: string[];
  filter?: string;
  flushSeconds?: number;
  destination: LogDestination;
}

export type SecretStoreProvider = 'vault' | 'onepassword' | 'aws' | 'doppler' | 'infisical';

export interface SecretStoreSpec {
  provider: SecretStoreProvider;
  refreshSeconds?: number;
  vault?: { address: string; mount?: string; namespace?: string; tokenSecret: string };
  onepassword?: { connectUrl: string; vault: string; tokenSecret: string };
  aws?: { region: string; credentialsSecret: string; endpoint?: string };
  doppler?: { tokenSecret: string; endpoint?: string };
  infisical?: { siteUrl?: string; projectId: string; environment: string; path?: string; credentialsSecret: string };
}

export interface SecretSource {
  store: string;
  key: string;
  field?: string;
}

export interface TestResult {
  ok: boolean;
  items: { target: string; ok: boolean; message: string }[];
}

export interface Incident {
  id: string;
  subject: string;
  startedAt: string;
  resolvedAt: string | null;
  value: number | null;
  message: string;
  notifications: { channel: string; event: string; status: 'pending' | 'sent' | 'failed'; error: string }[];
}

export interface NotificationRecord {
  id: string;
  event: 'firing' | 'resolved' | 'repeat' | 'test';
  alert: string;
  subject: string;
  status: 'pending' | 'sent' | 'failed';
  attempts: number;
  lastError: string;
  createdAt: string;
  sentAt: string | null;
}

/** A file in a bucket (L2-013 OBJ-01). */
export interface ObjectInfo {
  key: string;
  size: number;
  modified: string;
  contentType: string;
  tags?: Record<string, string>;
}
/** One folder of a bucket, one page at a time. */
export interface ObjectList {
  prefix: string;
  folders: string[];
  items: ObjectInfo[];
  nextCursor: string | null;
}
