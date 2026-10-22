import { MACHINE_METRICS, METRICS, RULES } from './generated';
import type { AlertSpec, ChannelSpec, Kind, LogExportSpec, MetricData, MetricSeries, Resource, SecretStoreSpec } from './types';
import { normalizePrefix } from '../lib/validation';

// The mock adapter's v0.7 pieces (ADR-002): defaults the server fills in, outputs, and made-up metrics
// that follow the same shapes and catalogue as L3-002 §10.4.

export function normalizeOperate(kind: Kind, spec: Record<string, unknown>): Record<string, unknown> | undefined {
  switch (kind) {
    case 'dashboard':
      return spec;
    case 'alert': {
      const a = spec as unknown as AlertSpec;
      const c = { ...a.condition };
      if (c.type === 'metric') c.forMinutes ??= RULES.alertForDefault;
      if (c.type === 'crash_loop') {
        c.restarts ??= RULES.alertRestartsDefault;
        c.windowMinutes ??= RULES.alertWindowDefault;
      }
      return { ...spec, condition: c };
    }
    case 'channel':
      return spec as unknown as ChannelSpec as unknown as Record<string, unknown>;
    case 'logexport': {
      const l = spec as unknown as LogExportSpec;
      const d = { ...l.destination };
      if (d.bucket) d.bucket = { ...d.bucket, prefix: normalizePrefix(d.bucket.prefix) };
      if (d.s3) d.s3 = { ...d.s3, prefix: normalizePrefix(d.s3.prefix) };
      return { ...spec, flushSeconds: l.flushSeconds ?? (d.bucket || d.s3 ? RULES.logFlushBucketDefault : RULES.logFlushDefault), destination: d };
    }
    case 'secretstore': {
      const st = { ...(spec as unknown as SecretStoreSpec) };
      st.refreshSeconds ??= RULES.storeRefreshDefault;
      if (st.vault) st.vault = { ...st.vault, mount: st.vault.mount || RULES.vaultMountDefault };
      if (st.infisical) st.infisical = { ...st.infisical, siteUrl: st.infisical.siteUrl || RULES.infisicalSiteDefault, path: st.infisical.path || '/' };
      return st as unknown as Record<string, unknown>;
    }
  }
  return undefined;
}

export function operateOutputs(r: Resource, at: string): Resource['outputs'] | undefined {
  switch (r.kind) {
    case 'dashboard':
      return { widgets: ((r.spec as { widgets?: unknown[] }).widgets ?? []).length };
    case 'alert':
      return { state: (r.spec as AlertSpec).enabled === false ? 'disabled' : String(r.outputs.state ?? 'ok'), lastEvaluatedAt: at, ...(r.outputs.since ? { since: r.outputs.since } : {}) };
    case 'channel':
      return { lastDeliveryStatus: String(r.outputs.lastDeliveryStatus ?? ''), ...(r.outputs.lastDeliveryAt ? { lastDeliveryAt: r.outputs.lastDeliveryAt } : {}) };
    case 'logexport':
      return { linesSent: Number(r.outputs.linesSent ?? 0), bytesSent: Number(r.outputs.bytesSent ?? 0), lagSeconds: 0, lastError: '', ...(r.outputs.lastSentAt ? { lastSentAt: r.outputs.lastSentAt } : {}) };
    case 'secretstore':
      return { checkedAt: at, syncedSecrets: Number(r.outputs.syncedSecrets ?? 0) };
  }
  return undefined;
}

/** A small stable number per string, so a resource's made-up graphs keep their shape between loads. */
function hash(s: string): number {
  let h = 2166136261;
  for (const c of s) h = Math.imul(h ^ c.charCodeAt(0), 16777619);
  return (h >>> 0) / 4294967295;
}

const BASE: Record<string, number> = {
  cpu: 0.4, memory: 180 * 2 ** 20, memoryLimit: 256 * 2 ** 20, netRx: 40_000, netTx: 25_000, diskRead: 8_000, diskWrite: 12_000, restarts: 0, requests: 12,
  errors5xx: 0.05, latency: 45, queueDepth: 20, inFlight: 2, oldestAge: 30, delivered: 3, deliveryFailures: 0, usedBytes: 3 * 2 ** 30, percentFull: 32,
  cachedBytes: 64 * 2 ** 20, diskFree: 180 * 2 ** 30, diskPercentFull: 61, resourcesFailed: 0,
};

/** The step the server would choose: 30 s or more, at most 500 points (ADR-037). */
export function stepFor(from: number, to: number): number {
  const raw = Math.max(30, Math.ceil((to - from) / 500));
  for (const s of [30, 60, 300, 900, 3600]) if (raw <= s) return s;
  return 3600;
}

export function mockMetrics(subject: string, kind: Kind | 'machine', names: string[], from: number, to: number, since: number): MetricData {
  const step = stepFor(from, to);
  const catalogue = kind === 'machine' ? MACHINE_METRICS : METRICS.filter((m) => (m.kinds as readonly string[]).includes(kind));
  const wanted = names.length ? catalogue.filter((m) => names.includes(m.name)) : catalogue;
  const items: MetricSeries[] = wanted.map((m) => {
    const seed = hash(`${subject}/${m.name}`);
    const base = (BASE[m.name] ?? 1) * (0.6 + seed);
    const points = [];
    for (let t = Math.ceil(Math.max(from, since) / step) * step; t <= to; t += step) {
      const wave = Math.sin(t / 1800 + seed * 6) * 0.25 + Math.sin(t / 240 + seed * 3) * 0.08;
      const v = m.name === 'restarts' || m.name === 'resourcesFailed' ? 0 : m.name === 'memoryLimit' ? base : Math.max(0, base * (1 + wave));
      points.push({ t, v: m.unit === 'count' ? Math.round(v) : Number(v.toPrecision(4)) });
    }
    return { metric: m.name, unit: m.unit, points };
  });
  return { from: new Date(from * 1000).toISOString(), to: new Date(to * 1000).toISOString(), stepSeconds: step, items };
}
