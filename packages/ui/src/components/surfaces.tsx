import { useId, type HTMLAttributes, type ReactNode } from 'react';
import { cx } from './primitives';

interface PanelProps extends Omit<HTMLAttributes<HTMLElement>, 'className' | 'style'> {
  as?: 'section' | 'div' | 'aside';
  wash?: boolean;
  /** Layout role of the panel. */
  layout?: 'list' | 'detail' | 'page';
}

/** The large rounded surface the list column and detail view sit on. */
export function Panel({ as: As = 'section', wash, layout, children, ...rest }: PanelProps) {
  return (
    <As className={cx('es-panel', wash && 'es-panel-wash', layout === 'list' && 'es-list-column', layout === 'detail' && 'es-detail')} {...rest}>
      {children}
    </As>
  );
}

interface CardProps extends Omit<HTMLAttributes<HTMLElement>, 'className' | 'style' | 'title'> {
  as?: 'div' | 'section' | 'article' | 'li';
  padding?: 'normal' | 'tight' | 'flush';
  tone?: 'plain' | 'accent';
  /** Optional heading row. */
  title?: ReactNode;
  action?: ReactNode;
}

/** Rounded white card for a group of facts or a form. A titled card is named by its title, so a section card is a landmark. */
export function Card({ as: As = 'div', padding = 'normal', tone = 'plain', title, action, children, ...rest }: CardProps) {
  const headingId = useId();
  return (
    <As className={cx('es-card', padding !== 'normal' && `es-card-${padding}`, tone === 'accent' && 'es-card-accent')} aria-labelledby={title ? headingId : undefined} {...rest}>
      {(title || action) && (
        <div className="es-card-head">
          {title && <h2 className="es-card-title" id={headingId}>{title}</h2>}
          {action}
        </div>
      )}
      {children}
    </As>
  );
}

/** One labelled value: "Host — 127.0.0.1". */
export function Fact({ label, value, mono, action }: { label: string; value: ReactNode; mono?: boolean; action?: ReactNode }) {
  return (
    <div className="es-fact">
      <span className="es-fact-label">{label}</span>
      <div className="es-fact-row">
        <span className={cx('es-fact-value', mono && 'es-mono')}>{value}</span>
        {action}
      </div>
    </div>
  );
}

/** Centered empty or placeholder state, optionally with an icon and action. */
export function EmptyState({ icon, title, children, action, full, role }: { icon?: ReactNode; title?: ReactNode; children?: ReactNode; action?: ReactNode; full?: boolean; role?: 'status' }) {
  return (
    <div className={cx('es-empty', full && 'es-empty-full')} role={role}>
      {icon && <span className="es-empty-icon" aria-hidden="true">{icon}</span>}
      {title && <p className="es-text es-size-lg es-weight-bold es-tone-ink">{title}</p>}
      {children}
      {action}
    </div>
  );
}

/** Divided list of rows (environment, events). */
export function Rows({ children, ...rest }: Omit<HTMLAttributes<HTMLDivElement>, 'className' | 'style'>) {
  return <div className="es-rows" {...rest}>{children}</div>;
}

export function Row({ children, ...rest }: Omit<HTMLAttributes<HTMLDivElement>, 'className' | 'style'>) {
  return <div className="es-row" {...rest}>{children}</div>;
}

/** Disclosure with a styled summary. */
export function Disclosure({ summary, children }: { summary: ReactNode; children: ReactNode }) {
  return (
    <details className="es-details">
      <summary>{summary}</summary>
      {children}
    </details>
  );
}
