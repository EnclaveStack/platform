import { Search, X } from 'lucide-react';
import { createContext, useCallback, useContext, useEffect, useId, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type ReactNode } from 'react';
import { Button, IconButton } from './controls';
import { Field, Input } from './forms';

function useEscape(onEscape: () => void) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onEscape();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onEscape]);
}

/** Side sheet over the page for create and edit forms. Escape, the scrim or the close button dismiss it. */
export function Drawer({ title, onClose, children }: { title: string; onClose: () => void; children: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  useEscape(onClose);
  useEffect(() => {
    ref.current?.querySelector<HTMLElement>('input:not([readonly]), select, textarea, .es-drawer-body button')?.focus();
  }, []);
  return (
    <div className="es-overlay">
      <button type="button" className="es-scrim" aria-label="Close" tabIndex={-1} onClick={onClose} />
      <div className="es-drawer" role="dialog" aria-modal="true" aria-label={title} ref={ref}>
        <div className="es-drawer-head">
          <h2 className="es-text es-size-xl es-weight-bold">{title}</h2>
          <IconButton label="Close" icon={<X />} onClick={onClose} />
        </div>
        <div className="es-drawer-body">{children}</div>
      </div>
    </div>
  );
}

interface ConfirmProps {
  title: string;
  body: ReactNode;
  confirmLabel: string;
  /** When set, the confirm button stays disabled until this exact text is typed (L2.5-002 §3). */
  typeToConfirm?: string;
  danger?: boolean;
  busy?: boolean;
  error?: ReactNode;
  onConfirm: () => void;
  onCancel: () => void;
}

/** Modal confirmation for destructive or sensitive actions. */
export function ConfirmDialog({ title, body, confirmLabel, typeToConfirm, danger, busy, error, onConfirm, onCancel }: ConfirmProps) {
  const [typed, setTyped] = useState('');
  const ref = useRef<HTMLDivElement>(null);
  useEscape(onCancel);
  useEffect(() => {
    ref.current?.querySelector<HTMLElement>('input, button')?.focus();
  }, []);
  const ready = !typeToConfirm || typed === typeToConfirm;
  return (
    <div className="es-overlay">
      <button type="button" className="es-scrim" aria-label="Cancel" tabIndex={-1} onClick={onCancel} />
      <div className="es-dialog" role="alertdialog" aria-modal="true" aria-labelledby="es-confirm-title" ref={ref}>
        <h2 id="es-confirm-title" className="es-text es-size-xl es-weight-bold">{title}</h2>
        <div className="es-text es-tone-muted">{body}</div>
        {typeToConfirm && (
          <Field id="es-confirm-name" label={<>Type <strong className="es-mono">{typeToConfirm}</strong> to confirm</>}>
            <Input id="es-confirm-name" mono value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" spellCheck={false} />
          </Field>
        )}
        {error && <div className="es-alert" role="alert">{error}</div>}
        <div className="es-dialog-actions">
          <Button onClick={onCancel}>Cancel</Button>
          <Button variant={danger ? 'danger' : 'dark'} disabled={!ready || busy} onClick={onConfirm}>
            {busy ? 'Working…' : confirmLabel}
          </Button>
        </div>
      </div>
    </div>
  );
}

interface Toast {
  id: number;
  message: string;
  action?: { label: string; run: () => void };
}

type Show = (message: string, action?: Toast['action']) => void;

const ToastContext = createContext<Show | null>(null);
const LIFETIME_MS = 6000;
const MAX_VISIBLE = 3;

/** Short confirmations at the bottom of the screen, announced politely. */
export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const next = useRef(1);
  const dismiss = useCallback((id: number) => setToasts((all) => all.filter((t) => t.id !== id)), []);
  const show = useCallback<Show>((message, action) => {
    const id = next.current++;
    setToasts((all) => [...all.slice(-(MAX_VISIBLE - 1)), { id, message, action }]);
    setTimeout(() => dismiss(id), LIFETIME_MS);
  }, [dismiss]);
  const value = useMemo(() => show, [show]);
  return (
    <ToastContext.Provider value={value}>
      {children}
      <div className="es-toasts" role="status" aria-live="polite">
        {toasts.map((t) => (
          <div key={t.id} className="es-toast">
            <span>{t.message}</span>
            {t.action && (
              <Button size="sm" variant="soft" onClick={() => {
                dismiss(t.id);
                t.action?.run();
              }}>
                {t.action.label}
              </Button>
            )}
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}

export function useToast(): Show {
  const show = useContext(ToastContext);
  if (!show) throw new Error('useToast() needs a <ToastProvider> above it');
  return show;
}

export interface FinderItem {
  id: string;
  label: string;
  /** What it is, on the right: "Page", "Guide", "app · ready". */
  meta?: string;
  icon?: ReactNode;
}

/**
 * Find (CON-62): a search field over pages and resources. Arrow keys move, Enter opens, Escape closes
 * (the WAI-ARIA combobox pattern). The app filters `items` from `query`.
 */
export function Finder({ query, onQuery, items, onChoose, onClose }: {
  query: string; onQuery: (q: string) => void; items: FinderItem[]; onChoose: (item: FinderItem) => void; onClose: () => void;
}) {
  const [active, setActive] = useState(0);
  const listId = useId();
  useEscape(onClose);
  const current = Math.min(active, Math.max(items.length - 1, 0));
  function onKey(e: ReactKeyboardEvent<HTMLInputElement>) {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      if (items.length) setActive((current + (e.key === 'ArrowDown' ? 1 : items.length - 1)) % items.length);
    } else if (e.key === 'Enter') {
      const item = items[current];
      if (item) {
        e.preventDefault();
        onChoose(item);
      }
    }
  }
  return (
    <div className="es-overlay">
      <button type="button" className="es-scrim" aria-label="Close" tabIndex={-1} onClick={onClose} />
      <div className="es-finder" role="dialog" aria-modal="true" aria-label="Find">
        <div className="es-finder-field">
          <Input autoFocus type="search" role="combobox" aria-label="Find a page or a resource" aria-expanded={items.length > 0} aria-controls={listId}
            aria-activedescendant={items[current] ? `${listId}-${current}` : undefined} placeholder="Find a page or a resource…" value={query}
            onChange={(e) => { setActive(0); onQuery(e.target.value); }} onKeyDown={onKey} autoComplete="off" spellCheck={false} />
        </div>
        {items.length ? (
          <ul className="es-finder-results" role="listbox" id={listId} aria-label="Results">
            {items.map((item, i) => (
              <li key={item.id} id={`${listId}-${i}`} role="option" aria-selected={i === current} className="es-finder-option"
                aria-label={item.meta ? `${item.label}, ${item.meta}` : item.label}
                onMouseEnter={() => setActive(i)} onMouseDown={(e) => { e.preventDefault(); onChoose(item); }}>
                {item.icon}
                <span>{item.label}</span>
                {item.meta && <span className="es-finder-option-meta">{item.meta}</span>}
              </li>
            ))}
          </ul>
        ) : (
          <p className="es-finder-empty" id={listId}>{query ? `Nothing matches “${query}”.` : 'Type to find a page or a resource.'}</p>
        )}
      </div>
    </div>
  );
}

/** The top bar's Find button: shows the shortcut. */
export function FinderButton({ onOpen, shortcut }: { onOpen: () => void; shortcut: string }) {
  return (
    <button type="button" className="es-finder-hint" onClick={onOpen} aria-keyshortcuts={shortcut === '⌘K' ? 'Meta+K' : 'Control+K'}>
      <Search aria-hidden="true" />
      <span>Find…</span>
      <kbd>{shortcut}</kbd>
    </button>
  );
}
