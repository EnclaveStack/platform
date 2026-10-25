import { Check, ChevronDown, X } from 'lucide-react';
import { useEffect, useId, useRef, useState, type KeyboardEvent, type ReactNode } from 'react';
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

export interface MenuOption {
  value: string;
  label: string;
}

/**
 * A styled single choice for the sidebar (the project menu): a button that opens a list under it. Arrow
 * keys, Home, End, Enter and Escape work, and a click outside closes it (WAI-ARIA listbox popup).
 */
export function MenuSelect({ label, value, options, onChange, icon }: {
  label: string; value: string; options: MenuOption[]; onChange: (value: string) => void; icon?: ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);
  const root = useRef<HTMLDivElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  const list = useRef<HTMLUListElement>(null);
  const listId = useId();
  const selected = Math.max(options.findIndex((o) => o.value === value), 0);
  const current = options[selected];

  useEffect(() => {
    if (!open) return;
    const away = (e: MouseEvent) => { if (!root.current?.contains(e.target as Node)) setOpen(false); };
    document.addEventListener('mousedown', away);
    return () => document.removeEventListener('mousedown', away);
  }, [open]);
  useEffect(() => { if (open) list.current?.focus(); }, [open]);

  const show = () => { setActive(selected); setOpen(true); };
  const close = (refocus: boolean) => { setOpen(false); if (refocus) button.current?.focus(); };
  const choose = (i: number) => {
    const o = options[i];
    close(true);
    if (o && o.value !== value) onChange(o.value);
  };
  function onKey(e: KeyboardEvent<HTMLUListElement>) {
    const last = options.length - 1;
    const move: Record<string, number> = { ArrowDown: Math.min(active + 1, last), ArrowUp: Math.max(active - 1, 0), Home: 0, End: last };
    if (e.key in move) {
      e.preventDefault();
      setActive(move[e.key] ?? 0);
    } else if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      choose(active);
    } else if (e.key === 'Escape') {
      e.preventDefault();
      close(true);
    } else if (e.key === 'Tab') {
      close(false);
    }
  }
  return (
    <div className="es-menu-select" ref={root}>
      <button ref={button} type="button" className="es-menu-select-button" aria-haspopup="listbox" aria-expanded={open} aria-controls={open ? listId : undefined}
        aria-label={`${label}: ${current?.label ?? ''}`} title={current?.label} onClick={() => (open ? close(false) : show())}>
        {icon && <span className="es-menu-select-icon" aria-hidden="true">{icon}</span>}
        <span className="es-menu-select-text">{current?.label}</span>
        <ChevronDown className="es-menu-select-chevron" aria-hidden="true" />
      </button>
      {open && (
        <ul ref={list} id={listId} className="es-menu-select-list" role="listbox" aria-label={label} tabIndex={-1}
          aria-activedescendant={`${listId}-${active}`} onKeyDown={onKey}>
          {options.map((o, i) => (
            <li key={o.value} id={`${listId}-${i}`} role="option" aria-selected={o.value === value} data-active={i === active}
              className="es-menu-select-option" onMouseEnter={() => setActive(i)} onMouseDown={(e) => e.preventDefault()} onClick={() => choose(i)}>
              <span className="es-menu-select-option-text">{o.label}</span>
              {o.value === value && <Check aria-hidden="true" />}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
