import { forwardRef, type ButtonHTMLAttributes, type HTMLAttributes, type ReactNode } from 'react';
import { cx } from './primitives';

export type ButtonVariant = 'primary' | 'dark' | 'outline' | 'soft' | 'ghost' | 'danger' | 'danger-outline';
export type ButtonSize = 'sm' | 'md' | 'lg';

interface ButtonStyle {
  variant?: ButtonVariant;
  size?: ButtonSize;
  block?: boolean;
}

/** Button classes, for links that must look like buttons (router links). */
export function buttonClass({ variant = 'outline', size = 'md', block }: ButtonStyle = {}): string {
  return cx('es-btn', `es-btn-${variant}`, size !== 'md' && `es-btn-${size}`, block && 'es-btn-block');
}

interface ButtonProps extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'className' | 'style'>, ButtonStyle {
  /** Icon shown before the label. */
  icon?: ReactNode;
}

/** Pill button. Dark is the one primary action per view; outline is everything else (L2.5-001 §5). */
export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button({ variant, size, block, icon, type = 'button', children, ...rest }, ref) {
  return (
    <button ref={ref} type={type} className={buttonClass({ variant, size, block })} {...rest}>
      {icon}
      {children}
    </button>
  );
});

interface IconButtonProps extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'className' | 'style' | 'children'> {
  /** Required: an icon-only control needs a name. */
  label: string;
  icon: ReactNode;
  variant?: 'outline' | 'ghost' | 'dark';
  size?: 'sm' | 'md';
  extraClass?: 'menu';
}

/** Round icon-only button. */
export const IconButton = forwardRef<HTMLButtonElement, IconButtonProps>(function IconButton({ label, icon, variant = 'outline', size = 'md', extraClass, type = 'button', ...rest }, ref) {
  return (
    <button ref={ref} type={type} aria-label={label} title={label}
      className={cx('es-icon-btn', variant !== 'outline' && `es-icon-btn-${variant}`, size === 'sm' && 'es-icon-btn-sm', extraClass === 'menu' && 'es-menu-btn')} {...rest}>
      {icon}
    </button>
  );
});

export type ChipTone = 'neutral' | 'accent' | 'ok' | 'warn' | 'bad' | 'dark';

interface ChipProps extends Omit<HTMLAttributes<HTMLSpanElement>, 'className' | 'style'> {
  tone?: ChipTone;
  mono?: boolean;
  icon?: ReactNode;
}

/** Small pill label: a tag, a kind, a key fact. */
export function Chip({ tone = 'neutral', mono, icon, children, ...rest }: ChipProps) {
  return (
    <span className={cx('es-chip', tone !== 'neutral' && `es-chip-${tone}`, mono && 'es-chip-mono')} {...rest}>
      {icon}
      {children}
    </span>
  );
}

interface BadgeProps extends Omit<HTMLAttributes<HTMLSpanElement>, 'className' | 'style'> {
  tone?: 'dark' | 'soft';
  mono?: boolean;
}

/** Round count or short value (the dark circle in list cards). */
export function Badge({ tone = 'dark', mono, children, ...rest }: BadgeProps) {
  return <span className={cx('es-badge', tone === 'soft' && 'es-badge-soft', mono && 'es-badge-mono')} {...rest}>{children}</span>;
}

const AVATAR_TONES = 6;

/** Stable 1–6 tone for a name, so a resource keeps its colour everywhere. */
export function avatarTone(name: string): number {
  let h = 0;
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) >>> 0;
  return (h % AVATAR_TONES) + 1;
}

/** Up to two initials from a name or email: "orders-db" → "OD", "ada@x.dev" → "AD". */
export function initialsOf(text: string): string {
  const base = text.split('@')[0] ?? '';
  const parts = base.split(/[\s._-]+/).filter(Boolean);
  const letters = parts.length > 1 ? (parts[0]?.[0] ?? '') + (parts[1]?.[0] ?? '') : base.slice(0, 2);
  return letters.toUpperCase() || '?';
}

interface AvatarProps extends Omit<HTMLAttributes<HTMLSpanElement>, 'className' | 'style' | 'children'> {
  name: string;
  size?: 'sm' | 'md' | 'lg' | 'xl';
  /** Show this instead of initials (an icon). */
  children?: ReactNode;
}

/** Pastel circle with initials, tinted from the name. Decorative unless given a label. */
export function Avatar({ name, size = 'md', children, ...rest }: AvatarProps) {
  const labelled = 'aria-label' in rest;
  return (
    <span className={cx('es-avatar', `es-avatar-${size}`, `es-av-${avatarTone(name)}`)} aria-hidden={labelled ? undefined : true} {...rest}>
      {children ?? initialsOf(name)}
    </span>
  );
}

export type StatusTone = 'ok' | 'busy' | 'bad';

/** Status is always a word beside the dot, never colour alone (L2.5-001 §3). */
export function Status({ tone, children }: { tone: StatusTone; children: ReactNode }) {
  return (
    <span className={cx('es-status', `es-status-${tone}`)}>
      <span className="es-dot" aria-hidden="true" />
      {children}
    </span>
  );
}

/** A dot on its own must carry a label for assistive technology. */
export function Dot({ tone, label }: { tone: StatusTone; label: string }) {
  return <span className={cx('es-dot', `es-status-${tone}`)} role="img" aria-label={label} />;
}

interface ProgressProps {
  label: string;
  /** 0–100. */
  value: number;
}

/** Thin progress bar (native <progress>, so it is announced as a progress bar). */
export function ProgressBar({ label, value }: ProgressProps) {
  const v = Math.round(Math.min(100, Math.max(0, value)));
  return <progress className="es-progress" aria-label={label} max={100} value={v}>{v}%</progress>;
}

/** Loading placeholder shapes. */
export function Skeleton({ shape = 'line' }: { shape?: 'line' | 'short' | 'title' | 'block' | 'card' }) {
  return <span className={cx('es-skeleton', `es-skeleton-${shape}`)} aria-hidden="true" />;
}

interface AlertProps extends Omit<HTMLAttributes<HTMLDivElement>, 'className' | 'style'> {
  tone?: 'bad' | 'info' | 'warn';
}

/** Inline message block. Errors get role="alert" by default. */
export function Alert({ tone = 'bad', role, children, ...rest }: AlertProps) {
  return (
    <div className={cx('es-alert', tone !== 'bad' && `es-alert-${tone}`)} role={role ?? (tone === 'bad' ? 'alert' : undefined)} {...rest}>
      {children}
    </div>
  );
}
