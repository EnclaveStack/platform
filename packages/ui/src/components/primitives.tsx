import type { ElementType, HTMLAttributes, ReactNode } from 'react';

/** Joins class names, dropping falsy parts. */
export function cx(...parts: (string | false | null | undefined)[]): string {
  return parts.filter(Boolean).join(' ');
}

export type Space = 0 | 1 | 2 | 3 | 4 | 5 | 6 | 8;
export type TextSize = '2xs' | 'xs' | 'sm' | 'base' | 'md' | 'lg' | 'xl' | '2xl' | '3xl';
export type Weight = 'regular' | 'medium' | 'semibold' | 'bold' | 'heavy';
export type Tone = 'ink' | 'muted' | 'subtle' | 'faint' | 'accent' | 'ok' | 'warn' | 'bad';

interface TextProps extends Omit<HTMLAttributes<HTMLElement>, 'className' | 'style'> {
  as?: ElementType;
  size?: TextSize;
  weight?: Weight;
  tone?: Tone;
  mono?: boolean;
  truncate?: boolean;
  /** Break long unspaced values (hosts, tokens) anywhere. */
  breakAnywhere?: boolean;
  children?: ReactNode;
}

/** Every run of text in the console: size, weight and tone come from the type scale only. */
export function Text({ as: As = 'span', size, weight, tone, mono, truncate, breakAnywhere, children, ...rest }: TextProps) {
  return (
    <As
      className={cx('es-text', size && `es-size-${size}`, weight && `es-weight-${weight}`, tone && `es-tone-${tone}`, mono && 'es-mono', truncate && 'es-truncate', breakAnywhere && 'es-break')}
      {...rest}
    >
      {children}
    </As>
  );
}

interface HeadingProps extends Omit<TextProps, 'as'> {
  level: 1 | 2 | 3;
}

const HEADING_SIZE: Record<HeadingProps['level'], TextSize> = { 1: '2xl', 2: 'xl', 3: 'lg' };

export function Heading({ level, size, weight = 'bold', ...rest }: HeadingProps) {
  return <Text as={`h${level}`} size={size ?? HEADING_SIZE[level]} weight={weight} {...rest} />;
}

interface FlexProps extends Omit<HTMLAttributes<HTMLElement>, 'className' | 'style'> {
  as?: ElementType;
  gap?: Space;
  align?: 'start' | 'center' | 'end' | 'baseline' | 'stretch';
  justify?: 'start' | 'between' | 'center' | 'end';
  wrap?: boolean;
  grow?: boolean;
  children?: ReactNode;
}

function flexClass(base: string, { gap, align, justify, wrap, grow }: FlexProps): string {
  return cx(base, gap !== undefined && `es-gap-${gap}`, align && `es-align-${align}`, justify && `es-justify-${justify}`, wrap && 'es-wrap', grow && 'es-grow');
}

/** Vertical flow with a spacing-scale gap. */
export function Stack({ as: As = 'div', gap = 3, align, justify, wrap, grow, children, ...rest }: FlexProps) {
  return <As className={flexClass('es-stack', { gap, align, justify, wrap, grow })} {...rest}>{children}</As>;
}

/** Horizontal flow with a spacing-scale gap. */
export function Inline({ as: As = 'div', gap = 2, align, justify, wrap, grow, children, ...rest }: FlexProps) {
  return <As className={flexClass('es-inline', { gap, align, justify, wrap, grow })} {...rest}>{children}</As>;
}

interface GridProps extends Omit<FlexProps, 'align' | 'justify' | 'wrap'> {
  /** Smallest column width before the grid wraps. */
  min?: 'sm' | 'md' | 'lg';
}

/** Auto-fitting card grid. */
export function Grid({ as: As = 'div', gap = 3, min = 'md', grow, children, ...rest }: GridProps) {
  return <As className={cx('es-grid', `es-grid-min-${min}`, `es-gap-${gap}`, grow && 'es-grow')} {...rest}>{children}</As>;
}

/** Text for assistive technology only. */
export function VisuallyHidden({ children, ...rest }: Omit<HTMLAttributes<HTMLSpanElement>, 'className' | 'style'>) {
  return <span className="es-sr-only" {...rest}>{children}</span>;
}
