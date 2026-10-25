import { forwardRef, type FormHTMLAttributes, type InputHTMLAttributes, type ReactNode, type SelectHTMLAttributes, type TextareaHTMLAttributes } from 'react';
import { cx } from './primitives';
import { buttonClass } from './controls';

/** A labelled control with its hint, or its error in place of the hint (L2.5-002 §1). */
export function Field({ id, label, hint, error, children }: { id: string; label: ReactNode; hint?: ReactNode; error?: string; children: ReactNode }) {
  return (
    <div className="es-field">
      <label className="es-field-label" htmlFor={id}>{label}</label>
      {children}
      {error ? (
        <p className="es-field-error" id={`${id}-error`}>{error}</p>
      ) : (
        hint && <p className="es-field-hint" id={`${id}-hint`}>{hint}</p>
      )}
    </div>
  );
}

/** The aria-describedby value matching what Field renders under the control. */
export function describedBy(id: string, error?: string, hint?: unknown): string | undefined {
  if (error) return `${id}-error`;
  return hint ? `${id}-hint` : undefined;
}

export function FieldHint({ children }: { children: ReactNode }) {
  return <p className="es-field-hint">{children}</p>;
}

export function FieldError({ children }: { children: ReactNode }) {
  return <p className="es-field-error">{children}</p>;
}

interface ControlStyle {
  mono?: boolean;
  sunk?: boolean;
}

type InputProps = Omit<InputHTMLAttributes<HTMLInputElement>, 'className' | 'style'> & ControlStyle;

export const Input = forwardRef<HTMLInputElement, InputProps>(function Input({ mono, sunk, ...rest }, ref) {
  return <input ref={ref} className={cx('es-input', mono && 'es-input-mono', sunk && 'es-input-sunk')} {...rest} />;
});

type SelectProps = Omit<SelectHTMLAttributes<HTMLSelectElement>, 'className' | 'style'> & ControlStyle;

export function Select({ mono, sunk, children, ...rest }: SelectProps) {
  return <select className={cx('es-input', mono && 'es-input-mono', sunk && 'es-input-sunk')} {...rest}>{children}</select>;
}

type TextareaProps = Omit<TextareaHTMLAttributes<HTMLTextAreaElement>, 'className' | 'style'> & ControlStyle;

export function Textarea({ mono, sunk, ...rest }: TextareaProps) {
  return <textarea className={cx('es-input', mono && 'es-input-mono', sunk && 'es-input-sunk')} {...rest} />;
}

/** Checkbox with its label to the right; the whole row is clickable. */
export function Checkbox({ label, ...rest }: Omit<InputHTMLAttributes<HTMLInputElement>, 'className' | 'style' | 'type'> & { label: ReactNode }) {
  return (
    <label className="es-check">
      <input type="checkbox" {...rest} />
      {label}
    </label>
  );
}

/**
 * One option of a radio group, as a card: a title and a line of explanation, the whole card clickable.
 * Group them in a Fieldset and give each the same `name`.
 */
export function Choice({ title, description, ...rest }: Omit<InputHTMLAttributes<HTMLInputElement>, 'className' | 'style' | 'type' | 'title'> & { title: ReactNode; description?: ReactNode }) {
  return (
    <label className="es-choice">
      <input type="radio" {...rest} />
      <span className="es-choice-text">
        <span className="es-choice-title">{title}</span>
        {description && <span className="es-choice-desc">{description}</span>}
      </span>
    </label>
  );
}

/** Vertical form with the standard field spacing. */
export function Form({ children, ...rest }: Omit<FormHTMLAttributes<HTMLFormElement>, 'className' | 'style'>) {
  return <form className="es-form" {...rest}>{children}</form>;
}

/** Fields side by side, wrapping on narrow screens. */
export function FormRow({ children }: { children: ReactNode }) {
  return <div className="es-form-row">{children}</div>;
}

export function Fieldset({ legend, children }: { legend: ReactNode; children: ReactNode }) {
  return (
    <fieldset className="es-fieldset">
      <legend>{legend}</legend>
      {children}
    </fieldset>
  );
}

/** Name · value · remove, with an optional error under the row. */
export function KeyValueRow({ children }: { children: ReactNode }) {
  return <div className="es-kv-row">{children}</div>;
}

/**
 * Choose files from the computer: a button-styled label over a hidden native input (keyboard and screen
 * readers use the input), and the chosen files listed with their sizes.
 */
export function FilePicker({ id, label, multiple, files, onChange, sizeText }: {
  id: string; label: string; multiple?: boolean; files: File[]; onChange: (files: File[]) => void; sizeText: (bytes: number) => string;
}) {
  return (
    <div className="es-file-picker">
      <input id={id} type="file" className="es-sr-only es-file-input" multiple={multiple} aria-label={label}
        onChange={(e) => onChange(Array.from(e.target.files ?? []))} />
      <label htmlFor={id} className={buttonClass({ size: 'sm' })}>{label}</label>
      {files.length > 0 && (
        <ul className="es-file-list" aria-label="Chosen files">
          {files.map((f) => (
            <li key={f.name}><span className="es-mono">{f.name}</span><span className="es-text es-tone-muted es-size-sm">{sizeText(f.size)}</span></li>
          ))}
        </ul>
      )}
    </div>
  );
}
