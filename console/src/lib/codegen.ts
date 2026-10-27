import { RULES } from '../api/generated';
import type { Placed, StackSpec, AlertSpec, ChannelSpec, DashboardSpec, LogExportSpec, SecretStoreSpec, AppSpec, BackupSpec, BucketGrant, BucketSpec, CacheSpec, CDNSpec, DatabaseSpec, DomainSpec, EmailSpec, FirewallRule, FirewallSpec, FunctionSpec, JobSpec, LoadBalancerSpec, NetworkSpec, QueueSpec, RecordSpec, Resource, SearchSpec, SnapshotSpec, TopicSpec, TranscoderSpec, TunnelSpec, Gate, VMSpec, VolumeMount, VolumeSpec, ZoneSpec } from '../api/types';

// "Use from code" (CON-18): the CLI command and OpenTofu block that produce a resource as it is now,
// so operators can move from clicks to code. Secret values are never included.

const sh = (s: string) => (/^[A-Za-z0-9_./:@=+-]+$/.test(s) ? s : `'${s.replace(/'/g, `'\\''`)}'`);
const hcl = (s: string) => JSON.stringify(s);
const ident = (name: string) => name.replace(/-/g, '_');

/** --network/--subnet (CLI-18) and --machine/--machine-label (CLI-48) flags of a placed resource. */
function placementFlags(p: { network?: string; subnet?: string } & Placed): string {
  return (p.network ? ` --network ${p.network}` : '') + (p.subnet ? ` --subnet ${p.subnet}` : '') + (p.machine ? ` --machine ${p.machine}` : '')
    + Object.entries(p.machineLabels ?? {}).map(([k, v]) => ` --machine-label ${sh(`${k}=${v}`)}`).join('');
}

/** --volume NAME:PATH[:ro] flags of an app or job (CLI-26). */
function mountFlags(ms: VolumeMount[] | undefined): string[] {
  return (ms ?? []).map((m) => `--volume ${sh(`${m.volume}:${m.path}${m.readOnly ? ':ro' : ''}`)}`);
}

/** --bucket flags of apps and jobs (CLI-53). */
function bucketFlags(gs: BucketGrant[] | undefined): string[] {
  return (gs ?? []).map((g) => `--bucket ${sh(`${g.bucket}:${g.access}`)}`);
}

/** The bucket_access attribute of enclave_app and enclave_job (TF-48). */
function grantsHcl(gs: BucketGrant[] | undefined): string | null {
  if (!gs?.length) return null;
  return `  bucket_access = [\n${gs.map((g) => `    { bucket = ${hcl(g.bucket)}, access = ${hcl(g.access)} },`).join('\n')}\n  ]`;
}

/** --rendition, --hls-* and --notify-* of a transcoder (CLI-54); defaults are left out. */
function transcodeOutputFlags(t: TranscoderSpec): string[] {
  const out: string[] = [];
  for (const r of t.renditions ?? []) {
    const audio = r.audioKbps ?? RULES.audioKbpsDefault;
    const gop = r.gopSeconds ?? RULES.gopSecondsDefault;
    const extra = gop !== RULES.gopSecondsDefault ? `:${audio}:${gop}` : audio !== RULES.audioKbpsDefault ? `:${audio}` : '';
    out.push(`--rendition ${r.name}:${r.container}:${r.maxWidth}x${r.maxHeight}:${r.videoKbps}${extra}`);
  }
  if (t.hls) {
    for (const r of t.hls.rungs) out.push(`--hls-rung ${r.name}:${r.maxWidth}x${r.maxHeight}:${r.videoKbps}`);
    if (t.hls.name && t.hls.name !== RULES.hlsNameDefault) out.push(`--hls-name ${t.hls.name}`);
    if (t.hls.segmentSeconds && t.hls.segmentSeconds !== RULES.segmentSecondsDefault) out.push(`--hls-segment ${t.hls.segmentSeconds}`);
    if (t.hls.gopSeconds && t.hls.gopSeconds !== RULES.gopSecondsDefault) out.push(`--hls-gop ${t.hls.gopSeconds}`);
    if (t.hls.audioKbps && t.hls.audioKbps !== RULES.audioKbpsDefault) out.push(`--hls-audio ${t.hls.audioKbps}`);
  }
  if (t.notify?.webhook) out.push(`--notify-webhook ${sh(t.notify.webhook.url)}`, ...(t.notify.webhook.secret ? [`--notify-secret ${t.notify.webhook.secret}`] : []));
  if (t.notify?.topic) out.push(`--notify-topic ${t.notify.topic}`);
  return out;
}

/** renditions, hls and notify of enclave_transcoder (TF-49); defaults are left out. */
function transcodeOutputsHcl(t: TranscoderSpec): string[] {
  const lines: string[] = [];
  const box = (r: { name: string; maxWidth: number; maxHeight: number; videoKbps: number }) =>
    `name = ${hcl(r.name)}, max_width = ${r.maxWidth}, max_height = ${r.maxHeight}, video_kbps = ${r.videoKbps}`;
  if (t.renditions?.length) {
    lines.push('  renditions = [', ...t.renditions.map((r) => {
      const extra = (r.audioKbps && r.audioKbps !== RULES.audioKbpsDefault ? `, audio_kbps = ${r.audioKbps}` : '')
        + (r.gopSeconds && r.gopSeconds !== RULES.gopSecondsDefault ? `, gop_seconds = ${r.gopSeconds}` : '');
      return `    { ${box(r)}, container = ${hcl(r.container)}${extra} },`;
    }), '  ]');
  }
  if (t.hls) {
    lines.push('  hls = {');
    if (t.hls.name && t.hls.name !== RULES.hlsNameDefault) lines.push(`    name = ${hcl(t.hls.name)}`);
    if (t.hls.segmentSeconds && t.hls.segmentSeconds !== RULES.segmentSecondsDefault) lines.push(`    segment_seconds = ${t.hls.segmentSeconds}`);
    if (t.hls.gopSeconds && t.hls.gopSeconds !== RULES.gopSecondsDefault) lines.push(`    gop_seconds = ${t.hls.gopSeconds}`);
    if (t.hls.audioKbps && t.hls.audioKbps !== RULES.audioKbpsDefault) lines.push(`    audio_kbps = ${t.hls.audioKbps}`);
    lines.push('    rungs = [', ...t.hls.rungs.map((r) => `      { ${box(r)} },`), '    ]', '  }');
  }
  if (t.notify) {
    const n = [...(t.notify.webhook ? [`webhook_url = ${hcl(t.notify.webhook.url)}`] : []), ...(t.notify.webhook?.secret ? [`webhook_secret = ${hcl(t.notify.webhook.secret)}`] : []),
      ...(t.notify.topic ? [`topic = ${hcl(t.notify.topic)}`] : [])];
    lines.push('  notify = {', ...n.map((x) => `    ${x}`), '  }');
  }
  return lines;
}

/** The volumes attribute of enclave_app and enclave_job (TF-24). */
function mountsHcl(ms: VolumeMount[] | undefined): string | null {
  if (!ms?.length) return null;
  return `  volumes = [\n${ms.map((m) => `    { volume = ${hcl(m.volume)}, path = ${hcl(m.path)}${m.readOnly ? ', read_only = true' : ''} },`).join('\n')}\n  ]`;
}

/** "tcp 5432,8000-8100 from app/api,10.0.0.0/8" — the CLI's rule syntax (CLI-19). */
function ruleText(r: FirewallRule, dir: 'from' | 'to'): string {
  const ports = r.ports?.length ? ` ${r.ports.join(',')}` : '';
  return `${r.protocol ?? 'tcp'}${ports} ${dir} ${(dir === 'from' ? r.from : r.to)?.join(',') ?? ''}`;
}

/** The gate's flags on `tunnel create` and `domain create`; --yes because --gate none asks first (GATE-05). */
function gateFlags(g: Gate | undefined): string[] {
  if (!g) return [];
  const out = g.mode === 'password' && g.passwordSecret ? [`--gate-password-secret ${g.passwordSecret}`] : [`--gate ${g.mode}`];
  for (const p of g.openPaths ?? []) out.push(`--gate-open-path ${sh(p)}`);
  if (g.mode === 'none') out.push('--yes');
  return out;
}

/** The gate as an HCL object. */
function gateHcl(g: Gate | undefined): string | null {
  if (!g) return null;
  const fields = [`mode = ${hcl(g.mode)}`];
  if (g.passwordSecret) fields.push(`password_secret = ${hcl(g.passwordSecret)}`);
  if (g.openPaths?.length) fields.push(`open_paths = [${g.openPaths.map(hcl).join(', ')}]`);
  return `{ ${fields.join(', ')} }`;
}

/** The CLI command; resources outside the default project name theirs (CLI-37). */
export function cliCommand(r: Resource): string {
  const cmd = kindCommand(r);
  return r.project && r.project !== RULES.defaultProject ? cmd.replace(/^enclave /, `enclave --project ${r.project} `) : cmd;
}

/** "1h30m"-style durations from seconds, as the CLI's duration flags take them. */
const dur = (sec: number) => (sec % 3600 === 0 ? `${sec / 3600}h` : sec % 60 === 0 ? `${sec / 60}m` : `${sec}s`);

function kindCommand(r: Resource): string {
  switch (r.kind) {
    case 'stack': {
      // Secret parameters are never returned, so they can't be shown here.
      const st = r.spec as StackSpec;
      const parts = [`enclave stack create ${r.name}`, st.template ? `--template ${st.template}` : `--template-file ./${r.name}.yaml`];
      if (st.version) parts.push(`--version ${st.version}`);
      for (const [k, v] of Object.entries(st.parameters ?? {})) parts.push(`--param ${sh(`${k}=${String(v)}`)}`);
      return [...parts, '--wait'].join(' \\\n  ');
    }
    case 'dashboard': {
      const d = r.spec as DashboardSpec;
      return [`enclave dashboard create ${r.name}`, ...d.widgets.map((w) => `--widget ${sh(`title=${w.title},metric=${w.metric},resources=${w.resources.join('+')}`)}`), '--wait'].join(' \\\n  ');
    }
    case 'alert': {
      const a = r.spec as AlertSpec;
      const c = a.condition;
      const parts = [`enclave alert create ${r.name}`];
      if (c.type === 'metric') {
        parts.push(`--metric ${c.metric ?? ''}`, ...c.resources.map((x) => `--resource ${x}`), `--${c.operator ?? 'above'} ${c.threshold ?? 0}`);
        if (c.forMinutes && c.forMinutes !== RULES.alertForDefault) parts.push(`--for ${c.forMinutes}m`);
      } else if (c.type === 'event') {
        parts.push(...c.resources.map((x) => `--resource ${x}`), ...(c.events ?? []).map((ev) => `--event ${ev}`));
      } else if (c.type === 'machine_offline') {
        parts.push(...c.resources.map((x) => `--machine-offline ${x.replace(RULES.machineRefPrefix, '')}`));
        if (c.forMinutes && c.forMinutes !== 1) parts.push(`--for ${c.forMinutes}m`);
      } else {
        const flag = { crash_loop: 'crash-loop', failed: 'failed', run_failed: 'run-failed' }[c.type];
        parts.push(...c.resources.map((x) => `--${flag} ${x}`));
        if (c.type === 'crash_loop') {
          if (c.restarts && c.restarts !== RULES.alertRestartsDefault) parts.push(`--restarts ${c.restarts}`);
          if (c.windowMinutes && c.windowMinutes !== RULES.alertWindowDefault) parts.push(`--window ${c.windowMinutes}m`);
        }
      }
      parts.push(...a.channels.map((ch) => `--channel ${ch}`));
      if (a.repeatMinutes) parts.push(`--repeat ${dur(a.repeatMinutes * 60)}`);
      if (a.enabled === false) parts.push('--disabled');
      return [...parts, '--wait'].join(' \\\n  ');
    }
    case 'channel': {
      const c = r.spec as ChannelSpec;
      const parts = [`enclave channel create ${r.name}`];
      if (c.type === 'email' && c.email) parts.push(`--email ${c.email.service}`, `--from ${sh(c.email.from)}`, ...c.email.to.map((t) => `--to ${sh(t)}`));
      if (c.type === 'webhook' && c.webhook) {
        parts.push(`--webhook ${sh(c.webhook.url)}`);
        if (c.webhook.secret) parts.push(`--secret ${c.webhook.secret}`);
      }
      if (c.type === 'slack' || c.type === 'discord' || c.type === 'telegram') parts.push(`--${c.type} ${c.secret ?? ''}`);
      if (c.chatId) parts.push(`--chat-id ${sh(c.chatId)}`);
      return [...parts, '--wait'].join(' \\\n  ');
    }
    case 'logexport': {
      const l = r.spec as LogExportSpec;
      const d = l.destination;
      const parts = [`enclave logexport create ${r.name}`, ...(l.sources ?? []).map((x) => `--source ${x}`)];
      if (d.bucket) parts.push(`--bucket ${d.bucket.bucket}${d.bucket.prefix ? `/${d.bucket.prefix}` : ''}`);
      if (d.s3) {
        parts.push(`--s3-endpoint ${sh(d.s3.endpoint)}`, `--s3-bucket ${d.s3.bucket}`, `--s3-credentials ${d.s3.credentialsSecret}`);
        if (d.s3.region) parts.push(`--s3-region ${d.s3.region}`);
        if (d.s3.prefix) parts.push(`--s3-prefix ${sh(d.s3.prefix)}`);
      }
      const h = d.http ?? d.otlp ?? d.loki;
      if (d.http) parts.push(`--http ${sh(d.http.url ?? '')}`);
      if (d.otlp) parts.push(`--otlp ${sh(d.otlp.endpoint ?? '')}`);
      if (d.loki) parts.push(`--loki ${sh(d.loki.url ?? '')}`, ...Object.entries(d.loki.labels ?? {}).map(([k, v]) => `--label ${sh(`${k}=${v}`)}`));
      if (h?.headersSecret) parts.push(`--headers-secret ${h.headersSecret}`);
      if (l.filter) parts.push(`--filter ${sh(l.filter)}`);
      const def = d.bucket || d.s3 ? RULES.logFlushBucketDefault : RULES.logFlushDefault;
      if (l.flushSeconds && l.flushSeconds !== def) parts.push(`--flush ${dur(l.flushSeconds)}`);
      return [...parts, '--wait'].join(' \\\n  ');
    }
    case 'secretstore': {
      const st = r.spec as SecretStoreSpec;
      const parts = [`enclave secretstore create ${r.name}`, `--provider ${st.provider}`];
      const v = st.vault;
      if (v) {
        parts.push(`--address ${sh(v.address)}`, `--token-secret ${v.tokenSecret}`);
        if (v.mount && v.mount !== RULES.vaultMountDefault) parts.push(`--mount ${sh(v.mount)}`);
        if (v.namespace) parts.push(`--namespace ${sh(v.namespace)}`);
      }
      if (st.onepassword) parts.push(`--connect-url ${sh(st.onepassword.connectUrl)}`, `--vault ${sh(st.onepassword.vault)}`, `--token-secret ${st.onepassword.tokenSecret}`);
      if (st.aws) {
        parts.push(`--region ${st.aws.region}`, `--credentials ${st.aws.credentialsSecret}`);
        if (st.aws.endpoint) parts.push(`--endpoint ${sh(st.aws.endpoint)}`);
      }
      if (st.doppler) {
        parts.push(`--token-secret ${st.doppler.tokenSecret}`);
        if (st.doppler.endpoint) parts.push(`--endpoint ${sh(st.doppler.endpoint)}`);
      }
      const i = st.infisical;
      if (i) {
        parts.push(`--project-id ${sh(i.projectId)}`, `--environment ${sh(i.environment)}`, `--credentials ${i.credentialsSecret}`);
        if (i.siteUrl && i.siteUrl !== RULES.infisicalSiteDefault) parts.push(`--site-url ${sh(i.siteUrl)}`);
        if (i.path && i.path !== '/') parts.push(`--path ${sh(i.path)}`);
      }
      if (st.refreshSeconds && st.refreshSeconds !== RULES.storeRefreshDefault) parts.push(`--refresh ${dur(st.refreshSeconds)}`);
      return [...parts, '--wait'].join(' \\\n  ');
    }
    case 'vm': {
      const v = r.spec as VMSpec;
      const parts = [`enclave vm create ${r.name}`, `--image ${sh(v.image)}`];
      if (v.imageSha256) parts.push(`--sha256 ${v.imageSha256}`);
      if (v.sshKeySecret) parts.push(`--ssh-key-secret ${v.sshKeySecret}`);
      for (const k of v.sshKeys ?? []) parts.push(`--ssh-key ${sh(k)}`);
      if (v.userData) parts.push(`--user-data ./${r.name}-init.sh`);
      if (v.cpus && v.cpus !== RULES.vmCpusDefault) parts.push(`--cpus ${v.cpus}`);
      if (v.memoryMiB && v.memoryMiB !== RULES.vmMemoryDefault) parts.push(`--memory ${v.memoryMiB}`);
      if (v.diskGiB && v.diskGiB !== RULES.vmDiskDefault) parts.push(`--disk ${v.diskGiB}`);
      if (placementFlags(v)) parts.push(placementFlags(v).trim());
      for (const d of v.disks ?? []) parts.push(`--disk-volume ${d}`);
      for (const p of v.publish ?? []) parts.push(`--publish ${p}`);
      if (v.power === 'stopped') parts.push('--stopped');
      parts.push('--wait');
      return parts.join(' \\\n  ');
    }
    case 'volume': {
      const v = r.spec as VolumeSpec;
      const parts = [`enclave volume create ${r.name}`, `--type ${v.type}`];
      if (v.type === 'disk' && v.sizeGiB && v.sizeGiB !== RULES.volumeSizeDefault) parts.push(`--size ${v.sizeGiB}`);
      if (v.snapshot) parts.push(`--snapshot ${v.snapshot}`);
      if (placementFlags(v)) parts.push(placementFlags(v).trim());
      return [...parts, '--wait'].join(' ');
    }
    case 'snapshot':
      return `enclave snapshot create ${r.name} --from ${(r.spec as SnapshotSpec).source} --wait`;
    case 'database': {
      const d = r.spec as DatabaseSpec;
      const engine = d.engine && d.engine !== 'postgres' ? ` --engine ${d.engine}` : '';
      const opts = (d.tls ? ' --tls' : '') + (d.exposed ? ' --exposed' : '') + (d.initScript ? ` --init-script-file ./${r.name}-init.sql` : '');
      return `enclave db create ${r.name}${engine} --version ${d.version}${opts}${placementFlags(d)} --wait`;
    }
    case 'queue': {
      const q = r.spec as QueueSpec;
      const parts = [`enclave queue create ${r.name}`];
      if (q.visibilityTimeoutSeconds && q.visibilityTimeoutSeconds !== RULES.queueVisibilityDefault) parts.push(`--visibility-timeout ${q.visibilityTimeoutSeconds}s`);
      if (q.retentionHours && q.retentionHours !== RULES.queueRetentionDefault) parts.push(`--retention ${q.retentionHours}h`);
      if (q.maxMessageBytes && q.maxMessageBytes !== RULES.messageBytesDefault) parts.push(`--max-message-size ${q.maxMessageBytes}`);
      if (q.maxReceives) parts.push(`--max-receives ${q.maxReceives}`);
      if (q.deadLetterQueue) parts.push(`--dead-letter-queue ${q.deadLetterQueue}`);
      return [...parts, '--wait'].join(' ');
    }
    case 'topic': {
      const t = r.spec as TopicSpec;
      const parts = [`enclave topic create ${r.name}`];
      if (t.retentionHours && t.retentionHours !== RULES.topicRetentionDefault) parts.push(`--retention ${t.retentionHours}h`);
      for (const sub of t.subscriptions ?? []) {
        parts.push(sub.queue ? `--queue ${sub.queue}` : sub.function ? `--function ${sub.function}` : `--webhook ${sh(`${sub.url ?? ''}${sub.secret ? `,secret=${sub.secret}` : ''}`)}`);
      }
      return [...parts, '--wait'].join(' \\\n  ');
    }
    case 'search': {
      const sp = r.spec as SearchSpec;
      const mem = sp.memoryMiB && sp.memoryMiB !== RULES.searchMemoryDefault ? ` --memory ${sp.memoryMiB}` : '';
      return `enclave search create ${r.name} --version ${sp.version ?? '3'}${mem}${placementFlags(sp)} --wait`;
    }
    case 'transcoder': {
      const t = r.spec as TranscoderSpec;
      const parts = [`enclave transcoder create ${r.name}`, `--input ${sh(`${t.input.bucket}/${t.input.prefix ?? ''}`)}`, `--output ${sh(`${t.output.bucket}/${t.output.prefix ?? ''}`)}`,
        ...(t.presets ?? []).map((p) => `--preset ${p}`), ...transcodeOutputFlags(t)];
      if (t.watch === false) parts.push('--no-watch');
      if (t.timeoutSeconds && t.timeoutSeconds !== RULES.transcodeTimeoutDefault) parts.push(`--run-timeout ${t.timeoutSeconds}s`);
      if (t.concurrency && t.concurrency !== RULES.concurrencyDefault) parts.push(`--concurrency ${t.concurrency}`);
      if (t.memoryMiB && t.memoryMiB !== RULES.transcodeMemoryDefault) parts.push(`--memory ${t.memoryMiB}`);
      return [...parts, '--wait'].join(' \\\n  ');
    }
    case 'cdn': {
      const c = r.spec as CDNSpec;
      const parts = [`enclave cdn create ${r.name}`, `--hostname ${c.hostname}`, `--origin ${c.origin}${c.originPort ? `:${c.originPort}` : ''}`];
      if (c.defaultTtlSeconds !== undefined && c.defaultTtlSeconds !== RULES.cdnTtlDefault) parts.push(`--default-ttl ${c.defaultTtlSeconds}s`);
      if (c.maxSizeMiB && c.maxSizeMiB !== RULES.cdnSizeDefault) parts.push(`--max-size ${c.maxSizeMiB}`);
      if (c.zone) parts.push(`--zone ${c.zone}`);
      else if (c.tls) parts.push(`--tls ${c.tls}`);
      return [...parts, '--wait'].join(' \\\n  ');
    }
    case 'email': {
      const e = r.spec as EmailSpec;
      const parts = [`enclave email create ${r.name}`];
      if (e.relay) {
        parts.push(`--relay-host ${e.relay.host}`, `--relay-credentials ${e.relay.credentialsSecret}`);
        if (e.relay.port && e.relay.port !== RULES.relayPortDefault) parts.push(`--relay-port ${e.relay.port}`);
        if (e.relay.tls && e.relay.tls !== 'starttls') parts.push(`--relay-tls ${e.relay.tls}`);
      }
      if (e.domain) parts.push(`--domain ${e.domain}`);
      if (e.keepMessages && e.keepMessages !== RULES.keepMessagesDefault) parts.push(`--keep ${e.keepMessages}`);
      const place = placementFlags(e).trim();
      if (place) parts.push(place);
      return [...parts, '--wait'].join(' \\\n  ');
    }
    case 'cache':
      return `enclave cache create ${r.name} --max-memory ${(r.spec as CacheSpec).maxMemoryMiB ?? 256}${placementFlags(r.spec as CacheSpec)} --wait`;
    case 'network': {
      const n = r.spec as NetworkSpec;
      const parts = [`enclave network create ${r.name}`, `--cidr ${n.cidr}`];
      const subnets = n.subnets ?? [];
      if (!(subnets.length === 1 && subnets[0]?.name === RULES.defaultSubnetName)) for (const sub of subnets) parts.push(`--subnet ${sub.name}=${sub.cidr}`);
      if (n.internet === false) parts.push('--no-internet');
      parts.push('--wait');
      return parts.join(' \\\n  ');
    }
    case 'firewall': {
      const f = r.spec as FirewallSpec;
      const parts = [`enclave firewall create ${r.name}`, ...f.targets.map((t) => `--target ${t}`), ...(f.ingress ?? []).map((x) => `--allow ${sh(ruleText(x, 'from'))}`)];
      if (f.egressDefault === 'deny') parts.push('--egress deny', ...(f.egress ?? []).map((x) => `--allow-out ${sh(ruleText(x, 'to'))}`));
      parts.push('--wait');
      return parts.join(' \\\n  ');
    }
    case 'loadbalancer': {
      const l = r.spec as LoadBalancerSpec;
      const parts = [`enclave lb create ${r.name}`, `--port ${l.port}`];
      if (l.protocol === 'tcp') parts.push('--protocol tcp');
      for (const t of l.targets) parts.push(`--target ${t.app}${t.port ? `:${t.port}` : ''}${t.weight && t.weight !== RULES.lbWeightDefault ? `@${t.weight}` : ''}`);
      if (l.algorithm && l.algorithm !== 'round_robin') parts.push(`--algorithm ${l.algorithm}`);
      if (l.healthCheck?.type === 'http') parts.push(`--health http:${l.healthCheck.path ?? '/'}`);
      if (l.publish) parts.push('--publish');
      if (placementFlags(l)) parts.push(placementFlags(l).trim());
      parts.push('--wait');
      return parts.join(' \\\n  ');
    }
    case 'zone': {
      const z = r.spec as ZoneSpec;
      return `enclave zone create ${r.name} \\\n  --provider ${z.provider} \\\n  --zone ${z.zone} \\\n  --credentials ${z.credentialsSecret} \\\n  --wait`;
    }
    case 'record': {
      const d = r.spec as RecordSpec;
      const parts = [`enclave record create ${r.name}`, `--zone ${d.zone}`, `--name ${sh(d.name)}`, `--type ${d.type}`];
      if (d.machine) parts.push('--machine');
      for (const v of d.values ?? []) parts.push(`--value ${sh(v)}`);
      if (d.ttl && d.ttl !== RULES.recordTtlDefault) parts.push(`--ttl ${d.ttl}`);
      parts.push('--wait');
      return parts.join(' \\\n  ');
    }
    case 'bucket':
      return `enclave bucket create ${r.name}${(r.spec as BucketSpec).versioning ? ' --versioning' : ''} --wait`;
    case 'secret': {
      const src = (r.spec as { source?: { store: string; key: string; field?: string } }).source;
      if (src) return `enclave secret create ${r.name} --from-store ${src.store} --key ${sh(src.key)}${src.field ? ` --field ${sh(src.field)}` : ''} --wait`;
      return `enclave secret create ${r.name} --from-stdin < value.txt`;
    }
    case 'function': {
      const f = r.spec as FunctionSpec;
      const parts = [`enclave function create ${r.name}`, `--image ${sh(f.image)}`];
      for (const t of f.triggers ?? []) {
        if (t.schedule) parts.push(`--schedule ${sh(t.schedule)}`, ...(t.timeZone ? [`--time-zone ${t.timeZone}`] : []));
        if (t.queue) parts.push(`--queue ${t.queue}`);
        if (t.bucket) parts.push(`--on-bucket ${sh(`${t.bucket}${t.prefix ? `/${t.prefix}` : ''}`)}`);
      }
      if (f.timeoutSeconds && f.timeoutSeconds !== RULES.functionTimeoutDefault) parts.push(`--run-timeout ${f.timeoutSeconds}s`);
      if (f.memoryMiB && f.memoryMiB !== RULES.functionMemoryDefault) parts.push(`--memory ${f.memoryMiB}`);
      if (f.concurrency && f.concurrency !== RULES.functionConcurrencyDefault) parts.push(`--concurrency ${f.concurrency}`);
      if (placementFlags(f)) parts.push(placementFlags(f).trim());
      parts.push(...bucketFlags(f.bucketAccess));
      for (const [k, v] of Object.entries(f.env ?? {})) parts.push(`--env ${sh(`${k}=${v}`)}`);
      for (const [k, v] of Object.entries(f.secretEnv ?? {})) parts.push(`--secret-env ${k}=${v}`);
      parts.push('--wait');
      if (f.command?.length) parts.push(`-- ${f.command.map(sh).join(' ')}`);
      return parts.join(' \\\n  ');
    }
    case 'job': {
      const j = r.spec as JobSpec;
      const parts = [`enclave job create ${r.name}`, `--schedule ${sh(j.schedule)}`];
      if (j.timeZone) parts.push(`--time-zone ${j.timeZone}`);
      parts.push(j.app ? `--app ${j.app}` : `--image ${sh(j.image ?? '')}`);
      if (j.timeoutSeconds && j.timeoutSeconds !== RULES.jobTimeoutDefault) parts.push(`--run-timeout ${j.timeoutSeconds}s`);
      if (j.overlap === 'allow') parts.push('--overlap allow');
      if (j.memoryMiB && j.memoryMiB !== RULES.appMemoryDefault) parts.push(`--memory ${j.memoryMiB}`);
      if (placementFlags(j)) parts.push(placementFlags(j).trim());
      parts.push(...mountFlags(j.volumes));
      parts.push(...bucketFlags(j.bucketAccess));
      for (const [k, v] of Object.entries(j.env ?? {})) parts.push(`--env ${sh(`${k}=${v}`)}`);
      for (const [k, v] of Object.entries(j.secretEnv ?? {})) parts.push(`--secret-env ${k}=${v}`);
      parts.push('--wait');
      if (j.command?.length) parts.push(`-- ${j.command.map(sh).join(' ')}`);
      return parts.join(' \\\n  ');
    }
    case 'backup': {
      const b = r.spec as BackupSpec;
      const parts = [`enclave backup create ${r.name}`, b.source.database ? `--source database/${b.source.database}` : b.source.volume ? `--source volume/${b.source.volume}` : `--source cache/${b.source.cache ?? ''}`];
      if (b.destination.s3) {
        const s3 = b.destination.s3;
        parts.push(`--s3-endpoint ${sh(s3.endpoint)}`);
        if (s3.region) parts.push(`--s3-region ${sh(s3.region)}`);
        parts.push(`--s3-bucket ${sh(s3.bucket)}`, `--s3-credentials ${s3.credentialsSecret}`);
      } else parts.push(`--bucket ${b.destination.bucket ?? ''}`);
      if (b.schedule && b.schedule !== RULES.backupScheduleDefault) parts.push(`--schedule ${sh(b.schedule)}`);
      if (b.timeZone) parts.push(`--time-zone ${b.timeZone}`);
      if (b.prefix && b.prefix !== RULES.backupPrefixDefault) parts.push(`--prefix ${sh(b.prefix)}`);
      if (b.keep && b.keep !== RULES.backupKeepDefault) parts.push(`--keep ${b.keep}`);
      if (b.keepDays) parts.push(`--keep-days ${b.keepDays}`);
      if (b.timeoutSeconds && b.timeoutSeconds !== RULES.backupTimeoutDefault) parts.push(`--run-timeout ${b.timeoutSeconds}s`);
      parts.push('--wait');
      return parts.join(' \\\n  ');
    }
    case 'domain': {
      const d = r.spec as DomainSpec;
      const parts = [`enclave domain create ${r.name}`, `--hostname ${d.hostname}`, `--app ${d.app}`];
      if (d.port) parts.push(`--port ${d.port}`);
      if (d.tls && d.tls !== (d.hostname.endsWith(RULES.localhostSuffix) ? 'internal' : 'auto')) parts.push(`--tls ${d.tls}`);
      if (d.redirectHttp === false) parts.push('--no-redirect');
      if (d.zone) parts.push(`--zone ${d.zone}`);
      if (d.tunnel) parts.push(`--tunnel ${d.tunnel}`, ...gateFlags(d.gate));
      for (const e of d.cloudflareAccess?.emails ?? []) parts.push(`--access-email ${sh(e)}`);
      for (const e of d.cloudflareAccess?.emailDomains ?? []) parts.push(`--access-email-domain ${sh(e)}`);
      parts.push('--wait');
      return parts.join(' \\\n  ');
    }
    case 'tunnel': {
      // Public access (L2.5-002 §21): one flag per field the way in uses.
      const t = r.spec as TunnelSpec;
      const parts = [`enclave tunnel create ${r.name}`, `--provider ${t.provider}`];
      if (t.credentialsSecret) parts.push(`--credentials ${t.credentialsSecret}`);
      if (t.accountId) parts.push(`--account-id ${t.accountId}`);
      if (t.machine) parts.push(`--machine ${t.machine}`);
      if (t.app) parts.push(`--app ${t.app}`);
      if (t.port) parts.push(`--port ${t.port}`);
      if (t.mode && t.mode !== 'private') parts.push(`--mode ${t.mode}`);
      if (t.hostname) parts.push(`--hostname ${t.hostname}`);
      if (t.domain) parts.push(`--domain ${t.domain}`);
      if (t.image) parts.push(`--image ${sh(t.image)}`);
      for (const c of t.command ?? []) parts.push(`--command ${sh(c)}`);
      for (const [k, v] of Object.entries(t.env ?? {})) parts.push(`--env ${sh(`${k}=${v}`)}`);
      for (const [k, v] of Object.entries(t.secretEnv ?? {})) parts.push(`--secret-env ${k}=${v}`);
      if (t.url) parts.push(`--url ${sh(t.url)}`);
      parts.push(...gateFlags(t.gate), '--wait');
      return parts.join(' \\\n  ');
    }
    case 'app': {
      const s = r.spec as AppSpec;
      const parts = [`enclave app create ${r.name}`];
      if (s.source) {
        const g = s.source;
        parts.push(`--git ${sh(g.git)}`, `--ref ${sh(g.ref)}`);
        if (g.dockerfile && g.dockerfile !== RULES.gitDockerfileDefault) parts.push(`--dockerfile ${sh(g.dockerfile)}`);
        if (g.context && g.context !== RULES.gitContextDefault) parts.push(`--context ${sh(g.context)}`);
        if (g.credentialsSecret) parts.push(`--git-credentials ${g.credentialsSecret}`);
        for (const [k, v] of Object.entries(g.buildArgs ?? {})) parts.push(`--build-arg ${sh(`${k}=${v}`)}`);
        if (g.webhookSecret) parts.push(`--webhook-secret ${g.webhookSecret}`);
        if (g.deployOnPush) parts.push('--deploy-on-push');
      } else parts.push(`--image ${sh(s.image ?? '')}`);
      const pv = s.previews;
      if (pv?.enabled) {
        parts.push('--previews');
        if (pv.domain) {
          parts.push(`--preview-domain ${pv.domain.suffix}`);
          if (pv.domain.zone) parts.push(`--preview-zone ${pv.domain.zone}`);
          if (pv.domain.tunnel) parts.push(`--preview-tunnel ${pv.domain.tunnel}`);
          if (pv.domain.tls && pv.domain.tls !== (pv.domain.zone || pv.domain.tunnel ? 'auto' : 'internal')) parts.push(`--preview-tls ${pv.domain.tls}`);
        }
        if (pv.ttlHours && pv.ttlHours !== RULES.previewTtlDefault) parts.push(`--preview-ttl ${pv.ttlHours}`);
        if (pv.maxActive && pv.maxActive !== RULES.previewMaxActiveDefault) parts.push(`--preview-max ${pv.maxActive}`);
      }
      if (s.port) parts.push(`--port ${s.port}`);
      if (s.publish) parts.push('--publish');
      if (s.memoryMiB && s.memoryMiB !== 256) parts.push(`--memory ${s.memoryMiB}`);
      for (const [k, v] of Object.entries(s.env ?? {})) parts.push(`--env ${sh(`${k}=${v}`)}`);
      for (const [k, v] of Object.entries(s.secretEnv ?? {})) parts.push(`--secret-env ${k}=${v}`);
      for (const c of s.command ?? []) parts.push(`--command ${sh(c)}`);
      if (s.strategy === 'recreate') parts.push('--strategy recreate');
      if (s.replicas && s.replicas > 1) parts.push(`--replicas ${s.replicas}`);
      if (placementFlags(s)) parts.push(placementFlags(s).trim());
      parts.push(...mountFlags(s.volumes));
      parts.push(...bucketFlags(s.bucketAccess));
      const h = s.healthCheck;
      if (h) {
        const kind = h.type === 'http' ? `http:${h.path ?? '/'}` : h.type === 'tcp' ? (h.port ? `tcp:${h.port}` : 'tcp') : h.type === 'command' ? `cmd:${(h.command ?? []).join(' ')}` : 'none';
        parts.push(`--health ${sh(kind)}`);
        if (h.type !== 'none') {
          if (h.intervalSeconds && h.intervalSeconds !== RULES.healthIntervalDefault) parts.push(`--health-interval ${h.intervalSeconds}`);
          if (h.timeoutSeconds && h.timeoutSeconds !== Math.min(RULES.healthTimeoutDefault, h.intervalSeconds ?? RULES.healthIntervalDefault)) parts.push(`--health-timeout ${h.timeoutSeconds}`);
          if (h.retries && h.retries !== RULES.healthRetriesDefault) parts.push(`--health-retries ${h.retries}`);
          if (h.startPeriodSeconds) parts.push(`--health-start-period ${h.startPeriodSeconds}`);
        }
      }
      parts.push('--wait');
      return parts.join(' \\\n  ');
    }
  }
}

function hclMap(m: Record<string, string> | undefined): string | null {
  const entries = Object.entries(m ?? {});
  if (!entries.length) return null;
  return `{\n${entries.map(([k, v]) => `    ${k} = ${hcl(v)}`).join('\n')}\n  }`;
}

export function tofuBlock(r: Resource): string {
  const lines: string[] = [`  name = ${hcl(r.name)}`];
  if (r.project && r.project !== RULES.defaultProject) lines.push(`  project = ${hcl(r.project)}`);
  let type = r.kind as string;
  const refs = (xs: string[]) => `[${xs.map(hcl).join(', ')}]`;
  const placement = (p: { network?: string; subnet?: string } & Placed) => {
    if (p.network) lines.push(`  network = ${hcl(p.network)}`);
    if (p.subnet) lines.push(`  subnet = ${hcl(p.subnet)}`);
    if (p.machine) lines.push(`  machine = ${hcl(p.machine)}`);
    const labels = hclMap(p.machineLabels);
    if (labels) lines.push(`  machine_labels = ${labels}`);
  };
  const ruleBlock = (x: FirewallRule, dir: 'from' | 'to') => {
    const fields = [`${dir} = [${((dir === 'from' ? x.from : x.to) ?? []).map(hcl).join(', ')}]`];
    if (x.protocol && x.protocol !== 'tcp') fields.push(`protocol = ${hcl(x.protocol)}`);
    if (x.ports?.length) fields.push(`ports = [${x.ports.map(hcl).join(', ')}]`);
    return `    { ${fields.join(', ')} },`;
  };
  switch (r.kind) {
    case 'stack': {
      const st = r.spec as StackSpec;
      lines.push(st.template ? `  template = ${hcl(st.template)}` : `  template_body = file("\${path.module}/${r.name}.yaml")`);
      if (st.version) lines.push(`  template_version = ${hcl(st.version)}`);
      const params = Object.fromEntries(Object.entries(st.parameters ?? {}).map(([k, v]) => [k, String(v)]));
      const map = hclMap(params);
      if (map) lines.push(`  parameters = ${map}`);
      break;
    }
    case 'vm': {
      const v = r.spec as VMSpec;
      lines.push(`  image = ${hcl(v.image)}`);
      if (v.imageSha256) lines.push(`  image_sha256 = ${hcl(v.imageSha256)}`);
      if (v.sshKeySecret) lines.push(`  ssh_key_secret = ${hcl(v.sshKeySecret)}`);
      if (v.sshKeys?.length) lines.push(`  ssh_keys = [\n${v.sshKeys.map((k) => `    ${hcl(k)},`).join('\n')}\n  ]`);
      if (v.userData) lines.push(`  user_data = file("\${path.module}/${r.name}-init.sh")`);
      if (v.cpus && v.cpus !== RULES.vmCpusDefault) lines.push(`  cpus = ${v.cpus}`);
      if (v.memoryMiB && v.memoryMiB !== RULES.vmMemoryDefault) lines.push(`  memory_mib = ${v.memoryMiB}`);
      if (v.diskGiB && v.diskGiB !== RULES.vmDiskDefault) lines.push(`  disk_gib = ${v.diskGiB}`);
      placement(v);
      if (v.disks?.length) lines.push(`  disks = [${v.disks.map(hcl).join(', ')}]`);
      if (v.publish?.length) lines.push(`  publish = [${v.publish.join(', ')}]`);
      if (v.power === 'stopped') lines.push('  power = "stopped"');
      break;
    }
    case 'volume': {
      const v = r.spec as VolumeSpec;
      lines.push(`  type = ${hcl(v.type)}`);
      if (v.type === 'disk' && v.sizeGiB) lines.push(`  size_gib = ${v.sizeGiB}`);
      if (v.snapshot) lines.push(`  snapshot = ${hcl(v.snapshot)}`);
      placement(v);
      break;
    }
    case 'snapshot':
      lines.push(`  source = ${hcl((r.spec as SnapshotSpec).source)}`);
      break;
    case 'database': {
      const d = r.spec as DatabaseSpec;
      if (d.engine && d.engine !== 'postgres') lines.push(`  engine = ${hcl(d.engine)}`);
      lines.push(`  version = ${hcl(d.version)}`);
      if (d.tls) lines.push('  tls = true');
      if (d.exposed) lines.push('  exposed = true');
      if (d.initScript) lines.push(`  init_script = file("\${path.module}/${r.name}-init.sql")`);
      placement(d);
      break;
    }
    case 'queue': {
      const q = r.spec as QueueSpec;
      if (q.visibilityTimeoutSeconds && q.visibilityTimeoutSeconds !== RULES.queueVisibilityDefault) lines.push(`  visibility_timeout_seconds = ${q.visibilityTimeoutSeconds}`);
      if (q.retentionHours && q.retentionHours !== RULES.queueRetentionDefault) lines.push(`  retention_hours = ${q.retentionHours}`);
      if (q.maxMessageBytes && q.maxMessageBytes !== RULES.messageBytesDefault) lines.push(`  max_message_bytes = ${q.maxMessageBytes}`);
      if (q.maxReceives) lines.push(`  max_receives = ${q.maxReceives}`);
      if (q.deadLetterQueue) lines.push(`  dead_letter_queue = ${hcl(q.deadLetterQueue)}`);
      break;
    }
    case 'topic': {
      const t = r.spec as TopicSpec;
      if (t.retentionHours && t.retentionHours !== RULES.topicRetentionDefault) lines.push(`  retention_hours = ${t.retentionHours}`);
      if (t.subscriptions?.length) {
        lines.push(`  subscriptions = [\n${t.subscriptions.map((sub) => (sub.queue ? `    { queue = ${hcl(sub.queue)} },` : sub.function ? `    { function = ${hcl(sub.function)} },`
          : `    { url = ${hcl(sub.url ?? '')}${sub.secret ? `, secret = ${hcl(sub.secret)}` : ''} },`)).join('\n')}\n  ]`);
      }
      break;
    }
    case 'search': {
      const sp = r.spec as SearchSpec;
      if (sp.version && sp.version !== '3') lines.push(`  version = ${hcl(sp.version)}`);
      if (sp.memoryMiB && sp.memoryMiB !== RULES.searchMemoryDefault) lines.push(`  memory_mib = ${sp.memoryMiB}`);
      placement(sp);
      break;
    }
    case 'transcoder': {
      const t = r.spec as TranscoderSpec;
      lines.push(`  input = { bucket = ${hcl(t.input.bucket)}, prefix = ${hcl(t.input.prefix ?? '')} }`);
      lines.push(`  output = { bucket = ${hcl(t.output.bucket)}, prefix = ${hcl(t.output.prefix ?? '')} }`);
      if (t.presets?.length) lines.push(`  presets = [${t.presets.map(hcl).join(', ')}]`);
      lines.push(...transcodeOutputsHcl(t));
      if (t.watch === false) lines.push('  watch = false');
      if (t.timeoutSeconds && t.timeoutSeconds !== RULES.transcodeTimeoutDefault) lines.push(`  timeout_seconds = ${t.timeoutSeconds}`);
      if (t.concurrency && t.concurrency !== RULES.concurrencyDefault) lines.push(`  concurrency = ${t.concurrency}`);
      if (t.memoryMiB && t.memoryMiB !== RULES.transcodeMemoryDefault) lines.push(`  memory_mib = ${t.memoryMiB}`);
      break;
    }
    case 'cdn': {
      const c = r.spec as CDNSpec;
      lines.push(`  hostname = ${hcl(c.hostname)}`, `  origin = ${hcl(c.origin)}`);
      if (c.originPort) lines.push(`  origin_port = ${c.originPort}`);
      if (c.defaultTtlSeconds !== undefined && c.defaultTtlSeconds !== RULES.cdnTtlDefault) lines.push(`  default_ttl_seconds = ${c.defaultTtlSeconds}`);
      if (c.maxSizeMiB && c.maxSizeMiB !== RULES.cdnSizeDefault) lines.push(`  max_size_mib = ${c.maxSizeMiB}`);
      if (c.zone) lines.push(`  zone = ${hcl(c.zone)}`);
      else if (c.tls) lines.push(`  tls = ${hcl(c.tls)}`);
      break;
    }
    case 'email': {
      const e = r.spec as EmailSpec;
      if (e.relay) {
        const extra = [e.relay.port && e.relay.port !== RULES.relayPortDefault ? `, port = ${e.relay.port}` : '', e.relay.tls && e.relay.tls !== 'starttls' ? `, tls = ${hcl(e.relay.tls)}` : ''].join('');
        lines.push(`  relay = { host = ${hcl(e.relay.host)}, credentials_secret = ${hcl(e.relay.credentialsSecret)}${extra} }`);
      }
      if (e.domain) lines.push(`  domain = ${hcl(e.domain)}`);
      if (e.keepMessages && e.keepMessages !== RULES.keepMessagesDefault) lines.push(`  keep_messages = ${e.keepMessages}`);
      placement(e);
      break;
    }
    case 'network': {
      const n = r.spec as NetworkSpec;
      lines.push(`  cidr = ${hcl(n.cidr)}`);
      const subnets = n.subnets ?? [];
      if (!(subnets.length === 1 && subnets[0]?.name === RULES.defaultSubnetName))
        lines.push(`  subnet = [\n${subnets.map((x) => `    { name = ${hcl(x.name)}, cidr = ${hcl(x.cidr)} },`).join('\n')}\n  ]`);
      if (n.internet === false) lines.push('  internet = false');
      break;
    }
    case 'firewall': {
      const f = r.spec as FirewallSpec;
      lines.push(`  targets = [${f.targets.map(hcl).join(', ')}]`);
      if (f.ingress?.length) lines.push(`  ingress = [\n${f.ingress.map((x) => ruleBlock(x, 'from')).join('\n')}\n  ]`);
      if (f.egressDefault === 'deny') {
        lines.push('  egress_default = "deny"');
        if (f.egress?.length) lines.push(`  egress = [\n${f.egress.map((x) => ruleBlock(x, 'to')).join('\n')}\n  ]`);
      }
      break;
    }
    case 'loadbalancer': {
      type = 'load_balancer';
      const l = r.spec as LoadBalancerSpec;
      lines.push(`  port = ${l.port}`);
      if (l.protocol === 'tcp') lines.push('  protocol = "tcp"');
      if (l.algorithm && l.algorithm !== 'round_robin') lines.push(`  algorithm = ${hcl(l.algorithm)}`);
      if (l.publish) lines.push('  publish = true');
      const targets = l.targets.map((t) => {
        const fields = [`app = ${hcl(t.app)}`];
        if (t.port) fields.push(`port = ${t.port}`);
        if (t.weight && t.weight !== RULES.lbWeightDefault) fields.push(`weight = ${t.weight}`);
        return `    { ${fields.join(', ')} },`;
      });
      lines.push(`  target = [\n${targets.join('\n')}\n  ]`);
      if (l.healthCheck?.type === 'http') lines.push(`  health_check = { type = "http", path = ${hcl(l.healthCheck.path ?? '/')} }`);
      placement(l);
      break;
    }
    case 'zone': {
      const z = r.spec as ZoneSpec;
      lines.push(`  dns_provider = ${hcl(z.provider)}`, `  zone = ${hcl(z.zone)}`, `  credentials_secret = ${hcl(z.credentialsSecret)}`);
      break;
    }
    case 'record': {
      const d = r.spec as RecordSpec;
      lines.push(`  zone = ${hcl(d.zone)}`, `  record_name = ${hcl(d.name)}`, `  type = ${hcl(d.type)}`);
      if (d.machine) lines.push('  machine = true');
      if (d.values?.length) lines.push(`  values = [${d.values.map(hcl).join(', ')}]`);
      if (d.ttl && d.ttl !== RULES.recordTtlDefault) lines.push(`  ttl = ${d.ttl}`);
      break;
    }
    case 'function': {
      const f = r.spec as FunctionSpec;
      lines.push(`  image = ${hcl(f.image)}`);
      if (f.command?.length) lines.push(`  command = [${f.command.map(hcl).join(', ')}]`);
      const env = hclMap(f.env);
      if (env) lines.push(`  env = ${env}`);
      const senv = hclMap(f.secretEnv);
      if (senv) lines.push(`  secret_env = ${senv}`);
      if (f.memoryMiB && f.memoryMiB !== RULES.functionMemoryDefault) lines.push(`  memory_mib = ${f.memoryMiB}`);
      if (f.timeoutSeconds && f.timeoutSeconds !== RULES.functionTimeoutDefault) lines.push(`  timeout_seconds = ${f.timeoutSeconds}`);
      if (f.concurrency && f.concurrency !== RULES.functionConcurrencyDefault) lines.push(`  concurrency = ${f.concurrency}`);
      if (f.triggers?.length) {
        const trig = (t: NonNullable<FunctionSpec['triggers']>[number]) => [
          ...(t.schedule ? [`schedule = ${hcl(t.schedule)}`] : []), ...(t.timeZone ? [`time_zone = ${hcl(t.timeZone)}`] : []),
          ...(t.queue ? [`queue = ${hcl(t.queue)}`] : []), ...(t.bucket ? [`bucket = ${hcl(t.bucket)}`] : []), ...(t.prefix ? [`prefix = ${hcl(t.prefix)}`] : []),
        ].join(', ');
        lines.push(`  triggers = [\n${f.triggers.map((t) => `    { ${trig(t)} },`).join('\n')}\n  ]`);
      }
      placement(f);
      const fg = grantsHcl(f.bucketAccess);
      if (fg) lines.push(fg);
      break;
    }
    case 'job': {
      const j = r.spec as JobSpec;
      lines.push(`  schedule = ${hcl(j.schedule)}`);
      if (j.timeZone) lines.push(`  time_zone = ${hcl(j.timeZone)}`);
      lines.push(j.app ? `  app = ${hcl(j.app)}` : `  image = ${hcl(j.image ?? '')}`);
      if (j.command?.length) lines.push(`  command = [${j.command.map(hcl).join(', ')}]`);
      const env = hclMap(j.env);
      if (env) lines.push(`  env = ${env}`);
      const senv = hclMap(j.secretEnv);
      if (senv) lines.push(`  secret_env = ${senv}`);
      if (j.timeoutSeconds && j.timeoutSeconds !== RULES.jobTimeoutDefault) lines.push(`  timeout_seconds = ${j.timeoutSeconds}`);
      if (j.overlap === 'allow') lines.push('  overlap = "allow"');
      if (j.memoryMiB && j.memoryMiB !== RULES.appMemoryDefault) lines.push(`  memory_mib = ${j.memoryMiB}`);
      placement(j);
      const jm = mountsHcl(j.volumes);
      if (jm) lines.push(jm);
      const jg = grantsHcl(j.bucketAccess);
      if (jg) lines.push(jg);
      break;
    }
    case 'backup': {
      const b = r.spec as BackupSpec;
      lines.push(b.source.database ? `  source = { database = ${hcl(b.source.database)} }` : b.source.volume ? `  source = { volume = ${hcl(b.source.volume)} }` : `  source = { cache = ${hcl(b.source.cache ?? '')} }`);
      if (b.destination.s3) {
        const s3 = b.destination.s3;
        const fields = [`    endpoint = ${hcl(s3.endpoint)}`];
        if (s3.region) fields.push(`    region = ${hcl(s3.region)}`);
        fields.push(`    bucket = ${hcl(s3.bucket)}`, `    credentials_secret = ${hcl(s3.credentialsSecret)}`);
        lines.push(`  s3 = {\n${fields.join('\n')}\n  }`);
      } else lines.push(`  bucket = ${hcl(b.destination.bucket ?? '')}`);
      if (b.schedule && b.schedule !== RULES.backupScheduleDefault) lines.push(`  schedule = ${hcl(b.schedule)}`);
      if (b.timeZone) lines.push(`  time_zone = ${hcl(b.timeZone)}`);
      if (b.prefix && b.prefix !== RULES.backupPrefixDefault) lines.push(`  prefix = ${hcl(b.prefix)}`);
      if (b.keep && b.keep !== RULES.backupKeepDefault) lines.push(`  keep = ${b.keep}`);
      if (b.keepDays) lines.push(`  keep_days = ${b.keepDays}`);
      if (b.timeoutSeconds && b.timeoutSeconds !== RULES.backupTimeoutDefault) lines.push(`  timeout_seconds = ${b.timeoutSeconds}`);
      break;
    }
    case 'domain': {
      const d = r.spec as DomainSpec;
      lines.push(`  hostname = ${hcl(d.hostname)}`, `  app = ${hcl(d.app)}`);
      if (d.port) lines.push(`  port = ${d.port}`);
      if (d.tls && d.tls !== (d.hostname.endsWith(RULES.localhostSuffix) ? 'internal' : 'auto')) lines.push(`  tls = ${hcl(d.tls)}`);
      if (d.redirectHttp === false) lines.push('  redirect_http = false');
      if (d.zone) lines.push(`  zone = ${hcl(d.zone)}`);
      if (d.tunnel) lines.push(`  tunnel = ${hcl(d.tunnel)}`);
      const gate = gateHcl(d.gate);
      if (gate) lines.push(`  gate = ${gate}`);
      const a = d.cloudflareAccess;
      if (a) lines.push(`  cloudflare_access = { ${[a.emails?.length ? `emails = ${refs(a.emails)}` : '', a.emailDomains?.length ? `email_domains = ${refs(a.emailDomains)}` : ''].filter(Boolean).join(', ')} }`);
      break;
    }
    case 'tunnel': {
      // "provider" is reserved by OpenTofu: the attribute is tunnel_provider (TF-51).
      const t = r.spec as TunnelSpec;
      lines.push(`  tunnel_provider = ${hcl(t.provider)}`);
      const fields: [string, string | number | undefined][] = [['credentials_secret', t.credentialsSecret], ['account_id', t.accountId], ['machine', t.machine],
        ['app', t.app], ['port', t.port], ['mode', t.mode && t.mode !== 'private' ? t.mode : undefined], ['hostname', t.hostname], ['domain', t.domain],
        ['image', t.image], ['connector_url', t.url]];
      for (const [k, v] of fields) if (v !== undefined && v !== '') lines.push(`  ${k} = ${typeof v === 'number' ? v : hcl(v)}`);
      if (t.command?.length) lines.push(`  command = ${refs(t.command)}`);
      const env = hclMap(t.env);
      if (env) lines.push(`  env = ${env}`);
      const senv = hclMap(t.secretEnv);
      if (senv) lines.push(`  secret_env = ${senv}`);
      const gate = gateHcl(t.gate);
      if (gate) lines.push(`  gate = ${gate}`);
      break;
    }
    case 'cache':
      lines.push(`  max_memory_mib = ${(r.spec as CacheSpec).maxMemoryMiB ?? 256}`);
      placement(r.spec as CacheSpec);
      break;
    case 'bucket':
      lines.push(`  versioning = ${(r.spec as BucketSpec).versioning ? 'true' : 'false'}`);
      break;
    case 'secret': {
      const src = (r.spec as { source?: { store: string; key: string; field?: string } }).source;
      if (src) lines.push(`  source = { store = ${hcl(src.store)}, key = ${hcl(src.key)}${src.field ? `, field = ${hcl(src.field)}` : ''} }`);
      else lines.push('  value = var.' + ident(r.name) + ' # never stored in the console');
      break;
    }
    case 'dashboard': {
      const d = r.spec as DashboardSpec;
      lines.push(`  widgets = [\n${d.widgets.map((w) => `    { title = ${hcl(w.title)}, metric = ${hcl(w.metric)}, resources = ${refs(w.resources)} },`).join('\n')}\n  ]`);
      break;
    }
    case 'alert': {
      const a = r.spec as AlertSpec;
      const c = a.condition;
      const fields = [`type = ${hcl(c.type)}`, `resources = ${refs(c.resources)}`];
      if (c.type === 'metric') {
        fields.push(`metric = ${hcl(c.metric ?? '')}`, `operator = ${hcl(c.operator ?? 'above')}`, `threshold = ${c.threshold ?? 0}`);
        if (c.forMinutes && c.forMinutes !== RULES.alertForDefault) fields.push(`for_minutes = ${c.forMinutes}`);
      }
      if (c.type === 'machine_offline' && c.forMinutes && c.forMinutes !== 1) fields.push(`for_minutes = ${c.forMinutes}`);
      if (c.type === 'event') fields.push(`events = ${refs(c.events ?? [])}`);
      if (c.type === 'crash_loop') {
        if (c.restarts && c.restarts !== RULES.alertRestartsDefault) fields.push(`restarts = ${c.restarts}`);
        if (c.windowMinutes && c.windowMinutes !== RULES.alertWindowDefault) fields.push(`window_minutes = ${c.windowMinutes}`);
      }
      lines.push(`  condition = { ${fields.join(', ')} }`, `  channels = ${refs(a.channels)}`);
      if (a.repeatMinutes) lines.push(`  repeat_minutes = ${a.repeatMinutes}`);
      if (a.enabled === false) lines.push('  enabled = false');
      break;
    }
    case 'channel': {
      const c = r.spec as ChannelSpec;
      lines.push(`  type = ${hcl(c.type)}`);
      if (c.email) lines.push(`  email = { service = ${hcl(c.email.service)}, from = ${hcl(c.email.from)}, to = ${refs(c.email.to)} }`);
      if (c.webhook) lines.push(`  webhook = { url = ${hcl(c.webhook.url)}${c.webhook.secret ? `, secret = ${hcl(c.webhook.secret)}` : ''} }`);
      if (c.secret) lines.push(`  secret = ${hcl(c.secret)}`);
      if (c.chatId) lines.push(`  chat_id = ${hcl(c.chatId)}`);
      break;
    }
    case 'logexport': {
      type = 'log_export';
      const l = r.spec as LogExportSpec;
      const d = l.destination;
      if (l.sources?.length) lines.push(`  sources = ${refs(l.sources)}`);
      if (l.filter) lines.push(`  filter = ${hcl(l.filter)}`);
      const def = d.bucket || d.s3 ? RULES.logFlushBucketDefault : RULES.logFlushDefault;
      if (l.flushSeconds && l.flushSeconds !== def) lines.push(`  flush_seconds = ${l.flushSeconds}`);
      if (d.bucket) lines.push(`  bucket = { bucket = ${hcl(d.bucket.bucket)}${d.bucket.prefix ? `, prefix = ${hcl(d.bucket.prefix)}` : ''} }`);
      if (d.s3) {
        const fields = [`endpoint = ${hcl(d.s3.endpoint)}`, `bucket = ${hcl(d.s3.bucket)}`, `credentials_secret = ${hcl(d.s3.credentialsSecret)}`];
        if (d.s3.region) fields.push(`region = ${hcl(d.s3.region)}`);
        if (d.s3.prefix) fields.push(`prefix = ${hcl(d.s3.prefix)}`);
        lines.push(`  s3 = { ${fields.join(', ')} }`);
      }
      for (const [key, h] of [['http', d.http], ['otlp', d.otlp], ['loki', d.loki]] as const) {
        if (!h) continue;
        const fields = [`url = ${hcl((key === 'otlp' ? h.endpoint : h.url) ?? '')}`];
        if (h.headersSecret) fields.push(`headers_secret = ${hcl(h.headersSecret)}`);
        const labels = Object.entries(h.labels ?? {});
        if (labels.length) fields.push(`labels = { ${labels.map(([k, v]) => `${k} = ${hcl(v)}`).join(', ')} }`);
        lines.push(`  ${key} = { ${fields.join(', ')} }`);
      }
      break;
    }
    case 'secretstore': {
      type = 'secret_store';
      const st = r.spec as SecretStoreSpec;
      lines.push(`  secret_provider = ${hcl(st.provider)}`);
      const block = (st[st.provider] ?? {}) as Record<string, string | undefined>;
      const defaults: Record<string, string> = { mount: RULES.vaultMountDefault, siteUrl: RULES.infisicalSiteDefault, path: '/' };
      const settings = Object.entries(block).filter(([k, v]) => v && v !== defaults[k]);
      lines.push(`  settings = {\n${settings.map(([k, v]) => `    ${k} = ${hcl(v ?? '')}`).join('\n')}\n  }`);
      if (st.refreshSeconds && st.refreshSeconds !== RULES.storeRefreshDefault) lines.push(`  refresh_seconds = ${st.refreshSeconds}`);
      break;
    }
    case 'app': {
      type = 'app';
      const s = r.spec as AppSpec;
      if (s.source) {
        const g = s.source;
        const fields = [`    git = ${hcl(g.git)}`, `    ref = ${hcl(g.ref)}`];
        if (g.dockerfile && g.dockerfile !== RULES.gitDockerfileDefault) fields.push(`    dockerfile = ${hcl(g.dockerfile)}`);
        if (g.context && g.context !== RULES.gitContextDefault) fields.push(`    context = ${hcl(g.context)}`);
        if (g.credentialsSecret) fields.push(`    credentials_secret = ${hcl(g.credentialsSecret)}`);
        const args = Object.entries(g.buildArgs ?? {});
        if (args.length) fields.push(`    build_args = { ${args.map(([k, v]) => `${k} = ${hcl(v)}`).join(', ')} }`);
        if (g.webhookSecret) fields.push(`    webhook_secret = ${hcl(g.webhookSecret)}`);
        if (g.deployOnPush) fields.push('    deploy_on_push = true');
        lines.push(`  source = {\n${fields.join('\n')}\n  }`);
      } else lines.push(`  image = ${hcl(s.image ?? '')}`);
      if (s.previews) {
        const pv = s.previews;
        const fields = [`    enabled = ${pv.enabled}`];
        if (pv.ttlHours && pv.ttlHours !== RULES.previewTtlDefault) fields.push(`    ttl_hours = ${pv.ttlHours}`);
        if (pv.maxActive && pv.maxActive !== RULES.previewMaxActiveDefault) fields.push(`    max_active = ${pv.maxActive}`);
        if (pv.domain) {
          const d = pv.domain;
          const df = [`suffix = ${hcl(d.suffix)}`, ...(d.zone ? [`zone = ${hcl(d.zone)}`] : []), ...(d.tunnel ? [`tunnel = ${hcl(d.tunnel)}`] : []),
            ...(d.tls && d.tls !== (d.zone || d.tunnel ? 'auto' : 'internal') ? [`tls = ${hcl(d.tls)}`] : [])];
          fields.push(`    domain = { ${df.join(', ')} }`);
        }
        lines.push(`  previews = {\n${fields.join('\n')}\n  }`);
      }
      if (s.port) lines.push(`  port = ${s.port}`);
      if (s.publish) lines.push('  publish = true');
      if (s.memoryMiB && s.memoryMiB !== 256) lines.push(`  memory_mib = ${s.memoryMiB}`);
      if (s.command?.length) lines.push(`  command = [${s.command.map(hcl).join(', ')}]`);
      const env = hclMap(s.env);
      if (env) lines.push(`  env = ${env}`);
      const senv = hclMap(s.secretEnv);
      if (senv) lines.push(`  secret_env = ${senv}`);
      if (s.strategy === 'recreate') lines.push('  strategy = "recreate"');
      if (s.replicas && s.replicas > 1) lines.push(`  replicas = ${s.replicas}`);
      placement(s);
      const am = mountsHcl(s.volumes);
      if (am) lines.push(am);
      const ag = grantsHcl(s.bucketAccess);
      if (ag) lines.push(ag);
      const h = s.healthCheck;
      if (h) {
        const fields = [`    type = ${hcl(h.type)}`];
        if (h.type === 'http' && h.path && h.path !== '/') fields.push(`    path = ${hcl(h.path)}`);
        if (h.port) fields.push(`    port = ${h.port}`);
        if (h.command?.length) fields.push(`    command = [${h.command.map(hcl).join(', ')}]`);
        if (h.type !== 'none') {
          if (h.intervalSeconds && h.intervalSeconds !== RULES.healthIntervalDefault) fields.push(`    interval_seconds = ${h.intervalSeconds}`);
          if (h.retries && h.retries !== RULES.healthRetriesDefault) fields.push(`    retries = ${h.retries}`);
          if (h.startPeriodSeconds) fields.push(`    start_period_seconds = ${h.startPeriodSeconds}`);
        }
        lines.push(`  health_check = {\n${fields.join('\n')}\n  }`);
      }
      break;
    }
  }
  return `resource "enclave_${type}" ${hcl(ident(r.name))} {\n${lines.join('\n')}\n}`;
}
