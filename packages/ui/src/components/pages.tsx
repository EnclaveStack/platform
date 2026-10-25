import { useEffect, useId, useRef, useState, type ReactNode } from 'react';
import { cx } from './primitives';

// Building blocks of the overview, the section summaries and the guides (L2.5-001 §7, L2.5-002 §16).

const COUNT_MS = 600;

/** Motion is on unless the system asks for less, or there is no way to ask (tests, server rendering). */
function canAnimate(): boolean {
  return typeof window !== 'undefined' && typeof window.matchMedia === 'function' && typeof window.requestAnimationFrame === 'function'
    && !window.matchMedia('(prefers-reduced-motion: reduce)').matches;
}

/**
 * Eases a number from where it was to `target` over 600 ms, a few lines of requestAnimationFrame and no
 * library. Without motion it is `target` at once.
 */
export function useCountUp(target: number): number {
  const motion = canAnimate();
  const [shown, setShown] = useState(motion ? 0 : target);
  const from = useRef(motion ? 0 : target);
  useEffect(() => {
    if (!motion) return;
    const start = performance.now();
    const begin = from.current;
    let frame = 0;
    const step = (now: number) => {
      const t = Math.min(1, (now - start) / COUNT_MS);
      const v = Math.round(begin + (target - begin) * (1 - (1 - t) ** 3));
      from.current = v;
      setShown(v);
      if (t < 1) frame = window.requestAnimationFrame(step);
    };
    frame = window.requestAnimationFrame(step);
    return () => window.cancelAnimationFrame(frame);
  }, [target, motion]);
  return motion ? shown : target;
}

function CountUp({ value }: { value: number }) {
  return <>{useCountUp(value)}</>;
}

/**
 * One headline number: a label, the value and a note. A number counts up; anything else shows as given.
 * `tone="bad"` marks a number that needs attention; it never stands alone, the label says what it is.
 */
export function StatTile({ label, value, note, icon, tone = 'plain' }: { label: string; value: number | ReactNode; note?: ReactNode; icon?: ReactNode; tone?: 'plain' | 'bad' | 'ok' }) {
  return (
    <div className={cx('es-stat', tone !== 'plain' && `es-stat-${tone}`)} role="group" aria-label={label}>
      <span className="es-stat-label">
        {icon && <span className="es-stat-icon" aria-hidden="true">{icon}</span>}
        {label}
      </span>
      <span className="es-stat-value">{typeof value === 'number' ? <CountUp value={value} /> : value}</span>
      {note && <span className="es-stat-note">{note}</span>}
    </div>
  );
}

/** A row of stat tiles that wraps on narrow screens. */
export function StatRow({ children, label }: { children: ReactNode; label: string }) {
  return <div className="es-stat-row" role="group" aria-label={label}>{children}</div>;
}

/** Two columns: a wide main one and a narrower side one, or two even ones; one column on narrow screens. */
export function Columns({ ratio = 'main-side', children }: { ratio?: 'main-side' | 'even'; children: ReactNode }) {
  return <div className={cx('es-columns', `es-columns-${ratio}`)}>{children}</div>;
}

export type GuideTone = 'what' | 'when' | 'how' | 'example' | 'note';

/**
 * One part of a guide (CON-67): a coloured bar and tint per kind of part. The heading always names the
 * part, so the colour is never the only cue (L2.5-001 §3).
 */
export function GuidePart({ tone, title, children }: { tone: GuideTone; title: string; children: ReactNode }) {
  const id = useId();
  return (
    <section className={cx('es-guide-part', `es-guide-${tone}`)} aria-labelledby={id}>
      <h2 id={id} className="es-guide-title">{title}</h2>
      <div className="es-guide-body">{children}</div>
    </section>
  );
}

/** A numbered list of steps inside a guide. */
export function Steps({ children }: { children: ReactNode }) {
  return <ol className="es-guide-steps">{children}</ol>;
}

/** A bulleted list inside a guide. */
export function Bullets({ children }: { children: ReactNode }) {
  return <ul className="es-guide-bullets">{children}</ul>;
}
