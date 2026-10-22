import { Check, X } from 'lucide-react';
import type { KeyboardEvent, ReactNode } from 'react';
import { cx, VisuallyHidden } from './primitives';

export interface TabItem<T extends string> {
  id: T;
  label: string;
  count?: number;
}

interface TabsProps<T extends string> {
  label: string;
  tabs: TabItem<T>[];
  selected: T;
  onSelect: (id: T) => void;
  /** Prefix for tab and panel ids, so several tab sets can share a page. */
  idPrefix?: string;
}

/** Segmented pill tabs; the selected tab is the dark pill. Arrow keys move between tabs (WAI-ARIA tabs). */
export function SegmentedTabs<T extends string>({ label, tabs, selected, onSelect, idPrefix = 'tab' }: TabsProps<T>) {
  function onKey(e: KeyboardEvent<HTMLDivElement>) {
    if (e.key !== 'ArrowRight' && e.key !== 'ArrowLeft') return;
    const i = tabs.findIndex((t) => t.id === selected);
    const next = tabs[(i + (e.key === 'ArrowRight' ? 1 : tabs.length - 1)) % tabs.length];
    if (!next) return;
    e.preventDefault();
    onSelect(next.id);
    e.currentTarget.querySelector<HTMLElement>(`#${idPrefix}-${next.id}`)?.focus();
  }
  return (
    <div className="es-tabs" role="tablist" aria-label={label} onKeyDown={onKey}>
      {tabs.map((t) => (
        <button key={t.id} type="button" role="tab" className="es-tab" id={`${idPrefix}-${t.id}`} aria-controls={`${idPrefix}-panel-${t.id}`}
          aria-selected={t.id === selected} tabIndex={t.id === selected ? 0 : -1} onClick={() => onSelect(t.id)}>
          {t.label}
          {t.count !== undefined && <span className="es-tab-count">{t.count}</span>}
        </button>
      ))}
    </div>
  );
}

/** The panel a SegmentedTabs tab controls. */
export function TabPanel({ idPrefix = 'tab', id, children }: { idPrefix?: string; id: string; children: ReactNode }) {
  return <div role="tabpanel" id={`${idPrefix}-panel-${id}`} aria-labelledby={`${idPrefix}-${id}`}>{children}</div>;
}

export type StageState = 'pending' | 'active' | 'done' | 'failed';

export interface Stage {
  label: string;
  state: StageState;
}

const STEP_CLASS = ['es-steps-1', 'es-steps-1', 'es-steps-2', 'es-steps-3', '', 'es-steps-5', 'es-steps-6'];

/** Numbered stage pill: provisioning steps with done / active / failed states. */
export function StageTrack({ label, stages }: { label: string; stages: Stage[] }) {
  const cols = STEP_CLASS[Math.min(stages.length, 6)] ?? 'es-steps-6';
  return (
    <ol className={cx('es-stages', cols)} aria-label={label}>
      {stages.map((s, i) => (
        <li key={s.label} className={cx('es-stage', s.state !== 'pending' && `es-stage-${s.state}`)} aria-current={s.state === 'active' ? 'step' : undefined}>
          <span className="es-stage-num" aria-hidden="true">
            {s.state === 'done' ? <Check /> : s.state === 'failed' ? <X /> : i + 1}
          </span>
          <span className="es-stage-label">{s.label}</span>
          <VisuallyHidden> — {s.state}</VisuallyHidden>
        </li>
      ))}
    </ol>
  );
}
