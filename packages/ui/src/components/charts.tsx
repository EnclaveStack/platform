import { useId, useState, type PointerEvent } from 'react';
import { cx } from './primitives';

// Line charts for metrics (L2.5-002 §14, ADR-037): 2 px lines in the series tokens, a recessive grid,
// one y axis, a crosshair with a tooltip on hover, and a legend that names each series with its latest
// value in text — so identity never rests on colour alone. A hidden table carries the same numbers.

export const SERIES_MAX = 8;

export interface ChartPoint {
  t: number; // unix seconds
  v: number;
}

export interface ChartSeries {
  label: string;
  points: ChartPoint[];
}

interface LineChartProps {
  /** What the chart shows; read by screen readers and used as the table caption. */
  title: string;
  series: ChartSeries[];
  /** The time range drawn, unix seconds. */
  from: number;
  to: number;
  /** Formats a value for the axis, legend and tooltip. */
  format: (v: number) => string;
  /** Shown instead of a plot when no series has a point. */
  empty?: string;
}

const W = 600;
const H = 200;
const PAD = { top: 12, right: 12, bottom: 24, left: 56 };

function niceMax(v: number): number {
  if (v <= 0) return 1;
  const p = 10 ** Math.floor(Math.log10(v));
  for (const m of [1, 2, 2.5, 5, 10]) if (v <= m * p) return m * p;
  return 10 * p;
}

function timeLabel(t: number, span: number): string {
  const d = new Date(t * 1000);
  if (span > 2 * 86400) return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
  return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
}

/** A time-series line chart; at most eight series (SERIES_MAX), coloured by their order. */
export function LineChart({ title, series, from, to, format, empty = 'No data yet.' }: LineChartProps) {
  const id = useId();
  const [hover, setHover] = useState<number | null>(null);
  const shown = series.slice(0, SERIES_MAX);
  const all = shown.flatMap((s) => s.points);
  const top = niceMax(Math.max(0, ...all.map((p) => p.v)));
  const span = Math.max(1, to - from);
  const x = (t: number) => PAD.left + ((t - from) / span) * (W - PAD.left - PAD.right);
  const y = (v: number) => PAD.top + (1 - v / top) * (H - PAD.top - PAD.bottom);
  const times = [...new Set(all.map((p) => p.t))].sort((a, b) => a - b);

  if (all.length === 0) {
    return (
      <div className="es-chart es-chart-empty" role="img" aria-label={`${title}: ${empty}`}>
        <span className="es-text es-size-sm es-tone-subtle">{empty}</span>
      </div>
    );
  }

  const onMove = (e: PointerEvent<SVGSVGElement>) => {
    const box = e.currentTarget.getBoundingClientRect();
    const px = ((e.clientX - box.left) / box.width) * W;
    let best: number | null = null;
    for (const t of times) if (best === null || Math.abs(x(t) - px) < Math.abs(x(best) - px)) best = t;
    setHover(best);
  };
  const at = (s: ChartSeries, t: number) => s.points.find((p) => p.t === t);
  const tip = hover === null ? null : shown.flatMap((s, i) => {
    const p = at(s, hover);
    return p ? [{ i, label: s.label, p }] : [];
  });
  const tipX = hover === null ? 0 : x(hover) > W / 2 ? x(hover) - 168 : x(hover) + 8;

  return (
    <figure className="es-chart" aria-labelledby={`${id}-t`}>
      <svg viewBox={`0 0 ${W} ${H}`} className="es-chart-svg" role="img" aria-labelledby={`${id}-t`} onPointerMove={onMove} onPointerLeave={() => setHover(null)}>
        {[0, 0.5, 1].map((f) => (
          <g key={f}>
            <line className="es-chart-grid" x1={PAD.left} x2={W - PAD.right} y1={y(top * f)} y2={y(top * f)} />
            <text className="es-chart-axis" x={PAD.left - 6} y={y(top * f) + 4} textAnchor="end">{format(top * f)}</text>
          </g>
        ))}
        {[from, from + span / 2, to].map((t) => (
          <text key={t} className="es-chart-axis" x={x(t)} y={H - 6} textAnchor={t === from ? 'start' : t === to ? 'end' : 'middle'}>{timeLabel(t, span)}</text>
        ))}
        {shown.map((s, i) => (
          <polyline key={s.label} className={cx('es-chart-line', `es-series-${i + 1}`)} fill="none"
            points={s.points.map((p) => `${x(p.t).toFixed(1)},${y(p.v).toFixed(1)}`).join(' ')} />
        ))}
        {hover !== null && tip && (
          <g aria-hidden="true">
            <line className="es-chart-cross" x1={x(hover)} x2={x(hover)} y1={PAD.top} y2={H - PAD.bottom} />
            {tip.map(({ i, p }) => <circle key={i} className={cx('es-chart-dot', `es-series-${i + 1}`)} cx={x(p.t)} cy={y(p.v)} r={4} />)}
            <rect className="es-chart-tip" x={tipX} y={PAD.top} width={160} height={20 + tip.length * 16} rx={8} />
            <text className="es-chart-tip-title" x={tipX + 10} y={PAD.top + 15}>{timeLabel(hover, 0)}</text>
            {tip.map(({ i, label, p }, row) => (
              <g key={i}>
                <rect className={cx('es-chart-swatch', `es-series-${i + 1}`)} x={tipX + 10} y={PAD.top + 22 + row * 16} width={8} height={8} rx={2} />
                <text className="es-chart-tip-text" x={tipX + 24} y={PAD.top + 30 + row * 16}>{`${label.slice(0, 14)}  ${format(p.v)}`}</text>
              </g>
            ))}
          </g>
        )}
      </svg>
      <figcaption>
        <span id={`${id}-t`} className="es-sr-only">{title}</span>
        <ul className="es-chart-legend">
          {shown.map((s, i) => {
            const last = s.points[s.points.length - 1];
            return (
              <li key={s.label}>
                <span className={cx('es-chart-key', `es-series-${i + 1}`)} aria-hidden="true" />
                <span className="es-text es-size-xs es-tone-muted">{s.label}</span>
                <span className="es-text es-size-xs es-weight-semibold es-tone-ink es-mono">{last ? format(last.v) : '—'}</span>
              </li>
            );
          })}
          {series.length > SERIES_MAX && <li className="es-text es-size-xs es-tone-subtle">{series.length - SERIES_MAX} more not drawn</li>}
        </ul>
      </figcaption>
      <table className="es-sr-only">
        <caption>{title}</caption>
        <thead><tr><th scope="col">Time</th>{shown.map((s) => <th key={s.label} scope="col">{s.label}</th>)}</tr></thead>
        <tbody>
          {times.map((t) => (
            <tr key={t}><th scope="row">{new Date(t * 1000).toISOString()}</th>{shown.map((s) => {
              const p = at(s, t);
              return <td key={s.label}>{p ? format(p.v) : ''}</td>;
            })}</tr>
          ))}
        </tbody>
      </table>
    </figure>
  );
}
