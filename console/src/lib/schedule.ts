import { RULES } from '../api/generated';

// Cron schedules for people (L2.5-002 §10): parse the same 5-field syntax and @-descriptors the server
// accepts (pkg/api ParseSchedule), describe them in words, and list upcoming runs in the browser's
// time zone. The server stays authoritative; this is for display, previews and the mock adapter.

const DESCRIPTOR: Record<string, string> = { '@hourly': '0 * * * *', '@daily': '0 0 * * *', '@weekly': '0 0 * * 0', '@monthly': '0 0 1 * *' };
const BOUNDS: [number, number][] = [[0, 59], [0, 23], [1, 31], [1, 12], [0, 6]];
const DAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];

export interface Cron {
  fields: Set<number>[];
  /** Day-of-month or day-of-week was restricted: cron ORs them when both are. */
  domStar: boolean;
  dowStar: boolean;
  source: string;
}

function parseField(f: string, [lo, hi]: [number, number], i: number): Set<number> {
  const out = new Set<number>();
  for (const part of f.split(',')) {
    const [range = '', stepText] = part.split('/');
    const step = stepText === undefined ? 1 : Number(stepText);
    if (!Number.isInteger(step) || step < 1) throw new Error(`bad step in "${part}"`);
    let a: number;
    let b: number;
    if (range === '*') [a, b] = [lo, hi];
    else if (range.includes('-')) [a, b] = range.split('-').map(Number) as [number, number];
    else {
      a = Number(range);
      b = stepText === undefined ? a : hi;
    }
    if (i === 4) {
      if (a === 7) a = 0;
      if (b === 7) b = 6;
    }
    if (!Number.isInteger(a) || !Number.isInteger(b) || a < lo || b > hi || a > b) throw new Error(`"${part}" is out of range ${lo}–${hi}`);
    for (let v = a; v <= b; v += step) out.add(v);
  }
  return out;
}

/** Parses a schedule; throws an Error with a short reason when it is invalid. */
export function parseCron(expr: string): Cron {
  const src = expr.trim();
  if (src.startsWith('@')) {
    if (!(RULES.scheduleDescriptors as readonly string[]).includes(src)) throw new Error(`use one of ${RULES.scheduleDescriptors.join(', ')}`);
    return parseCron(DESCRIPTOR[src] ?? '');
  }
  const parts = src.split(/\s+/);
  if (parts.length !== 5) throw new Error('use 5 fields: minute hour day month weekday');
  return { fields: parts.map((p, i) => parseField(p, BOUNDS[i] ?? [0, 0], i)), domStar: parts[2] === '*', dowStar: parts[4] === '*', source: src };
}

function matches(c: Cron, d: Date): boolean {
  const [min, hour, dom, mon, dow] = c.fields as [Set<number>, Set<number>, Set<number>, Set<number>, Set<number>];
  if (!min.has(d.getMinutes()) || !hour.has(d.getHours()) || !mon.has(d.getMonth() + 1)) return false;
  const domOk = dom.has(d.getDate());
  const dowOk = dow.has(d.getDay());
  return c.domStar || c.dowStar ? domOk && dowOk : domOk || dowOk;
}

/** The next `count` runs after `from`, in the browser's time zone (minute resolution, at most a year ahead). */
export function nextRuns(expr: string, from: Date, count = 3): Date[] {
  const c = parseCron(expr);
  const out: Date[] = [];
  const d = new Date(from);
  d.setSeconds(0, 0);
  for (let i = 0; i < 527_040 && out.length < count; i++) {
    d.setMinutes(d.getMinutes() + 1);
    if (matches(c, d)) out.push(new Date(d));
  }
  return out;
}

const pad = (n: number) => String(n).padStart(2, '0');
const one = (s: Set<number>) => (s.size === 1 ? [...s][0] : undefined);

/** "Every day at 03:00", "Every hour at :15", "Every 5 minutes" — or the expression itself. */
export function describeSchedule(expr: string, timeZone?: string): string {
  let c: Cron;
  try {
    c = parseCron(expr);
  } catch {
    return expr;
  }
  const [min, hour, dom, mon, dow] = c.fields as [Set<number>, Set<number>, Set<number>, Set<number>, Set<number>];
  const p = c.source.split(/\s+/);
  const m = one(min);
  const h = one(hour);
  const zone = timeZone ? `, ${timeZone}` : '';
  const at = m !== undefined && h !== undefined ? ` at ${pad(h)}:${pad(m)}` : '';
  let words: string | undefined;
  if (p.every((f) => f === '*')) words = 'Every minute';
  else if (/^\*\/\d+$/.test(p[0] ?? '') && p.slice(1).every((f) => f === '*')) words = `Every ${p[0]?.slice(2)} minutes`;
  else if (m !== undefined && p[1] === '*' && p.slice(2).every((f) => f === '*')) words = `Every hour at :${pad(m)}`;
  else if (at && p.slice(2).every((f) => f === '*')) words = `Every day${at}`;
  else if (at && p[2] === '*' && p[3] === '*' && dow.size === 1) words = `Every ${DAYS[one(dow) ?? 0]}${at}`;
  else if (at && p[2] === '*' && p[3] === '*' && p[4] === '1-5') words = `Every weekday${at}`;
  else if (at && one(dom) !== undefined && p[3] === '*' && p[4] === '*') words = `On day ${one(dom)} of every month${at}`;
  else if (at && one(dom) !== undefined && one(mon) !== undefined && p[4] === '*') words = `Every year on ${one(dom)}/${one(mon)}${at}`;
  return words ? words + zone : `Cron ${c.source}${zone}`;
}
