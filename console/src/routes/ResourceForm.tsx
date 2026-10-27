import { Alert, Button, Checkbox, Choice, ConfirmDialog, describedBy, Field, FieldHint, Fieldset, Form, FormRow, Inline, Input, Select, Text, Textarea } from '@enclavestack/ui';
import { useState, type FormEvent } from 'react';
import { useApi } from '../api/context';
import { RULES } from '../api/generated';
import { ApiError, type Algorithm, type AppSpec, type BackupSpec, type DatabaseEngine, type DatabaseSpec, type DnsProvider, type DomainSpec, type FirewallSpec, type FunctionSpec, type JobSpec, type LoadBalancerSpec, type NetworkSpec, type Overlap, type HealthCheck, type HealthCheckType, type Kind, type RecordSpec, type RecordType, type Resource, type SnapshotSpec, type Strategy, type TlsMode, type TunnelProvider, type VMSpec, type VolumeSpec, type VolumeType, type ZoneSpec } from '../api/types';
import { isPlaced, MachineFields, usePlacement } from '../components/MachineFields';
import { BucketAccessEditor, grantRows, grantsOf, type GrantRow } from '../components/AccessFields';
import { DiskPicker, freeDisks, IMAGE_TITLES, MountEditor, mountRows, mountsOf, VM_SIZES, type MountRow } from '../components/ComputeFields';
import { DATA_IMMUTABLE, DATA_KINDS, useDataForm } from '../components/DataFields';
import { TriggerEditor, triggerRows, triggersOf, type TriggerRow } from '../components/FunctionFields';
import { OPERATE_IMMUTABLE, OPERATE_KINDS, useOperateForm } from '../components/OperateFields';
import { useDomainPublic, useTunnelForm } from '../components/PublicAccessFields';
import { useProject } from '../lib/project';
import { hasInternet, PlacementFields, RuleEditor, ruleRows, rulesOf, TargetChecklist, TargetEditor, targetRows, targetsOf, type RuleRow, type TargetRow } from '../components/NetworkingFields';
import { fromRows, KeyValueEditor, toRows, type Row } from '../components/KeyValueEditor';
import { inSentence, sectionByKind } from '../lib/sections';
import { useLoad } from '../lib/useLoad';
import { describeSchedule, nextRuns } from '../lib/schedule';
import { cidrSize, parseCidr, validateName, validateNetwork, validateSpec, type FieldErrors } from '../lib/validation';

interface Props {
  kind: Kind;
  /** Present when editing. */
  existing?: Resource;
  onDone: (r: Resource) => void;
  onReload?: () => void;
  /** Pre-fills a new domain's app ("Add domain" on an app). */
  initialApp?: string;
  /** Pre-fills a new backup's source ("Set up backups" on a database or cache). */
  initialSource?: { kind: 'database' | 'cache' | 'volume'; name: string };
  /** Pre-fills a new record's zone ("Add record" on a zone). */
  initialZone?: string;
  /** Pre-fills a new volume's or VM's snapshot ("Create volume", "Launch VM" on a snapshot). */
  initialSnapshot?: string;
  /** Pre-fills a new snapshot's source ("Take snapshot" on a volume or VM). */
  initialSource2?: string;
  /** Pre-fills a new tunnel's way in (Install on a Public access card, Make public). */
  initialProvider?: TunnelProvider;
  /** Pre-fills a new domain's tunnel (Make public through a domain tunnel). */
  initialTunnel?: string;
}

/** The database engines with one line each on what they are for (L2.5-002 §13). */
const DB_ENGINES: { engine: DatabaseEngine; title: string; description: string }[] = [
  { engine: 'postgres', title: 'PostgreSQL', description: 'The all-rounder: relational, JSON, extensions.' },
  { engine: 'mysql', title: 'MySQL', description: 'For apps written for MySQL, like many PHP and WordPress stacks.' },
  { engine: 'mongodb', title: 'MongoDB', description: 'Documents instead of tables.' },
  { engine: 'clickhouse', title: 'ClickHouse', description: 'Analytics: fast queries over billions of rows.' },
];

/** What in an update restarts a running VM (VM-01, VOL-02): the same list as pkg/api VMRestarts. */
export function vmRestarts(before: VMSpec, after: VMSpec): string[] {
  const why: string[] = [];
  if ((before.cpus ?? RULES.vmCpusDefault) !== (after.cpus ?? RULES.vmCpusDefault)) why.push('CPUs');
  if ((before.memoryMiB ?? RULES.vmMemoryDefault) !== (after.memoryMiB ?? RULES.vmMemoryDefault)) why.push('memory');
  if (JSON.stringify(before.disks ?? []) !== JSON.stringify(after.disks ?? [])) why.push('disks');
  return why;
}

/** The schedule in words, plus the next runs when they can be shown in this browser's zone. */
function scheduleHint(schedule: string, timeZone: string): string {
  if (!schedule.trim()) return 'Like 0 3 * * * (03:00 daily) or @hourly.';
  const words = describeSchedule(schedule, timeZone.trim() || undefined);
  const local = Intl.DateTimeFormat().resolvedOptions().timeZone;
  if (words === schedule || (timeZone.trim() && timeZone.trim() !== local)) return words;
  try {
    const next = nextRuns(schedule, new Date(), 3).map((d) => d.toLocaleString(undefined, { weekday: 'short', hour: '2-digit', minute: '2-digit' }));
    return next.length ? `${words}. Next: ${next.join(', ')}` : words;
  } catch {
    return words;
  }
}

// Create and edit forms for every kind (L2-001 CON-14, CON-15; L2.5-002 §1–2).
export function ResourceForm({ kind, existing, onDone, onReload, initialApp, initialSource, initialZone, initialSnapshot, initialSource2, initialProvider, initialTunnel }: Props) {
  const api = useApi();
  // A resource stays in its project; a new one goes to the current project (ADR-035, CON-45).
  const { createIn } = useProject();
  const project = existing?.project ?? createIn;
  // Fresh each time the form opens, so secrets made elsewhere (CLI, OpenTofu) are offered.
  const secrets = useLoad(`form-secrets:${kind}:${project}`, () => (kind === 'app' || kind === 'job' || kind === 'function' || kind === 'backup' || kind === 'zone' ? api.listResources('secret', project) : Promise.resolve([])));
  const secretNames = (secrets.data ?? []).filter((s) => s.status.phase !== 'deleting').map((s) => s.name);
  const apps = useLoad(`form-apps:${kind}:${project}`, () => (kind === 'domain' || kind === 'job' ? api.listResources('app', project) : Promise.resolve([])));
  const appNames = (apps.data ?? []).filter((a) => a.status.phase !== 'deleting').map((a) => a.name);
  // A backup's possible sources and buckets.
  const stores = useLoad(`form-stores:${kind}:${project}`, () => (kind === 'backup' ? api.listResources(undefined, project) : Promise.resolve([])));
  const namesOf = (k: Kind) => (stores.data ?? []).filter((r) => r.kind === k && r.status.phase !== 'deleting').map((r) => r.name);
  // v0.4: networks for placement, zones for domains and records, and what firewalls and load balancers can name.
  const everything = useLoad(`form-all:${kind}:${project}`, () => api.listResources(undefined, project));
  const live = (k: Kind) => (everything.data ?? []).filter((r) => r.kind === k && r.status.phase !== 'deleting');
  const networks = live('network') as Resource<'network'>[];
  const zoneNames = live('zone').map((z) => z.name);
  const allApps = live('app').map((a) => a.name);
  const firewallCandidates = RULES.firewallKinds.flatMap((k) => live(k).map((r) => `${k}/${r.name}`));
  // v0.5: filesystem volumes to mount, disk volumes to attach, snapshots to start from.
  const fsVolumes = live('volume').filter((v) => (v.spec as VolumeSpec).type === 'filesystem').map((v) => v.name);
  // Per-app bucket access (L2-011): the project's buckets.
  const bucketNames = live('bucket').map((b) => b.name);
  const snapshots = live('snapshot').filter((x) => x.status.phase === 'ready');
  const machine = useLoad(`form-machine:${kind}`, () => (kind === 'vm' ? api.getMachine() : Promise.resolve(null)));
  const noVMs = kind === 'vm' && !existing && machine.data?.vms && !machine.data.vms.available ? machine.data.vms.message : '';
  const peerCandidates = [...live('network').map((n) => `network/${n.name}`), ...firewallCandidates];
  const editing = Boolean(existing);
  // One loose view over every kind's spec; each form only reads its own fields.
  const spec = (existing?.spec ?? {}) as Partial<AppSpec> & { version?: string; maxMemoryMiB?: number; versioning?: boolean };
  const dspec = (existing?.spec ?? {}) as Partial<DomainSpec>;
  const jspec = (existing?.spec ?? {}) as Partial<JobSpec>;
  const bspec = (existing?.spec ?? {}) as Partial<BackupSpec>;
  const nspec = (existing?.spec ?? {}) as Partial<NetworkSpec>;
  const fspec = (existing?.spec ?? {}) as Partial<FirewallSpec>;
  const lspec = (existing?.spec ?? {}) as Partial<LoadBalancerSpec>;
  const zspec = (existing?.spec ?? {}) as Partial<ZoneSpec>;
  const rspec = (existing?.spec ?? {}) as Partial<RecordSpec>;
  const placedSpec = (existing?.spec ?? {}) as { network?: string; subnet?: string };
  // Read only by their own kind: an app's publish is a flag, a VM's a list of ports.
  const vspec = (kind === 'vm' ? (existing?.spec ?? {}) : {}) as Partial<VMSpec>;
  const volSpec = (kind === 'volume' ? (existing?.spec ?? {}) : {}) as Partial<VolumeSpec>;
  const snapSpec = (kind === 'snapshot' ? (existing?.spec ?? {}) : {}) as Partial<SnapshotSpec>;
  // v0.10 (L2-016 CON-82, CON-84, CON-86).
  const fnSpec = (kind === 'function' ? (existing?.spec ?? {}) : {}) as Partial<FunctionSpec>;
  const dbSpec = (kind === 'database' ? (existing?.spec ?? {}) : {}) as Partial<DatabaseSpec>;

  const [name, setName] = useState(existing?.name ?? '');
  const [image, setImage] = useState(spec.image ?? '');
  const [deployFrom, setDeployFrom] = useState<'image' | 'git'>(spec.source ? 'git' : 'image');
  const [gitUrl, setGitUrl] = useState(spec.source?.git ?? '');
  const [gitRef, setGitRef] = useState(spec.source?.ref ?? 'main');
  const [gitDockerfile, setGitDockerfile] = useState(spec.source?.dockerfile ?? RULES.gitDockerfileDefault);
  const [gitContext, setGitContext] = useState(spec.source?.context ?? RULES.gitContextDefault);
  const [gitCreds, setGitCreds] = useState(spec.source?.credentialsSecret ?? '');
  const [buildArgs, setBuildArgs] = useState<Row[]>(toRows(spec.source?.buildArgs));
  const [webhookSecret, setWebhookSecret] = useState(spec.source?.webhookSecret ?? '');
  const [deployOnPush, setDeployOnPush] = useState(spec.source?.deployOnPush ?? false);
  const [previewsOn, setPreviewsOn] = useState(spec.previews?.enabled ?? false);
  const [previewTtl, setPreviewTtl] = useState(String(spec.previews?.ttlHours ?? RULES.previewTtlDefault));
  const [previewMax, setPreviewMax] = useState(String(spec.previews?.maxActive ?? RULES.previewMaxActiveDefault));
  const [previewSuffix, setPreviewSuffix] = useState(spec.previews?.domain?.suffix ?? '');
  const [fnImage, setFnImage] = useState(fnSpec.image ?? '');
  const [fnTimeout, setFnTimeout] = useState(String(fnSpec.timeoutSeconds ?? RULES.functionTimeoutDefault));
  const [fnConcurrency, setFnConcurrency] = useState(String(fnSpec.concurrency ?? RULES.functionConcurrencyDefault));
  const [triggers, setTriggers] = useState<TriggerRow[]>(triggerRows(fnSpec.triggers));
  const [dbTls, setDbTls] = useState(dbSpec.tls ?? false);
  const [dbExposed, setDbExposed] = useState(dbSpec.exposed ?? false);
  const [dbInit, setDbInit] = useState(dbSpec.initScript ?? '');
  const [port, setPort] = useState(spec.port ? String(spec.port) : '');
  const [publish, setPublish] = useState(spec.publish ?? false);
  const [memory, setMemory] = useState(String(spec.memoryMiB ?? (kind === 'function' ? RULES.functionMemoryDefault : RULES.appMemoryDefault)));
  const [command, setCommand] = useState((spec.command ?? []).join('\n'));
  const [env, setEnv] = useState<Row[]>(toRows(spec.env));
  const [secretEnv, setSecretEnv] = useState<Row[]>(toRows(spec.secretEnv));
  const [dbEngine, setDbEngine] = useState<DatabaseEngine>((kind === 'database' ? (existing?.spec as DatabaseSpec | undefined)?.engine : undefined) ?? 'postgres');
  const [version, setVersion] = useState(spec.version ?? RULES.defaultDatabaseVersion);
  const data = useDataForm(kind, existing);
  const operate = useOperateForm(kind, existing);
  const tunnelForm = useTunnelForm(existing, { provider: initialProvider, app: initialApp });
  const pub = useDomainPublic(existing, initialTunnel);
  const edgeMachines = useLoad(`form-edge:${kind}`, () => (kind === 'tunnel' ? api.listMachines() : Promise.resolve([])));
  const [maxMemory, setMaxMemory] = useState(String(spec.maxMemoryMiB ?? RULES.cacheMemoryDefault));
  const [versioning, setVersioning] = useState(spec.versioning ?? false);
  const [strategy, setStrategy] = useState<Strategy>(spec.strategy ?? 'rolling');
  const hc = spec.healthCheck;
  const [hcType, setHcType] = useState<HealthCheckType | ''>(hc?.type ?? '');
  const [hcPath, setHcPath] = useState(hc?.path ?? '/');
  const [hcPort, setHcPort] = useState(hc?.port ? String(hc.port) : '');
  const [hcCommand, setHcCommand] = useState((hc?.command ?? []).join(' '));
  const [hcInterval, setHcInterval] = useState(String(hc?.intervalSeconds ?? RULES.healthIntervalDefault));
  const [hcRetries, setHcRetries] = useState(String(hc?.retries ?? RULES.healthRetriesDefault));
  const [hcStart, setHcStart] = useState(String(hc?.startPeriodSeconds ?? 0));
  const [hostname, setHostname] = useState(dspec.hostname ?? '');
  const [domainApp, setDomainApp] = useState(dspec.app ?? initialApp ?? '');
  const [domainPort, setDomainPort] = useState(dspec.port ? String(dspec.port) : '');
  const [tls, setTls] = useState<TlsMode | ''>(existing ? (dspec.tls ?? '') : '');
  const [redirect, setRedirect] = useState(dspec.redirectHttp ?? true);
  const [schedule, setSchedule] = useState(jspec.schedule ?? (kind === 'backup' ? RULES.backupScheduleDefault : ''));
  const [timeZone, setTimeZone] = useState(jspec.timeZone ?? '');
  const [runWith, setRunWith] = useState<'app' | 'image'>(jspec.image ? 'image' : 'app');
  const [jobApp, setJobApp] = useState(jspec.app ?? '');
  const [jobImage, setJobImage] = useState(jspec.image ?? '');
  const [jobTimeout, setJobTimeout] = useState(String(jspec.timeoutSeconds ?? RULES.jobTimeoutDefault));
  const [overlap, setOverlap] = useState<Overlap>(jspec.overlap ?? 'forbid');
  const [sourceKind, setSourceKind] = useState<'database' | 'cache' | 'volume'>(bspec.source?.cache ? 'cache' : bspec.source?.volume ? 'volume' : (initialSource?.kind ?? 'database'));
  const [sourceName, setSourceName] = useState(bspec.source?.database ?? bspec.source?.cache ?? bspec.source?.volume ?? initialSource?.name ?? '');
  const [destKind, setDestKind] = useState<'bucket' | 's3'>(bspec.destination?.s3 ? 's3' : 'bucket');
  const [destBucket, setDestBucket] = useState(bspec.destination?.bucket ?? '');
  const [s3Endpoint, setS3Endpoint] = useState(bspec.destination?.s3?.endpoint ?? '');
  const [s3Region, setS3Region] = useState(bspec.destination?.s3?.region ?? '');
  const [s3Bucket, setS3Bucket] = useState(bspec.destination?.s3?.bucket ?? '');
  const [s3Creds, setS3Creds] = useState(bspec.destination?.s3?.credentialsSecret ?? '');
  const [prefix, setPrefix] = useState(bspec.prefix ?? RULES.backupPrefixDefault);
  const [keep, setKeep] = useState(String(bspec.keep ?? RULES.backupKeepDefault));
  const [keepDays, setKeepDays] = useState(bspec.keepDays ? String(bspec.keepDays) : '');
  const [backupTimeout, setBackupTimeout] = useState(String(bspec.timeoutSeconds ?? RULES.backupTimeoutDefault));
  const [network, setNetwork] = useState(placedSpec.network ?? '');
  const [subnet, setSubnet] = useState(placedSpec.subnet ?? '');
  const [replicas, setReplicas] = useState(String(spec.replicas ?? RULES.replicasDefault));
  const [domainZone, setDomainZone] = useState(dspec.zone ?? '');
  const [cidr, setCidr] = useState(nspec.cidr ?? '');
  const [subnets, setSubnets] = useState<Row[]>((nspec.subnets ?? []).filter((x) => !(nspec.subnets?.length === 1 && x.name === RULES.defaultSubnetName)).map((x) => ({ key: x.name, value: x.cidr })));
  const [internet, setInternet] = useState(nspec.internet ?? true);
  const [fwTargets, setFwTargets] = useState<string[]>(fspec.targets ?? []);
  const [ingress, setIngress] = useState<RuleRow[]>(ruleRows(fspec.ingress, 'from'));
  const [egressDeny, setEgressDeny] = useState(fspec.egressDefault === 'deny');
  const [egress, setEgress] = useState<RuleRow[]>(ruleRows(fspec.egress, 'to'));
  const [lbPort, setLbPort] = useState(lspec.port ? String(lspec.port) : '');
  const [lbProtocol, setLbProtocol] = useState<'http' | 'tcp'>(lspec.protocol ?? 'http');
  const [lbAlgorithm, setLbAlgorithm] = useState<Algorithm>(lspec.algorithm ?? 'round_robin');
  const [lbTargets, setLbTargets] = useState<TargetRow[]>(targetRows(lspec.targets));
  const [lbCheck, setLbCheck] = useState<'tcp' | 'http'>(lspec.healthCheck?.type ?? 'tcp');
  const [lbPath, setLbPath] = useState(lspec.healthCheck?.path ?? '/');
  const [lbPublish, setLbPublish] = useState(lspec.publish ?? false);
  const [provider, setProvider] = useState<DnsProvider>(zspec.provider ?? 'cloudflare');
  const [zoneName, setZoneName] = useState(zspec.zone ?? '');
  const [zoneCreds, setZoneCreds] = useState(zspec.credentialsSecret ?? '');
  const [recZone, setRecZone] = useState(rspec.zone ?? initialZone ?? '');
  const [recName, setRecName] = useState(rspec.name ?? '');
  const [recType, setRecType] = useState<RecordType>(rspec.type ?? 'A');
  const [recMachine, setRecMachine] = useState(rspec.machine ?? false);
  const [recValues, setRecValues] = useState((rspec.values ?? []).join('\n'));
  const [recTtl, setRecTtl] = useState(String(rspec.ttl ?? RULES.recordTtlDefault));
  const [mounts, setMounts] = useState<MountRow[]>(mountRows((existing?.spec as { volumes?: AppSpec['volumes'] } | undefined)?.volumes));
  const [grants, setGrants] = useState<GrantRow[]>(grantRows((existing?.spec as { bucketAccess?: AppSpec['bucketAccess'] } | undefined)?.bucketAccess));
  const startImage = vspec.image ?? (initialSnapshot ? RULES.snapshotImagePrefix + initialSnapshot : 'ubuntu-24.04');
  const [imageFrom, setImageFrom] = useState<'catalog' | 'url' | 'snapshot'>(startImage.startsWith('https://') ? 'url' : startImage.startsWith(RULES.snapshotImagePrefix) ? 'snapshot' : 'catalog');
  const [vmImage, setVmImage] = useState(startImage.startsWith('https://') || startImage.startsWith(RULES.snapshotImagePrefix) ? 'ubuntu-24.04' : startImage);
  const [vmUrl, setVmUrl] = useState(startImage.startsWith('https://') ? startImage : '');
  const [vmSha, setVmSha] = useState(vspec.imageSha256 ?? '');
  const [vmSnapshot, setVmSnapshot] = useState(startImage.startsWith(RULES.snapshotImagePrefix) ? startImage.slice(RULES.snapshotImagePrefix.length) : '');
  const [keysFrom, setKeysFrom] = useState<'paste' | 'secret'>(vspec.sshKeySecret ? 'secret' : 'paste');
  const [vmKeys, setVmKeys] = useState((vspec.sshKeys ?? []).join('\n'));
  const [vmKeySecret, setVmKeySecret] = useState(vspec.sshKeySecret ?? '');
  const [vmUserData, setVmUserData] = useState(vspec.userData ?? '');
  const [vmCpus, setVmCpus] = useState(String(vspec.cpus ?? RULES.vmCpusDefault));
  const [vmMemory, setVmMemory] = useState(String(vspec.memoryMiB ?? RULES.vmMemoryDefault));
  const [vmDisk, setVmDisk] = useState(String(vspec.diskGiB ?? RULES.vmDiskDefault));
  const [vmDisks, setVmDisks] = useState<string[]>(vspec.disks ?? []);
  const [vmPublish, setVmPublish] = useState((vspec.publish ?? []).join(', '));
  const [vmStopped, setVmStopped] = useState(vspec.power === 'stopped');
  const [volType, setVolType] = useState<VolumeType>(volSpec.type ?? 'filesystem');
  const [volSize, setVolSize] = useState(String(volSpec.sizeGiB ?? RULES.volumeSizeDefault));
  const [volSnapshot, setVolSnapshot] = useState(volSpec.snapshot ?? initialSnapshot ?? '');
  const [snapSource, setSnapSource] = useState(snapSpec.source ?? initialSource2 ?? '');
  const [restartWhy, setRestartWhy] = useState<string[] | null>(null);
  const [value, setValue] = useState('');
  const [showValue, setShowValue] = useState(false);
  // A secret's value, or a source in a secret store (EXT-03).
  const source = kind === 'secret' ? (existing?.spec as { source?: { store: string; key: string; field?: string } } | undefined)?.source : undefined;
  const [valueFrom, setValueFrom] = useState<'value' | 'store'>(source ? 'store' : 'value');
  const [srcStore, setSrcStore] = useState(source?.store ?? '');
  const [srcKey, setSrcKey] = useState(source?.key ?? '');
  const [srcField, setSrcField] = useState(source?.field ?? '');

  const placement = usePlacement(existing);
  const machines = useLoad(`form-machines:${kind}`, () => (isPlaced(kind) && !existing ? api.listMachines() : Promise.resolve([])));
  const [errors, setErrors] = useState<FieldErrors>({});
  const [formError, setFormError] = useState<ApiError | null>(null);
  const [busy, setBusy] = useState(false);
  const section = sectionByKind(kind);

  /** Adds network and subnet when set (ADR-020). */
  const placed = <T extends object>(o: T): T => ({ ...o, ...(network ? { network } : {}), ...(network && subnet ? { subnet } : {}) });

  function buildSpec(): Record<string, unknown> {
    switch (kind) {
      case 'stack':
        return {}; // stacks are installed from the catalogue (StackForm)
      case 'network': {
        const n: NetworkSpec = { cidr: cidr.trim(), internet };
        const rows = subnets.filter((r) => r.key.trim() || r.value.trim());
        if (rows.length) n.subnets = rows.map((r) => ({ name: r.key.trim(), cidr: r.value.trim() }));
        return n as unknown as Record<string, unknown>;
      }
      case 'firewall': {
        const f: FirewallSpec = { targets: fwTargets, egressDefault: egressDeny ? 'deny' : 'allow' };
        if (ingress.length) f.ingress = rulesOf(ingress, 'from');
        if (egressDeny && egress.length) f.egress = rulesOf(egress, 'to');
        return f as unknown as Record<string, unknown>;
      }
      case 'loadbalancer': {
        const l: LoadBalancerSpec = { port: Number(lbPort) || 0, protocol: lbProtocol, algorithm: lbAlgorithm, targets: targetsOf(lbTargets), publish: lbPublish,
          healthCheck: lbCheck === 'http' ? { type: 'http', path: lbPath.trim() || '/' } : { type: 'tcp' } };
        return l as unknown as Record<string, unknown>;
      }
      case 'zone':
        return { provider, zone: zoneName.trim().toLowerCase(), credentialsSecret: zoneCreds };
      case 'record': {
        const r: RecordSpec = { zone: recZone, name: recName.trim(), type: recType, ttl: Number(recTtl) || 0 };
        if (recMachine && (recType === 'A' || recType === 'AAAA')) r.machine = true;
        else r.values = recValues.split('\n').map((v) => v.trim()).filter(Boolean);
        return r as unknown as Record<string, unknown>;
      }
      case 'vm': {
        const v: VMSpec = { image: imageFrom === 'url' ? vmUrl.trim() : imageFrom === 'snapshot' ? RULES.snapshotImagePrefix + vmSnapshot : vmImage,
          cpus: Number(vmCpus) || 0, memoryMiB: Number(vmMemory) || 0, diskGiB: Number(vmDisk) || 0, power: vmStopped ? 'stopped' : 'running' };
        if (imageFrom === 'url' && vmSha.trim()) v.imageSha256 = vmSha.trim().toLowerCase();
        if (keysFrom === 'secret') v.sshKeySecret = vmKeySecret;
        else v.sshKeys = vmKeys.split('\n').map((k) => k.trim()).filter((k) => k && !k.startsWith('#'));
        if (vmUserData.trim()) v.userData = vmUserData;
        if (network) v.network = network;
        if (network && subnet) v.subnet = subnet;
        if (vmDisks.length) v.disks = vmDisks;
        const ports = vmPublish.split(/[,\s]+/).filter(Boolean).map(Number);
        if (ports.length) v.publish = ports;
        return v as unknown as Record<string, unknown>;
      }
      case 'volume': {
        const v: VolumeSpec = { type: volType };
        if (volType === 'disk') v.sizeGiB = Number(volSize) || 0;
        if (volSnapshot) v.snapshot = volSnapshot;
        return v as unknown as Record<string, unknown>;
      }
      case 'snapshot':
        return { source: snapSource };
      case 'app': {
        const s: AppSpec = { publish, memoryMiB: Number(memory) || 0 };
        if (deployFrom === 'image') s.image = image.trim();
        else {
          s.source = { git: gitUrl.trim(), ref: gitRef.trim(), dockerfile: gitDockerfile.trim() || RULES.gitDockerfileDefault, context: gitContext.trim() || RULES.gitContextDefault };
          if (gitCreds) s.source.credentialsSecret = gitCreds;
          const args = fromRows(buildArgs);
          if (args) s.source.buildArgs = args;
          if (webhookSecret) s.source.webhookSecret = webhookSecret;
          if (webhookSecret && deployOnPush) s.source.deployOnPush = true;
          if (previewsOn) {
            s.previews = { enabled: true, ttlHours: Number(previewTtl) || 0, maxActive: Number(previewMax) || 0 };
            if (previewSuffix.trim()) s.previews.domain = { ...spec.previews?.domain, suffix: previewSuffix.trim().toLowerCase() };
          } else if (spec.previews) s.previews = { ...spec.previews, enabled: false };
        }
        if (port.trim()) s.port = Number(port);
        const cmd = command.split('\n').map((c) => c.trim()).filter(Boolean);
        if (cmd.length) s.command = cmd;
        const e = fromRows(env);
        if (e) s.env = e;
        const se = fromRows(secretEnv);
        if (se) s.secretEnv = se;
        s.strategy = strategy;
        s.replicas = Number(replicas) || 0;
        if (mounts.length) s.volumes = mountsOf(mounts);
        if (grants.length) s.bucketAccess = grantsOf(grants);
        if (network) s.network = network;
        if (network && subnet) s.subnet = subnet;
        if (hcType) {
          const h: HealthCheck = { type: hcType };
          if (hcType === 'http') h.path = hcPath.trim() || '/';
          if ((hcType === 'http' || hcType === 'tcp') && hcPort.trim()) h.port = Number(hcPort);
          if (hcType === 'command') h.command = hcCommand.split(/\s+/).filter(Boolean);
          if (hcType !== 'none') {
            h.intervalSeconds = Number(hcInterval) || 0;
            h.retries = Number(hcRetries) || 0;
            h.startPeriodSeconds = Number(hcStart) || 0;
          }
          s.healthCheck = h;
        }
        return s as unknown as Record<string, unknown>;
      }
      case 'database':
        return placed({ engine: dbEngine, version, ...(dbTls ? { tls: true } : {}), ...(dbTls && dbExposed ? { exposed: true } : {}), ...(dbInit.trim() ? { initScript: dbInit } : {}) });
      case 'function': {
        const f: FunctionSpec = { image: fnImage.trim(), memoryMiB: Number(memory) || 0, timeoutSeconds: Number(fnTimeout) || 0, concurrency: Number(fnConcurrency) || 0 };
        const cmd = command.split('\n').map((c) => c.trim()).filter(Boolean);
        if (cmd.length) f.command = cmd;
        const e = fromRows(env);
        if (e) f.env = e;
        const se = fromRows(secretEnv);
        if (se) f.secretEnv = se;
        if (triggers.length) f.triggers = triggersOf(triggers);
        if (network) f.network = network;
        if (network && subnet) f.subnet = subnet;
        if (grants.length) f.bucketAccess = grantsOf(grants);
        return f as unknown as Record<string, unknown>;
      }
      case 'cache':
        return placed({ engine: RULES.cacheEngine, version: RULES.cacheVersion, maxMemoryMiB: Number(maxMemory) || 0 });
      case 'bucket':
        return { versioning };
      case 'secret':
        return valueFrom === 'store' ? { source: { store: srcStore, key: srcKey.trim(), ...(srcField.trim() ? { field: srcField.trim() } : {}) } } : { value };
      case 'dashboard':
      case 'alert':
      case 'channel':
      case 'logexport':
      case 'secretstore':
        return operate.build();
      case 'job': {
        const j: JobSpec = { schedule: schedule.trim(), timeoutSeconds: Number(jobTimeout) || 0, overlap, memoryMiB: Number(memory) || 0 };
        if (timeZone.trim()) j.timeZone = timeZone.trim();
        if (runWith === 'app') j.app = jobApp;
        else {
          j.image = jobImage.trim();
          if (network) j.network = network;
          if (network && subnet) j.subnet = subnet;
        }
        const cmd = command.split('\n').map((c) => c.trim()).filter(Boolean);
        if (cmd.length) j.command = cmd;
        const e = fromRows(env);
        if (e) j.env = e;
        const se = fromRows(secretEnv);
        if (se) j.secretEnv = se;
        if (mounts.length) j.volumes = mountsOf(mounts);
        if (runWith !== 'app' && grants.length) j.bucketAccess = grantsOf(grants);
        return j as unknown as Record<string, unknown>;
      }
      case 'backup': {
        const b: BackupSpec = {
          source: { [sourceKind]: sourceName },
          destination: destKind === 'bucket' ? { bucket: destBucket }
            : { s3: { endpoint: s3Endpoint.trim(), ...(s3Region.trim() ? { region: s3Region.trim() } : {}), bucket: s3Bucket.trim(), credentialsSecret: s3Creds } },
          prefix: prefix.trim(), schedule: schedule.trim(), keep: Number(keep) || 0, keepDays: Number(keepDays) || 0, timeoutSeconds: Number(backupTimeout) || 0,
        };
        if (timeZone.trim()) b.timeZone = timeZone.trim();
        return b as unknown as Record<string, unknown>;
      }
      case 'queue':
      case 'topic':
      case 'search':
      case 'transcoder':
      case 'cdn':
      case 'email':
        return data.build();
      case 'domain': {
        const d: DomainSpec = { hostname: hostname.trim().toLowerCase(), app: domainApp, redirectHttp: redirect };
        if (domainPort.trim()) d.port = Number(domainPort);
        if (tls) d.tls = tls;
        if (domainZone) {
          d.zone = domainZone;
          d.tls = 'auto';
        }
        return pub.apply(d, live) as unknown as Record<string, unknown>;
      }
      case 'tunnel':
        return tunnelForm.build();
    }
  }

  /** The spec with its machine and labels (CON-56); an app job runs where its app runs. */
  const machinePlaced = isPlaced(kind) && !(kind === 'job' && runWith === 'app');
  const specOf = () => (machinePlaced ? placement.apply(buildSpec()) : buildSpec());

  function validate(): FieldErrors {
    const e: FieldErrors = {};
    if (!editing) {
      const n = validateName(name);
      if (n) e.name = n;
    }
    if (kind === 'app' && port.trim() && !/^\d+$/.test(port.trim())) e['spec.port'] = 'Use a port from 1 to 65535.';
    if (kind === 'secret' && editing && valueFrom === 'value' && !value && !source) return e; // no new value: nothing to change
    const found = { ...e, ...(kind === 'network' && !editing ? validateNetwork(buildSpec() as unknown as NetworkSpec, networks.map((n) => (n.spec as NetworkSpec).cidr)) : validateSpec(kind, specOf())) };
    if (kind === 'backup') {
      // An empty picker reads better as "choose one" than as the API's exactly-one rule.
      if (!sourceName) found['spec.source'] = `Choose a ${sourceKind}.`;
      if (destKind === 'bucket' && !destBucket) {
        delete found['spec.destination'];
        found['spec.destination.bucket'] = 'Choose a bucket.';
      }
    }
    return found;
  }

  /** For edits: only fields that changed; maps are replaced whole and cleared with null (L3-002 §2). */
  function patch(): Record<string, unknown> {
    const next = specOf();
    if (kind === 'secret') {
      if (valueFrom === 'store') return { source: buildSpec().source, value: null };
      return value ? { value, ...(source ? { source: null } : {}) } : {};
    }
    const prev = existing?.spec as unknown as Record<string, unknown>;
    const out: Record<string, unknown> = {};
    const keys = new Set([...Object.keys(next), ...Object.keys(prev ?? {})]);
    for (const k of keys) {
      // Fields that can't change in place (CP-04) are shown read-only and never sent.
      if (k === 'engine' || (kind === 'database' && (k === 'version' || k === 'initScript')) || (kind === 'backup' && k === 'source') || kind === 'network'
        || (kind === 'zone' && (k === 'provider' || k === 'zone')) || (kind === 'record' && (k === 'zone' || k === 'name' || k === 'type'))
        || (kind === 'vm' && ['image', 'imageSha256', 'sshKeys', 'sshKeySecret', 'userData'].includes(k)) || (kind === 'volume' && (k === 'type' || k === 'snapshot'))
        || kind === 'snapshot' || (kind === 'tunnel' && (k === 'provider' || k === 'machine')) || (DATA_IMMUTABLE[kind] ?? []).includes(k) || (OPERATE_IMMUTABLE[kind] ?? []).includes(k)) continue;
      const a = JSON.stringify(prev?.[k] ?? null);
      const b = JSON.stringify(next[k] ?? null);
      if (a !== b) out[k] = next[k] ?? null;
    }
    return out;
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    const found = validate();
    setErrors(found);
    setFormError(null);
    if (Object.keys(found).length) return;
    // A change that restarts a running VM is confirmed first, naming what restarts it (CON-33).
    if (kind === 'vm' && existing && existing.outputs.power === 'running' && restartWhy === null) {
      const why = vmRestarts(existing.spec as VMSpec, buildSpec() as unknown as VMSpec);
      if (why.length) {
        setRestartWhy(why);
        return;
      }
    }
    await send();
  }

  async function send() {
    setRestartWhy(null);
    setBusy(true);
    try {
      let r: Resource;
      if (existing) {
        const p = patch();
        if (!Object.keys(p).length) {
          onDone(existing);
          return;
        }
        r = await api.updateResource(existing.id, p, existing.generation);
      } else {
        r = await api.createResource(kind, name.trim(), specOf() as never, project);
      }
      onDone(r);
    } catch (err) {
      if (err instanceof ApiError) {
        const fieldErrs: FieldErrors = {};
        for (const d of err.details) fieldErrs[d.field] = d.message;
        setErrors(fieldErrs);
        // Field problems show next to their field; anything else shows above the button.
        if (!err.details.some((d) => d.field === 'name' || d.field.startsWith('spec'))) setFormError(err);
      } else {
        setFormError(new ApiError('INTERNAL', 'Something went wrong. Try again.', 0));
      }
      setBusy(false);
    }
  }

  const onBlur = () => setErrors(validate());
  const err = (f: string) => errors[f];
  // A domain's zone: DNS-01 for a local domain, where its record is written for a public one (PUB-02).
  const domainZoneField = (
    <Field id="f-domain-zone" label={pub.isPublic ? 'Zone' : 'Get the certificate through DNS'} error={err('spec.zone')}
      hint={pub.isPublic ? undefined : domainZone ? `Let's Encrypt checks a TXT record in ${domainZone}: no inbound port is needed, and the hostname may start with *.` : 'Optional. Choose a zone to use DNS-01, needed for wildcard hostnames like *.example.com.'}>
      <Select id="f-domain-zone" value={domainZone} onChange={(e) => setDomainZone(e.target.value)}>
        <option value="">{pub.isPublic ? 'None: I point the record myself' : "No (HTTP-01 or this machine's certificate)"}</option>
        {zoneNames.map((z) => (
          <option key={z} value={z}>{z}</option>
        ))}
      </Select>
    </Field>
  );

  return (
    <Form onSubmit={submit} noValidate>
      {formError && (
        <Alert>
          <Inline justify="between" wrap>
            <span>{formError.message}</span>
            {formError.code === 'PRECONDITION_FAILED' && onReload && <Button size="sm" onClick={onReload}>Reload</Button>}
          </Inline>
          {formError.requestId && <Text size="xs" mono> Request ID {formError.requestId}</Text>}
        </Alert>
      )}

      <Text size="sm" tone="muted">{editing ? 'In' : 'Creates in'} the project <Text weight="semibold" tone="ink">{project}</Text></Text>
      {editing ? (
        <Field id="f-name" label="Name" hint="Names can't change.">
          <Input id="f-name" mono value={name} readOnly aria-readonly="true" />
        </Field>
      ) : (
        <Field id="f-name" label="Name" hint={`3–40 lowercase letters, numbers or hyphens. Other ${inSentence(section.label)} in ${project} can't share it.`} error={err('name')}>
          <Input id="f-name" mono value={name} onChange={(e) => setName(e.target.value)} onBlur={onBlur} autoComplete="off" spellCheck={false}
            aria-invalid={Boolean(err('name'))} aria-describedby={describedBy('f-name', err('name'), true)} placeholder={`my-${section.noun.replace(' ', '-')}`} />
        </Field>
      )}

      {kind === 'app' && (
        <>
          <Fieldset legend="Deploy from">
            <Choice name="deploy-from" value="image" checked={deployFrom === 'image'} onChange={() => setDeployFrom('image')}
              title="An image" description="A prebuilt image from any registry." />
            <Choice name="deploy-from" value="git" checked={deployFrom === 'git'} onChange={() => setDeployFrom('git')}
              title="A Git repository" description="Built here with the repository's Dockerfile. Redeploy builds the latest commit." />
          </Fieldset>
          {deployFrom === 'image' ? (
            <Field id="f-image" label="Image" hint="Any registry image, for example nginx:1.27-alpine or ghcr.io/acme/api:1.4." error={err('spec.image')}>
              <Input id="f-image" mono value={image} onChange={(e) => setImage(e.target.value)} onBlur={onBlur} spellCheck={false}
                aria-invalid={Boolean(err('spec.image'))} aria-describedby={describedBy('f-image', err('spec.image'), true)} />
            </Field>
          ) : (
            <>
              <FormRow>
                <Field id="f-git" label="Repository" hint="https:// or ssh://" error={err('spec.source.git') ?? err('spec.image')}>
                  <Input id="f-git" mono value={gitUrl} onChange={(e) => setGitUrl(e.target.value)} onBlur={onBlur} spellCheck={false}
                    placeholder="https://github.com/acme/api.git" aria-invalid={Boolean(err('spec.source.git'))} />
                </Field>
                <Field id="f-ref" hint="The branch, tag or commit to build, like main or v1.2.0." label="Branch, tag or commit" error={err('spec.source.ref')}>
                  <Input id="f-ref" mono value={gitRef} onChange={(e) => setGitRef(e.target.value)} onBlur={onBlur} spellCheck={false} />
                </Field>
              </FormRow>
              <FormRow>
                <Field id="f-dockerfile" label="Dockerfile" hint="Relative to the context." error={err('spec.source.dockerfile')}>
                  <Input id="f-dockerfile" mono value={gitDockerfile} onChange={(e) => setGitDockerfile(e.target.value)} onBlur={onBlur} spellCheck={false} />
                </Field>
                <Field id="f-context" hint="The folder in the repository the build starts from. A dot means its top." label="Build context" error={err('spec.source.context')}>
                  <Input id="f-context" mono value={gitContext} onChange={(e) => setGitContext(e.target.value)} onBlur={onBlur} spellCheck={false} />
                </Field>
              </FormRow>
              <Field id="f-git-creds" label="Credentials (private repositories)" error={err('spec.source.credentialsSecret')}
                hint={'A secret holding {"token": "…"}, or {"privateKey": "…", "knownHosts": "…"} for ssh.'}>
                <Select id="f-git-creds" value={gitCreds} onChange={(e) => setGitCreds(e.target.value)}>
                  <option value="">None (public repository)</option>
                  {secretNames.map((n) => (
                    <option key={n} value={n}>{n}</option>
                  ))}
                </Select>
              </Field>
              <KeyValueEditor idPrefix="f-bargs" label="Build arguments" rows={buildArgs} onChange={setBuildArgs} errors={errors}
                errorField={(k) => `spec.source.buildArgs.${k}`} addLabel="Add build argument" emptyHint="Not for secrets: build arguments end up in the image's metadata." />
              <Fieldset legend="Pushes and pull requests">
                <Field id="f-webhook-secret" label="Webhook secret" error={err('spec.source.webhookSecret')}
                  hint="Your Git host sends pushes and pull requests to this app, signed with this secret. The app's page shows the address to give it.">
                  <Select id="f-webhook-secret" value={webhookSecret} onChange={(e) => setWebhookSecret(e.target.value)}>
                    <option value="">None: no webhook</option>
                    {secretNames.map((n) => (
                      <option key={n} value={n}>{n}</option>
                    ))}
                  </Select>
                </Field>
                <Checkbox label="Redeploy when the branch gets a push" checked={deployOnPush} disabled={!webhookSecret} onChange={(e) => setDeployOnPush(e.target.checked)} />
                <Checkbox label="Make a preview of each pull request" checked={previewsOn} onChange={(e) => setPreviewsOn(e.target.checked)} />
                {err('spec.previews') && <Text size="sm" tone="bad" role="alert">{err('spec.previews')}</Text>}
                {previewsOn && (
                  <>
                    <FormRow>
                      <Field id="f-preview-ttl" label="Keep for (hours)" hint="A preview without a push for this long is deleted. 1–720." error={err('spec.previews.ttlHours')}>
                        <Input id="f-preview-ttl" inputMode="numeric" value={previewTtl} onChange={(e) => setPreviewTtl(e.target.value)} onBlur={onBlur} />
                      </Field>
                      <Field id="f-preview-max" label="At most" hint="Previews at the same time. 1–20." error={err('spec.previews.maxActive')}>
                        <Input id="f-preview-max" inputMode="numeric" value={previewMax} onChange={(e) => setPreviewMax(e.target.value)} onBlur={onBlur} />
                      </Field>
                    </FormRow>
                    <Field id="f-preview-domain" label="Preview domain" error={err('spec.previews.domain.suffix')}
                      hint={previewSuffix.trim() ? `A preview named web-login answers at https://web-login.${previewSuffix.trim()}, for people signed in to this console.` : 'Optional, like preview.localhost. Without one, open previews from their app page.'}>
                      <Input id="f-preview-domain" mono value={previewSuffix} onChange={(e) => setPreviewSuffix(e.target.value)} onBlur={onBlur} placeholder="preview.localhost" spellCheck={false} />
                    </Field>
                  </>
                )}
              </Fieldset>
            </>
          )}
          <FormRow>
            <Field id="f-port" label="Port" hint="Where the app listens (optional)." error={err('spec.port')}>
              <Input id="f-port" inputMode="numeric" value={port} onChange={(e) => setPort(e.target.value)} onBlur={onBlur}
                aria-invalid={Boolean(err('spec.port'))} aria-describedby={describedBy('f-port', err('spec.port'), true)} />
            </Field>
            <Field id="f-memory" label="Memory (MiB)" hint={`${RULES.appMemoryMin}–${RULES.appMemoryMax}`} error={err('spec.memoryMiB')}>
              <Input id="f-memory" inputMode="numeric" value={memory} onChange={(e) => setMemory(e.target.value)} onBlur={onBlur}
                aria-invalid={Boolean(err('spec.memoryMiB'))} aria-describedby={describedBy('f-memory', err('spec.memoryMiB'), true)} />
            </Field>
          </FormRow>
          {network && !hasInternet(networks, network) ? (
            <Text size="sm" tone="muted">The network {network} has no internet access, so the app can't publish a host port. Use a domain or a published load balancer.</Text>
          ) : (
            <Checkbox label="Publish the port on this machine" checked={publish} onChange={(e) => setPublish(e.target.checked)} />
          )}
          <KeyValueEditor idPrefix="f-env" label="Environment variables" rows={env} onChange={setEnv} errors={errors} errorField={(k) => `spec.env.${k}`}
            addLabel="Add variable" emptyHint="Plain values the app receives at start." />
          <KeyValueEditor idPrefix="f-senv" label="Secret environment variables" rows={secretEnv} onChange={setSecretEnv} options={secretNames} errors={errors}
            errorField={(k) => `spec.secretEnv.${k}`} addLabel="Add secret variable"
            emptyHint={secretNames.length ? 'Values taken from secrets; the console never sees them.' : 'Create a secret first to use it here.'} />
          <Field id="f-command" label="Command" hint="Optional. One argument per line; replaces the image's command.">
            <Textarea id="f-command" mono rows={3} value={command} onChange={(e) => setCommand(e.target.value)} spellCheck={false} />
          </Field>
          <Fieldset legend="Deploys">
            <Field id="f-strategy" label="Deploy strategy" error={err('spec.strategy')}
              hint={publish ? 'Published apps always recreate: the old revision stops before the new one starts.' : 'Rolling starts the new revision and moves traffic only once it is healthy.'}>
              <Select id="f-strategy" value={strategy} onChange={(e) => setStrategy(e.target.value as Strategy)}>
                <option value="rolling">Rolling (no downtime)</option>
                <option value="recreate">Recreate (stop, then start)</option>
              </Select>
            </Field>
            <Field id="f-replicas" label="Replicas" error={err('spec.replicas')}
              hint={publish ? 'An app that publishes a host port runs one replica.' : `Containers per revision, ${RULES.replicasMin}–${RULES.replicasMax}. Domains and load balancers spread requests across them.`}>
              <Input id="f-replicas" inputMode="numeric" value={publish ? '1' : replicas} disabled={publish} onChange={(e) => setReplicas(e.target.value)} onBlur={onBlur} />
            </Field>
            <Field id="f-health" label="Health check" error={err('spec.healthCheck.type')}
              hint={hcType ? 'A new revision takes traffic only after this passes.' : 'Default: TCP on the port, or running for 5 s without restarting.'}>
              <Select id="f-health" value={hcType} onChange={(e) => setHcType(e.target.value as HealthCheckType | '')}>
                <option value="">Default</option>
                <option value="http">HTTP request</option>
                <option value="tcp">TCP connection</option>
                <option value="command">Command in the container</option>
                <option value="none">None</option>
              </Select>
            </Field>
            {(hcType === 'http' || hcType === 'tcp') && (
              <FormRow>
                {hcType === 'http' && (
                  <Field id="f-es-path" hint="The page the check asks for, like /healthz." label="Path" error={err('spec.healthCheck.path')}>
                    <Input id="f-es-path" mono value={hcPath} onChange={(e) => setHcPath(e.target.value)} onBlur={onBlur} spellCheck={false} />
                  </Field>
                )}
                <Field id="f-es-port" label="Check port" hint="Empty: the app's port." error={err('spec.healthCheck.port')}>
                  <Input id="f-es-port" inputMode="numeric" value={hcPort} onChange={(e) => setHcPort(e.target.value)} onBlur={onBlur} />
                </Field>
              </FormRow>
            )}
            {hcType === 'command' && (
              <Field id="f-es-command" label="Check command" hint="Runs inside the container; exit code 0 means healthy." error={err('spec.healthCheck.command')}>
                <Input id="f-es-command" mono value={hcCommand} onChange={(e) => setHcCommand(e.target.value)} onBlur={onBlur} spellCheck={false} placeholder="pg_isready -U app" />
              </Field>
            )}
            {hcType && hcType !== 'none' && (
              <FormRow>
                <Field id="f-es-interval" hint="How often the check runs." label="Interval (s)" error={err('spec.healthCheck.intervalSeconds')}>
                  <Input id="f-es-interval" inputMode="numeric" value={hcInterval} onChange={(e) => setHcInterval(e.target.value)} onBlur={onBlur} />
                </Field>
                <Field id="f-es-retries" hint="Failed checks in a row before a revision counts as unhealthy." label="Retries" error={err('spec.healthCheck.retries')}>
                  <Input id="f-es-retries" inputMode="numeric" value={hcRetries} onChange={(e) => setHcRetries(e.target.value)} onBlur={onBlur} />
                </Field>
                <Field id="f-es-start" hint="How long a new revision may take to start before failed checks count." label="Start period (s)" error={err('spec.healthCheck.startPeriodSeconds')}>
                  <Input id="f-es-start" inputMode="numeric" value={hcStart} onChange={(e) => setHcStart(e.target.value)} onBlur={onBlur} />
                </Field>
              </FormRow>
            )}
          </Fieldset>
          <PlacementFields networks={networks} network={network} subnet={subnet} errors={errors} onChange={(n, sub) => { setNetwork(n); setSubnet(sub); }} />
          <MountEditor rows={mounts} onChange={setMounts} volumes={fsVolumes} errors={errors} />
          <BucketAccessEditor rows={grants} onChange={setGrants} buckets={bucketNames} errors={errors} />
        </>
      )}

      {kind === 'database' && (
        <>
          <Fieldset legend="Engine">
            {DB_ENGINES.map((e) => (
              <Choice key={e.engine} name="db-engine" value={e.engine} checked={dbEngine === e.engine} disabled={editing && dbEngine !== e.engine} title={e.title} description={e.description}
                onChange={() => { setDbEngine(e.engine); setVersion((RULES.databaseDefaultVersions as Record<string, string>)[e.engine] ?? ''); }} />
            ))}
          </Fieldset>
          <Field id="f-version" label={`${DB_ENGINES.find((e) => e.engine === dbEngine)?.title ?? 'Engine'} version`} hint={editing ? 'The engine and version can\'t change; create a new database instead.' : 'Choose the version your app was built for.'} error={err('spec.version') ?? err('spec.engine')}>
            <Select id="f-version" value={version} onChange={(e) => setVersion(e.target.value)} disabled={editing}>
              {((RULES.databaseEngineVersions as Record<string, readonly string[]>)[dbEngine] ?? []).map((v) => (
                <option key={v} value={v}>{v}</option>
              ))}
            </Select>
          </Field>
          <Fieldset legend="Connections">
            {(RULES.tlsEngines as readonly string[]).includes(dbEngine) ? (
              <>
                <Checkbox label="Encrypted connections only (TLS)" checked={dbTls} onChange={(e) => { setDbTls(e.target.checked); if (!e.target.checked) setDbExposed(false); }} />
                <Checkbox label="Reachable from other computers on this network" checked={dbExposed} disabled={!dbTls} onChange={(e) => setDbExposed(e.target.checked)} />
                <FieldHint>{dbExposed ? "It listens on this machine's addresses, not only on this computer. Clients check its certificate with the CA shown on its page." : 'Apps here reach it by name; other computers only when you turn this on, and only with TLS.'}</FieldHint>
                {err('spec.tls') && <Text size="sm" tone="bad" role="alert">{err('spec.tls')}</Text>}
                {err('spec.exposed') && <Text size="sm" tone="bad" role="alert">{err('spec.exposed')}</Text>}
              </>
            ) : (
              <FieldHint>TLS and access from other computers are available for PostgreSQL and MySQL.</FieldHint>
            )}
          </Fieldset>
          <Field id="f-db-init" label={dbEngine === 'mongodb' ? 'Setup script (JavaScript)' : 'Setup script (SQL)'} error={err('spec.initScript')}
            hint={editing ? 'It ran once, when the database was made, and can’t change.' : 'Optional. Runs once when the database is first made: create tables, users or seed rows.'}>
            <Textarea id="f-db-init" mono rows={4} value={dbInit} readOnly={editing} onChange={(e) => setDbInit(e.target.value)} onBlur={onBlur} spellCheck={false}
              placeholder={dbEngine === 'mongodb' ? 'db.createCollection("orders");' : 'CREATE TABLE orders (id serial PRIMARY KEY);'} />
          </Field>
        </>
      )}
      {kind === 'function' && (
        <>
          <Field id="f-fn-image" label="Image" error={err('spec.image')}
            hint={`Started once per event. The event is in ${RULES.eventPath}; ENCLAVE_EVENT_FILE names it.`}>
            <Input id="f-fn-image" mono value={fnImage} onChange={(e) => setFnImage(e.target.value)} onBlur={onBlur} placeholder="ghcr.io/acme/thumbs:1.0" spellCheck={false} />
          </Field>
          <Field id="f-command" label="Command" hint="Optional. One argument per line; replaces the image's command.">
            <Textarea id="f-command" mono rows={3} value={command} onChange={(e) => setCommand(e.target.value)} spellCheck={false} />
          </Field>
          <TriggerEditor rows={triggers} onChange={setTriggers} queues={live('queue').map((q) => q.name)} buckets={bucketNames} errors={errors} onBlur={onBlur} />
          <FormRow>
            <Field id="f-fn-timeout" label="Timeout (s)" error={err('spec.timeoutSeconds')} hint={`A run is stopped after this. ${RULES.functionTimeoutMin}–${RULES.functionTimeoutMax}.`}>
              <Input id="f-fn-timeout" inputMode="numeric" value={fnTimeout} onChange={(e) => setFnTimeout(e.target.value)} onBlur={onBlur} />
            </Field>
            <Field id="f-fn-concurrency" label="Runs at once" error={err('spec.concurrency')} hint={`${RULES.functionConcurrencyMin}–${RULES.functionConcurrencyMax}. More events wait.`}>
              <Input id="f-fn-concurrency" inputMode="numeric" value={fnConcurrency} onChange={(e) => setFnConcurrency(e.target.value)} onBlur={onBlur} />
            </Field>
            <Field id="f-memory" label="Memory (MiB)" hint={`${RULES.appMemoryMin}–${RULES.functionMemoryMax}`} error={err('spec.memoryMiB')}>
              <Input id="f-memory" inputMode="numeric" value={memory} onChange={(e) => setMemory(e.target.value)} onBlur={onBlur} />
            </Field>
          </FormRow>
          <KeyValueEditor idPrefix="f-env" label="Environment variables" rows={env} onChange={setEnv} errors={errors} errorField={(k) => `spec.env.${k}`}
            addLabel="Add variable" emptyHint="Plain values each run receives. Names starting with ENCLAVE_ are set by EnclaveStack." />
          <KeyValueEditor idPrefix="f-senv" label="Secret environment variables" rows={secretEnv} onChange={setSecretEnv} options={secretNames} errors={errors}
            errorField={(k) => `spec.secretEnv.${k}`} addLabel="Add secret variable"
            emptyHint={secretNames.length ? 'Values taken from secrets; the console never sees them.' : 'Create a secret first to use it here.'} />
          <PlacementFields networks={networks} network={network} subnet={subnet} errors={errors} onChange={(n, sub) => { setNetwork(n); setSubnet(sub); }} />
          <BucketAccessEditor rows={grants} onChange={setGrants} buckets={bucketNames} errors={errors} />
        </>
      )}
      {DATA_KINDS.includes(kind) && data.render({ live, editing, errors, onBlur })}
      {kind === 'tunnel' && tunnelForm.render({ live, edgeMachines: (edgeMachines.data ?? []).filter((m) => m.role === 'edge'), editing, errors, onBlur })}
      {OPERATE_KINDS.includes(kind) && operate.render({ live, editing, errors, onBlur })}
      {kind === 'database' && <PlacementFields networks={networks} network={network} subnet={subnet} errors={errors} onChange={(n, sub) => { setNetwork(n); setSubnet(sub); }} />}
      {kind === 'cache' && <PlacementFields networks={networks} network={network} subnet={subnet} errors={errors} onChange={(n, sub) => { setNetwork(n); setSubnet(sub); }} />}

      {kind === 'cache' && (
        <Field id="f-maxmem" label="Max memory (MiB)" hint={`${RULES.cacheMemoryMin}–${RULES.cacheMemoryMax}. Least recently used keys are evicted beyond it.`} error={err('spec.maxMemoryMiB')}>
          <Input id="f-maxmem" inputMode="numeric" value={maxMemory} onChange={(e) => setMaxMemory(e.target.value)} onBlur={onBlur}
            aria-invalid={Boolean(err('spec.maxMemoryMiB'))} aria-describedby={describedBy('f-maxmem', err('spec.maxMemoryMiB'), true)} />
        </Field>
      )}

      {kind === 'bucket' && (
        <Checkbox label="Keep previous versions of objects (versioning)" checked={versioning} onChange={(e) => setVersioning(e.target.checked)} />
      )}

      {kind === 'job' && (
        <>
          <FormRow>
            <Field id="f-schedule" label="Schedule" error={err('spec.schedule')} hint={scheduleHint(schedule, timeZone)}>
              <Input id="f-schedule" mono value={schedule} onChange={(e) => setSchedule(e.target.value)} onBlur={onBlur} placeholder="0 3 * * *" spellCheck={false}
                aria-invalid={Boolean(err('spec.schedule'))} aria-describedby={describedBy('f-schedule', err('spec.schedule'), true)} />
            </Field>
            <Field id="f-tz" label="Time zone" error={err('spec.timeZone')} hint="IANA name. Empty: the machine's.">
              <Input id="f-tz" value={timeZone} onChange={(e) => setTimeZone(e.target.value)} onBlur={onBlur} placeholder="Asia/Kolkata" spellCheck={false} />
            </Field>
          </FormRow>
          <FormRow>
            <Field id="f-run-with" hint="Use an app’s image, environment and secrets, or an image of its own." label="Run with">
              <Select id="f-run-with" value={runWith} onChange={(e) => setRunWith(e.target.value as 'app' | 'image')}>
                <option value="app">An app's image and environment</option>
                <option value="image">Its own image</option>
              </Select>
            </Field>
            {runWith === 'app' ? (
              <Field id="f-job-app" hint="The app whose image, environment and secrets each run uses." label="App" error={err('spec.app')}>
                <Select id="f-job-app" value={jobApp} onChange={(e) => setJobApp(e.target.value)} aria-invalid={Boolean(err('spec.app'))}>
                  <option value="">Choose an app…</option>
                  {appNames.map((a) => (
                    <option key={a} value={a}>{a}</option>
                  ))}
                </Select>
              </Field>
            ) : (
              <Field id="f-job-image" hint="The container image each run starts from, like alpine:3." label="Image" error={err('spec.image') ?? err('spec.app')}>
                <Input id="f-job-image" mono value={jobImage} onChange={(e) => setJobImage(e.target.value)} onBlur={onBlur} placeholder="busybox:1.37" spellCheck={false} />
              </Field>
            )}
          </FormRow>
          <Field id="f-command" label="Command" hint={runWith === 'app' ? "One argument per line. Empty: the app's command." : "One argument per line. Empty: the image's command."}>
            <Textarea id="f-command" mono rows={3} value={command} onChange={(e) => setCommand(e.target.value)} spellCheck={false} />
          </Field>
          <FormRow>
            <Field id="f-job-timeout" label="Timeout (s)" error={err('spec.timeoutSeconds')} hint="A run is stopped after this.">
              <Input id="f-job-timeout" inputMode="numeric" value={jobTimeout} onChange={(e) => setJobTimeout(e.target.value)} onBlur={onBlur} />
            </Field>
            <Field id="f-overlap" hint="What happens when the next run is due before the last one has ended." label="If a run is still going" error={err('spec.overlap')}>
              <Select id="f-overlap" value={overlap} onChange={(e) => setOverlap(e.target.value as Overlap)}>
                <option value="forbid">Skip the next run</option>
                <option value="allow">Run anyway</option>
              </Select>
            </Field>
            <Field id="f-memory" label="Memory (MiB)" hint={`${RULES.appMemoryMin}–${RULES.appMemoryMax}`} error={err('spec.memoryMiB')}>
              <Input id="f-memory" inputMode="numeric" value={memory} onChange={(e) => setMemory(e.target.value)} onBlur={onBlur} />
            </Field>
          </FormRow>
          <KeyValueEditor idPrefix="f-env" label="Environment variables" rows={env} onChange={setEnv} errors={errors} errorField={(k) => `spec.env.${k}`}
            addLabel="Add variable" emptyHint={runWith === 'app' ? "The app's variables apply; these override them." : 'Plain values the run receives.'} />
          <PlacementFields networks={networks} network={runWith === 'app' ? '' : network} subnet={subnet} errors={errors}
            disabledReason={runWith === 'app' ? "A job that runs with an app's image runs in that app's network." : undefined}
            onChange={(n, sub) => { setNetwork(n); setSubnet(sub); }} />
          <KeyValueEditor idPrefix="f-senv" label="Secret environment variables" rows={secretEnv} onChange={setSecretEnv} options={secretNames} errors={errors}
            errorField={(k) => `spec.secretEnv.${k}`} addLabel="Add secret variable"
            emptyHint={secretNames.length ? 'Values taken from secrets; the console never sees them.' : 'Create a secret first to use it here.'} />
          <MountEditor rows={mounts} onChange={setMounts} volumes={fsVolumes} errors={errors} />
          {runWith === 'app'
            ? <Fieldset legend="Bucket access"><FieldHint>A job that runs as an app uses the app's bucket access and keys.</FieldHint></Fieldset>
            : <BucketAccessEditor rows={grants} onChange={setGrants} buckets={bucketNames} errors={errors} />}
        </>
      )}

      {kind === 'backup' && (
        <>
          <FormRow>
            <Field id="f-source-kind" label="Back up" hint={editing ? "The source can't change. Create a new backup instead." : undefined}>
              <Select id="f-source-kind" value={sourceKind} onChange={(e) => { setSourceKind(e.target.value as 'database' | 'cache' | 'volume'); setSourceName(''); }} disabled={editing}>
                <option value="database">A database</option>
                <option value="cache">A cache</option>
                <option value="volume">A volume's files</option>
              </Select>
            </Field>
            <Field id="f-source" label={sourceKind === 'database' ? 'Database' : sourceKind === 'cache' ? 'Cache' : 'Volume'} error={err('spec.source') ?? err('spec.source.volume')}
              hint={sourceKind === 'volume' ? 'A filesystem volume: its files are stored as a .tar.gz. Back up disks with snapshots.' : 'What to back up.'}>
              <Select id="f-source" value={sourceName} onChange={(e) => setSourceName(e.target.value)} disabled={editing} aria-invalid={Boolean(err('spec.source'))}>
                <option value="">Choose a {sourceKind}…</option>
                {(editing && sourceName ? [sourceName] : sourceKind === 'volume' ? fsVolumes : namesOf(sourceKind)).map((n) => (
                  <option key={n} value={n}>{n}</option>
                ))}
              </Select>
            </Field>
          </FormRow>
          <Fieldset legend="Where backups are stored">
            <Field id="f-dest-kind" label="Destination" error={err('spec.destination')}
              hint={destKind === 'bucket' ? 'A bucket on this machine. For protection against losing the machine, use external S3.' : 'Any S3-compatible service: AWS S3, Backblaze B2, Cloudflare R2, Wasabi.'}>
              <Select id="f-dest-kind" value={destKind} onChange={(e) => setDestKind(e.target.value as 'bucket' | 's3')}>
                <option value="bucket">A EnclaveStack bucket</option>
                <option value="s3">External S3</option>
              </Select>
            </Field>
            {destKind === 'bucket' ? (
              <Field id="f-dest-bucket" hint="A bucket of this project that keeps the copies." label="Bucket" error={err('spec.destination.bucket')}>
                <Select id="f-dest-bucket" value={destBucket} onChange={(e) => setDestBucket(e.target.value)} aria-invalid={Boolean(err('spec.destination.bucket'))}>
                  <option value="">Choose a bucket…</option>
                  {namesOf('bucket').map((n) => (
                    <option key={n} value={n}>{n}</option>
                  ))}
                </Select>
              </Field>
            ) : (
              <>
                <Field id="f-s3-endpoint" hint="The address of the S3-compatible storage, like https://storage.example.com." label="Endpoint" error={err('spec.destination.s3.endpoint')}>
                  <Input id="f-s3-endpoint" mono value={s3Endpoint} onChange={(e) => setS3Endpoint(e.target.value)} onBlur={onBlur} spellCheck={false}
                    placeholder="https://s3.eu-central-1.amazonaws.com" aria-invalid={Boolean(err('spec.destination.s3.endpoint'))}
                    aria-describedby={describedBy('f-s3-endpoint', err('spec.destination.s3.endpoint'), false)} />
                </Field>
                <FormRow>
                  <Field id="f-s3-bucket" hint="The bucket’s name at that storage." label="Bucket" error={err('spec.destination.s3.bucket')}>
                    <Input id="f-s3-bucket" mono value={s3Bucket} onChange={(e) => setS3Bucket(e.target.value)} onBlur={onBlur} spellCheck={false} placeholder="acme-backups" />
                  </Field>
                  <Field id="f-s3-region" label="Region" hint="If the service needs one.">
                    <Input id="f-s3-region" mono value={s3Region} onChange={(e) => setS3Region(e.target.value)} spellCheck={false} placeholder="eu-central-1" />
                  </Field>
                </FormRow>
                <Field id="f-s3-creds" label="Credentials secret" error={err('spec.destination.s3.credentialsSecret')}
                  hint={'A secret whose value is {"accessKey": "…", "secretKey": "…"}.'}>
                  <Select id="f-s3-creds" value={s3Creds} onChange={(e) => setS3Creds(e.target.value)} aria-invalid={Boolean(err('spec.destination.s3.credentialsSecret'))}>
                    <option value="">{secretNames.length ? 'Choose a secret…' : 'Create a secret first'}</option>
                    {secretNames.map((n) => (
                      <option key={n} value={n}>{n}</option>
                    ))}
                  </Select>
                </Field>
              </>
            )}
            <Field id="f-prefix" label="Key prefix" error={err('spec.prefix')} hint={`Backups are stored as ${prefix || '…'}/${sourceName || '<source>'}/<time>-<run>.${sourceKind === 'database' ? 'pgdump' : 'rdb'}`}>
              <Input id="f-prefix" mono value={prefix} onChange={(e) => setPrefix(e.target.value)} onBlur={onBlur} spellCheck={false} />
            </Field>
          </Fieldset>
          <FormRow>
            <Field id="f-schedule" label="Schedule" error={err('spec.schedule')} hint={scheduleHint(schedule, timeZone)}>
              <Input id="f-schedule" mono value={schedule} onChange={(e) => setSchedule(e.target.value)} onBlur={onBlur} placeholder={RULES.backupScheduleDefault} spellCheck={false}
                aria-invalid={Boolean(err('spec.schedule'))} aria-describedby={describedBy('f-schedule', err('spec.schedule'), true)} />
            </Field>
            <Field id="f-tz" label="Time zone" error={err('spec.timeZone')} hint="IANA name. Empty: the machine's.">
              <Input id="f-tz" value={timeZone} onChange={(e) => setTimeZone(e.target.value)} onBlur={onBlur} placeholder="Asia/Kolkata" spellCheck={false} />
            </Field>
          </FormRow>
          <FormRow>
            <Field id="f-keep" label="Keep" hint="The newest N backups." error={err('spec.keep')}>
              <Input id="f-keep" inputMode="numeric" value={keep} onChange={(e) => setKeep(e.target.value)} onBlur={onBlur} />
            </Field>
            <Field id="f-keep-days" label="Max age (days)" hint="Optional. The newest is always kept." error={err('spec.keepDays')}>
              <Input id="f-keep-days" inputMode="numeric" value={keepDays} onChange={(e) => setKeepDays(e.target.value)} onBlur={onBlur} />
            </Field>
            <Field id="f-backup-timeout" label="Timeout (s)" hint="A run is stopped after this." error={err('spec.timeoutSeconds')}>
              <Input id="f-backup-timeout" inputMode="numeric" value={backupTimeout} onChange={(e) => setBackupTimeout(e.target.value)} onBlur={onBlur} />
            </Field>
          </FormRow>
        </>
      )}

      {kind === 'domain' && (
        <>
          {editing ? (
            <Field id="f-hostname" label="Hostname" hint="Hostnames can't change. Create a new domain instead.">
              <Input id="f-hostname" mono value={hostname} readOnly aria-readonly="true" />
            </Field>
          ) : (
            <Field id="f-hostname" label="Hostname" error={err('spec.hostname')}
              hint="Like api.example.com. Names ending in .localhost work on this machine with no DNS.">
              <Input id="f-hostname" mono value={hostname} onChange={(e) => setHostname(e.target.value)} onBlur={onBlur} autoComplete="off" spellCheck={false}
                placeholder="api.example.com" aria-invalid={Boolean(err('spec.hostname'))} aria-describedby={describedBy('f-hostname', err('spec.hostname'), true)} />
            </Field>
          )}
          <FormRow>
            <Field id="f-domain-app" hint="The app this address sends visitors to." label="App" error={err('spec.app')}>
              <Select id="f-domain-app" value={domainApp} onChange={(e) => setDomainApp(e.target.value)} aria-invalid={Boolean(err('spec.app'))}>
                <option value="">Choose an app…</option>
                {appNames.map((a) => (
                  <option key={a} value={a}>{a}</option>
                ))}
              </Select>
            </Field>
            <Field id="f-domain-port" label="App port" hint="Empty: the app's port." error={err('spec.port')}>
              <Input id="f-domain-port" inputMode="numeric" value={domainPort} onChange={(e) => setDomainPort(e.target.value)} onBlur={onBlur} />
            </Field>
          </FormRow>
          {!pub.isPublic && (<>
          <Field id="f-tls" label="HTTPS certificate" error={err('spec.tls')}
            hint={tls === 'auto' ? "Let's Encrypt: the hostname's DNS must point to this machine and ports 80 and 443 must reach it."
              : tls === 'internal' ? 'Signed by this machine. Browsers trust it once you install the EnclaveStack root certificate.'
                : tls === 'off' ? 'Plain HTTP only.' : 'Default: internal for .localhost names, otherwise Let\'s Encrypt.'}>
            <Select id="f-tls" value={tls} onChange={(e) => setTls(e.target.value as TlsMode | '')}>
              <option value="">Default</option>
              <option value="auto">Let&apos;s Encrypt (public)</option>
              <option value="internal">EnclaveStack certificate (this machine)</option>
              <option value="off">None (HTTP only)</option>
            </Select>
          </Field>
          {tls !== 'off' && <Checkbox label="Redirect HTTP to HTTPS" checked={redirect} onChange={(e) => setRedirect(e.target.checked)} />}
          </>)}
          {!pub.isPublic && domainZoneField}
          {pub.render({ live, edgeMachines: [], editing, errors, onBlur, host: hostname.trim().toLowerCase(), zone: domainZone, zoneField: domainZoneField })}
        </>
      )}

      {kind === 'network' && (
        <>
          <Field id="f-cidr" label="Address range" error={err('spec.cidr')}
            hint={editing ? "A network's addresses can't change. Create a new network and move resources to it." : (() => {
              const c = parseCidr(cidr);
              return c ? `${cidrSize(c[1]).toLocaleString()} addresses. Private ranges, /16 to /28.` : 'A private IPv4 range like 10.40.0.0/16.';
            })()}>
            <Input id="f-cidr" mono value={cidr} readOnly={editing} onChange={(e) => setCidr(e.target.value)} onBlur={onBlur} placeholder="10.40.0.0/16" spellCheck={false}
              aria-invalid={Boolean(err('spec.cidr'))} aria-describedby={describedBy('f-cidr', err('spec.cidr'), true)} />
          </Field>
          {editing ? (
            <Text size="sm" tone="muted">Subnets: {(nspec.subnets ?? []).map((x) => `${x.name} ${x.cidr}`).join(', ')}</Text>
          ) : (
            <KeyValueEditor idPrefix="f-subnets" label="Subnets" rows={subnets} onChange={setSubnets} errors={Object.fromEntries(subnets.flatMap((r, i) => {
              const msg = errors[`spec.subnets[${i}].name`] ?? errors[`spec.subnets[${i}].cidr`];
              return msg && r.key ? [[r.key, msg]] : [];
            }))} errorField={(k) => k} addLabel="Add subnet" emptyHint="Optional: name ranges inside the network (name → CIDR). Without any, one subnet covers it all." />
          )}
          <Checkbox label="Internet access (nothing in a network without it can publish a host port)" checked={internet} disabled={editing} onChange={(e) => setInternet(e.target.checked)} />
        </>
      )}

      {kind === 'firewall' && (
        <>
          <TargetChecklist candidates={firewallCandidates} selected={fwTargets} onChange={setFwTargets} error={err('spec.targets') ?? err('spec.targets[0]')} />
          <RuleEditor legend="Allow in" dir="from" field="spec.ingress" rows={ingress} onChange={setIngress} peers={peerCandidates} errors={errors} />
          <Checkbox label="Deny other outgoing traffic" checked={egressDeny} onChange={(e) => setEgressDeny(e.target.checked)} />
          {egressDeny && <RuleEditor legend="Allow out" dir="to" field="spec.egress" rows={egress} onChange={setEgress} peers={peerCandidates} errors={errors} />}
          <Text size="sm" tone="muted">Published host ports stay reachable through the machine: firewalls govern traffic between resources and networks.</Text>
        </>
      )}

      {kind === 'loadbalancer' && (
        <>
          <FormRow>
            <Field id="f-lb-port" label="Port" error={err('spec.port')} hint="Where the load balancer listens.">
              <Input id="f-lb-port" inputMode="numeric" value={lbPort} onChange={(e) => setLbPort(e.target.value)} onBlur={onBlur} />
            </Field>
            <Field id="f-lb-protocol" hint="HTTP spreads each request; TCP spreads each connection, for anything that isn’t HTTP." label="Protocol">
              <Select id="f-lb-protocol" value={lbProtocol} onChange={(e) => setLbProtocol(e.target.value as 'http' | 'tcp')}>
                <option value="http">HTTP</option>
                <option value="tcp">TCP</option>
              </Select>
            </Field>
            <Field id="f-lb-algorithm" hint="How the next replica is picked: in turn, the least busy, or always the same one for a client." label="Algorithm">
              <Select id="f-lb-algorithm" value={lbAlgorithm} onChange={(e) => setLbAlgorithm(e.target.value as Algorithm)}>
                <option value="round_robin">Round robin</option>
                <option value="least_conn">Fewest connections</option>
                <option value="source">Same client, same replica</option>
              </Select>
            </Field>
          </FormRow>
          <TargetEditor rows={lbTargets} onChange={setLbTargets} apps={allApps} errors={errors} />
          <FormRow>
            <Field id="f-lb-check" hint="How each replica is checked before it gets traffic." label="Replica check">
              <Select id="f-lb-check" value={lbCheck} onChange={(e) => setLbCheck(e.target.value as 'tcp' | 'http')}>
                <option value="tcp">TCP connection</option>
                <option value="http">HTTP request</option>
              </Select>
            </Field>
            {lbCheck === 'http' && (
              <Field id="f-lb-path" hint="The page the check asks each replica for, like /healthz." label="Path" error={err('spec.healthCheck.path')}>
                <Input id="f-lb-path" mono value={lbPath} onChange={(e) => setLbPath(e.target.value)} onBlur={onBlur} spellCheck={false} />
              </Field>
            )}
          </FormRow>
          <Checkbox label="Also listen on a port of this machine" checked={lbPublish} onChange={(e) => setLbPublish(e.target.checked)} />
        </>
      )}

      {kind === 'zone' && (
        <>
          <Fieldset legend="Provider">
            <Choice name="dns-provider" value="cloudflare" checked={provider === 'cloudflare'} disabled={editing} onChange={() => setProvider('cloudflare')}
              title="Cloudflare" description="An API token with Zone:Read and DNS:Edit on this zone, stored as a secret {&quot;apiToken&quot;: &quot;…&quot;}." />
            <Choice name="dns-provider" value="route53" checked={provider === 'route53'} disabled={editing} onChange={() => setProvider('route53')}
              title="Route 53" description="An IAM key limited to this hosted zone, stored as a secret {&quot;accessKeyId&quot;: &quot;…&quot;, &quot;secretAccessKey&quot;: &quot;…&quot;}." />
          </Fieldset>
          {/* PUB-09, PUB-10: one zone per environment; records EnclaveStack didn't make are left alone. */}
          <Alert tone="info">
            EnclaveStack writes, changes and deletes only the records of its own domains and records. Anything else in the zone, like production on another cloud, is never touched.
            Another environment on the same domain gets its own project and its own subdomain zone, like stage.example.com.
          </Alert>
          <Field id="f-zone" label="Zone" error={err('spec.zone')} hint={editing ? "A zone's provider and name can't change." : 'The zone as it exists at the provider, like example.com or a subdomain zone like stage.example.com. EnclaveStack never creates or deletes zones.'}>
            <Input id="f-zone" mono value={zoneName} readOnly={editing} onChange={(e) => setZoneName(e.target.value)} onBlur={onBlur} placeholder="example.com" spellCheck={false} />
          </Field>
          <Field id="f-zone-creds" hint="A secret holding the provider’s API token, allowed to edit this zone’s DNS." label="Credentials secret" error={err('spec.credentialsSecret')}>
            <Select id="f-zone-creds" value={zoneCreds} onChange={(e) => setZoneCreds(e.target.value)}>
              <option value="">{secretNames.length ? 'Choose a secret…' : 'Create a secret first'}</option>
              {secretNames.map((n) => (
                <option key={n} value={n}>{n}</option>
              ))}
            </Select>
          </Field>
        </>
      )}

      {kind === 'record' && (
        <>
          <FormRow>
            <Field id="f-rec-zone" hint="The DNS zone the record is kept in." label="Zone" error={err('spec.zone')}>
              <Select id="f-rec-zone" value={recZone} disabled={editing} onChange={(e) => setRecZone(e.target.value)}>
                <option value="">Choose a zone…</option>
                {(editing && recZone ? [recZone] : zoneNames).map((z) => (
                  <option key={z} value={z}>{z}</option>
                ))}
              </Select>
            </Field>
            <Field id="f-rec-name" label="Record name" error={err('spec.name')} hint={editing ? "Zone, name and type can't change." : 'Inside the zone: api, @ for the zone itself, *.dev for a wildcard.'}>
              <Input id="f-rec-name" mono value={recName} readOnly={editing} onChange={(e) => setRecName(e.target.value)} onBlur={onBlur} placeholder="api" spellCheck={false} />
            </Field>
            <Field id="f-rec-type" hint="A and AAAA point at addresses, CNAME at another name, TXT holds text." label="Type" error={err('spec.type')}>
              <Select id="f-rec-type" value={recType} disabled={editing} onChange={(e) => setRecType(e.target.value as RecordType)}>
                {RULES.recordTypes.map((t) => (
                  <option key={t} value={t}>{t}</option>
                ))}
              </Select>
            </Field>
          </FormRow>
          {(recType === 'A' || recType === 'AAAA') && (
            <Checkbox label="Point at this machine (its public address, followed when it changes)" checked={recMachine} onChange={(e) => setRecMachine(e.target.checked)} />
          )}
          {!(recMachine && (recType === 'A' || recType === 'AAAA')) && (
            <Field id="f-rec-values" label="Values" error={err('spec.values') ?? err('spec.values[0]')} hint="One per line.">
              <Textarea id="f-rec-values" mono rows={3} value={recValues} onChange={(e) => setRecValues(e.target.value)} onBlur={onBlur} spellCheck={false} />
            </Field>
          )}
          <Field id="f-rec-ttl" hint="How long others may remember the answer before asking again." label="TTL (s)" error={err('spec.ttl')}>
            <Input id="f-rec-ttl" inputMode="numeric" value={recTtl} onChange={(e) => setRecTtl(e.target.value)} onBlur={onBlur} />
          </Field>
        </>
      )}

      {kind === 'vm' && (
        <>
          <Fieldset legend="Image">
            {editing ? (
              <Text size="sm" tone="muted">{vspec.image}. A VM's image, SSH keys and first-boot script can't change: create a new VM for different ones.</Text>
            ) : (
              <>
                {RULES.imageCatalog.map((img) => (
                  <Choice key={img} name="vm-image" value={img} checked={imageFrom === 'catalog' && vmImage === img} onChange={() => { setImageFrom('catalog'); setVmImage(img); }}
                    title={IMAGE_TITLES[img] ?? img} description="Downloaded once, checked against its published checksum, and shared by every VM that uses it." />
                ))}
                <Choice name="vm-image" value="url" checked={imageFrom === 'url'} onChange={() => setImageFrom('url')}
                  title="From a URL" description="Any cloud image (qcow2 or raw, with cloud-init) for this machine's architecture." />
                <Choice name="vm-image" value="snapshot" checked={imageFrom === 'snapshot'} onChange={() => setImageFrom('snapshot')}
                  title="From a snapshot" description="A copy of another VM's disk: its files, with a new host name and keys." />
              </>
            )}
            {!editing && imageFrom === 'url' && (
              <FormRow>
                <Field id="f-vm-url" hint="A link to a cloud image of the operating system to start from." label="Image URL" error={err('spec.image')}>
                  <Input id="f-vm-url" mono value={vmUrl} onChange={(e) => setVmUrl(e.target.value)} onBlur={onBlur} spellCheck={false} placeholder="https://cloud-images.ubuntu.com/…/noble-server-cloudimg-arm64.img" />
                </Field>
                <Field id="f-vm-sha" label="SHA-256" hint="Optional, recommended." error={err('spec.imageSha256')}>
                  <Input id="f-vm-sha" mono value={vmSha} onChange={(e) => setVmSha(e.target.value)} onBlur={onBlur} spellCheck={false} />
                </Field>
              </FormRow>
            )}
            {!editing && imageFrom === 'snapshot' && (
              <Field id="f-vm-snapshot" label="Snapshot" error={err('spec.image')} hint="Disk and VM snapshots. Make the root disk at least as big as the snapshot.">
                <Select id="f-vm-snapshot" value={vmSnapshot} onChange={(e) => setVmSnapshot(e.target.value)}>
                  <option value="">Choose a snapshot…</option>
                  {snapshots.filter((x) => x.outputs.type === 'disk').map((x) => (
                    <option key={x.name} value={x.name}>{x.name} · {(x.spec as SnapshotSpec).source}</option>
                  ))}
                </Select>
              </Field>
            )}
            {!editing && imageFrom === 'catalog' && err('spec.image') && <Text size="sm" tone="bad">{err('spec.image')}</Text>}
          </Fieldset>
          <Fieldset legend="Size">
            <Inline wrap>
              {VM_SIZES.map((z) => (
                <Choice key={z.id} name="vm-size" value={z.id} checked={Number(vmCpus) === z.cpus && Number(vmMemory) === z.memoryMiB}
                  onChange={() => { setVmCpus(String(z.cpus)); setVmMemory(String(z.memoryMiB)); }} title={z.title} description={`${z.cpus} CPU${z.cpus > 1 ? 's' : ''} · ${z.memoryMiB / 1024} GiB`} />
              ))}
            </Inline>
            <FormRow>
              <Field id="f-vm-cpus" label="CPUs" hint={`${RULES.vmCpusMin}–${RULES.vmCpusMax}`} error={err('spec.cpus')}>
                <Input id="f-vm-cpus" inputMode="numeric" value={vmCpus} onChange={(e) => setVmCpus(e.target.value)} onBlur={onBlur} />
              </Field>
              <Field id="f-vm-memory" label="Memory (MiB)" hint={`In steps of ${RULES.vmMemoryStep}`} error={err('spec.memoryMiB')}>
                <Input id="f-vm-memory" inputMode="numeric" value={vmMemory} onChange={(e) => setVmMemory(e.target.value)} onBlur={onBlur} />
              </Field>
              <Field id="f-vm-disk" label="Root disk (GiB)" hint="Grows while the VM runs; it can't shrink." error={err('spec.diskGiB')}>
                <Input id="f-vm-disk" inputMode="numeric" value={vmDisk} onChange={(e) => setVmDisk(e.target.value)} onBlur={onBlur} />
              </Field>
            </FormRow>
          </Fieldset>
          {!editing && (
            <Fieldset legend="Access">
              <Field id="f-vm-keys-from" label="SSH keys" hint="Installed for the user enclave (with sudo) and the image's own user. Keys can't change later.">
                <Select id="f-vm-keys-from" value={keysFrom} onChange={(e) => setKeysFrom(e.target.value as 'paste' | 'secret')}>
                  <option value="paste">Paste public keys</option>
                  <option value="secret">From a secret (one key per line)</option>
                </Select>
              </Field>
              {keysFrom === 'paste' ? (
                <Field id="f-vm-keys" label="Public keys" hint="One per line: the contents of ~/.ssh/id_ed25519.pub." error={err('spec.sshKeys') ?? err('spec.sshKeys[0]')}>
                  <Textarea id="f-vm-keys" mono rows={3} value={vmKeys} onChange={(e) => setVmKeys(e.target.value)} onBlur={onBlur} spellCheck={false} placeholder="ssh-ed25519 AAAA… you@laptop" />
                </Field>
              ) : (
                <Field id="f-vm-key-secret" hint="A secret holding the SSH public key you log in with." label="Key secret" error={err('spec.sshKeySecret')}>
                  <Select id="f-vm-key-secret" value={vmKeySecret} onChange={(e) => setVmKeySecret(e.target.value)}>
                    <option value="">{secretNames.length ? 'Choose a secret…' : 'Create a secret first'}</option>
                    {secretNames.map((n) => (
                      <option key={n} value={n}>{n}</option>
                    ))}
                  </Select>
                </Field>
              )}
            </Fieldset>
          )}
          {!editing && (
            <Field id="f-vm-userdata" label="First boot" error={err('spec.userData')}
              hint={`Optional: a #cloud-config document or a #!/bin/sh script, run once. ${new TextEncoder().encode(vmUserData).length.toLocaleString()} of ${RULES.userDataMaxBytes.toLocaleString()} bytes. Use secrets for passwords.`}>
              <Textarea id="f-vm-userdata" mono rows={5} value={vmUserData} onChange={(e) => setVmUserData(e.target.value)} onBlur={onBlur} spellCheck={false} placeholder={'#!/bin/sh\napt-get install -y htop'} />
            </Field>
          )}
          <PlacementFields networks={networks} network={network} subnet={subnet} errors={errors} onChange={(n, sub) => { setNetwork(n); setSubnet(sub); }} />
          <DiskPicker disks={freeDisks(live('volume'), live('vm'), existing?.name)} selected={vmDisks} onChange={setVmDisks} error={err('spec.disks') ?? err('spec.disks[0]')} />
          {network && !hasInternet(networks, network) ? (
            <Text size="sm" tone="muted">The network {network} has no internet access, so the VM can't publish ports. Its SSH address still works.</Text>
          ) : (
            <Field id="f-vm-publish" label="Publish ports" hint="Optional: VM ports served on the same port of this machine, like 80, 443. SSH has its own address." error={err('spec.publish') ?? err('spec.publish[0]')}>
              <Input id="f-vm-publish" mono value={vmPublish} onChange={(e) => setVmPublish(e.target.value)} onBlur={onBlur} placeholder="80, 443" />
            </Field>
          )}
          {!editing && <Checkbox label="Create it stopped" checked={vmStopped} onChange={(e) => setVmStopped(e.target.checked)} />}
        </>
      )}

      {kind === 'volume' && (
        <>
          <Fieldset legend="Type">
            <Choice name="vol-type" value="filesystem" checked={volType === 'filesystem'} disabled={editing} onChange={() => setVolType('filesystem')}
              title="Filesystem" description="A folder apps and jobs mount; every replica sees the same files, and they survive deploys." />
            <Choice name="vol-type" value="disk" checked={volType === 'disk'} disabled={editing} onChange={() => setVolType('disk')}
              title="Disk" description="An extra drive for one VM. EnclaveStack never formats it: do that inside the VM." />
          </Fieldset>
          {volType === 'disk' && (
            <Field id="f-vol-size" label="Size (GiB)" error={err('spec.sizeGiB')}
              hint={editing ? "Grows while the VM runs; it can't shrink." : `${RULES.volumeSizeMin}–${RULES.volumeSizeMax}. It takes only the space written to it.`}>
              <Input id="f-vol-size" inputMode="numeric" value={volSize} onChange={(e) => setVolSize(e.target.value)} onBlur={onBlur} />
            </Field>
          )}
          <Field id="f-vol-snapshot" label="Start from a snapshot" error={err('spec.snapshot')} hint={editing ? "A volume's snapshot can't change." : 'Optional: a copy of a snapshot of the same type.'}>
            <Select id="f-vol-snapshot" value={volSnapshot} disabled={editing} onChange={(e) => setVolSnapshot(e.target.value)}>
              <option value="">Empty</option>
              {(editing && volSnapshot ? [volSnapshot] : snapshots.filter((x) => x.outputs.type === volType).map((x) => x.name)).map((n) => (
                <option key={n} value={n}>{n}</option>
              ))}
            </Select>
          </Field>
        </>
      )}

      {kind === 'snapshot' && (
        <Field id="f-snap-source" label="Copy" error={err('spec.source')} hint={editing ? 'A snapshot never changes. Take a new one instead.' : 'Taken while in use: a disk as after a power cut; a filesystem file by file.'}>
          <Select id="f-snap-source" value={snapSource} disabled={editing} onChange={(e) => setSnapSource(e.target.value)}>
            <option value="">Choose a volume or VM…</option>
            {(editing ? [snapSource] : [...live('volume').map((v) => `volume/${v.name}`), ...live('vm').map((v) => `vm/${v.name}`)]).map((n) => (
              <option key={n} value={n}>{n}</option>
            ))}
          </Select>
        </Field>
      )}

      {kind === 'secret' && (
        <Fieldset legend="The value">
          <Choice name="value-from" value="value" checked={valueFrom === 'value'} onChange={() => setValueFrom('value')} title="Enter a value" description="Stored encrypted on this machine." />
          <Choice name="value-from" value="store" checked={valueFrom === 'store'} onChange={() => setValueFrom('store')} title="From a secret store"
            description="Read from Vault, 1Password, AWS, Doppler or Infisical, and kept in sync." />
        </Fieldset>
      )}
      {kind === 'secret' && valueFrom === 'store' && (
        <>
          <Field id="f-src-store" label="Secret store" error={err('spec.source.store')} hint={live('secretstore').length ? undefined : 'This project has no secret store yet. Connect one in Secret stores first.'}>
            <Select id="f-src-store" value={srcStore} onChange={(e) => setSrcStore(e.target.value)}>
              <option value="">Choose a store…</option>
              {live('secretstore').map((st) => <option key={st.name} value={st.name}>{st.name}</option>)}
            </Select>
          </Field>
          <FormRow>
            <Field id="f-src-key" label="Key" hint="The path, item or secret name in the store, like shop/stripe." error={err('spec.source.key')}>
              <Input id="f-src-key" mono value={srcKey} onChange={(e) => setSrcKey(e.target.value)} onBlur={onBlur} spellCheck={false} />
            </Field>
            <Field id="f-src-field" label="Field" hint="Optional: one field of it, like key." error={err('spec.source.field')}>
              <Input id="f-src-field" mono value={srcField} onChange={(e) => setSrcField(e.target.value)} onBlur={onBlur} spellCheck={false} />
            </Field>
          </FormRow>
        </>
      )}
      {kind === 'secret' && valueFrom === 'value' && (
        <Field id="f-value" label={editing ? 'New value' : 'Value'} hint={editing ? (source ? 'Replaces the store: the secret stops syncing.' : 'Leave empty to keep the current value.') : 'Stored encrypted. Only Reveal shows it again.'} error={err('spec.value')}>
          <Inline>
            <Input id="f-value" mono type={showValue ? 'text' : 'password'} value={value} onChange={(e) => setValue(e.target.value)} onBlur={onBlur}
              autoComplete="off" spellCheck={false} aria-invalid={Boolean(err('spec.value'))} aria-describedby={describedBy('f-value', err('spec.value'), true)} />
            <Button onClick={() => setShowValue((v) => !v)} aria-label={showValue ? 'Hide value' : 'Show value'}>
              {showValue ? 'Hide' : 'Show'}
            </Button>
          </Inline>
        </Field>
      )}

      {restartWhy && existing && (
        <ConfirmDialog title={`Restart ${existing.name}?`} confirmLabel="Save and restart" busy={busy}
          body={<p>{existing.name} will restart to apply the new {restartWhy.join(', ')}. Programs running in it stop and start again; its disks are kept.</p>}
          onConfirm={() => void send()} onCancel={() => setRestartWhy(null)} />
      )}

      {machinePlaced && !editing && machines.data && <MachineFields placement={placement} machines={machines.data} errors={errors} />}
      {noVMs && <Alert tone="warn">VMs can't be created yet: {noVMs}</Alert>}
      <Button type="submit" variant="dark" size="lg" block disabled={busy || Boolean(noVMs)}>
        {busy ? (editing ? 'Saving…' : 'Creating…') : editing ? 'Save changes' : `Create ${section.noun}`}
      </Button>
    </Form>
  );
}
