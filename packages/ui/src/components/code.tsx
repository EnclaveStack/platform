import { Check, Copy } from 'lucide-react';
import { Fragment, useEffect, useState, type ReactNode } from 'react';
import { Button } from './controls';
import { cx } from './primitives';

/** Copies text; the state reads "copied" or "failed" (the clipboard was blocked) for a moment. */
function useCopy(): [state: 'idle' | 'copied' | 'failed', copy: (text: string) => Promise<void>] {
  const [state, setState] = useState<'idle' | 'copied' | 'failed'>('idle');
  useEffect(() => {
    if (state === 'idle') return;
    const t = setTimeout(() => setState('idle'), 1800);
    return () => clearTimeout(t);
  }, [state]);
  async function copy(text: string) {
    try {
      await navigator.clipboard.writeText(text);
      setState('copied');
    } catch {
      setState('failed');
    }
  }
  return [state, copy];
}

/** Labelled code with a copy button; when the clipboard is blocked it asks the reader to select the text. */
export function CodeBlock({ label, code }: { label: string; code: string }) {
  const [state, copy] = useCopy();
  return (
    <div className="es-code-block">
      <div className="es-code-head">
        <span>{label}</span>
        <Button size="sm" variant="ghost" icon={state === 'copied' ? <Check /> : <Copy />} onClick={() => void copy(code)} aria-label={`Copy ${label}`}>
          {state === 'copied' ? 'Copied' : state === 'failed' ? 'Select it' : 'Copy'}
        </Button>
      </div>
      <pre className="es-code" aria-label={label}>{code}</pre>
    </div>
  );
}

/** One line of a DetailList (L2.5-002 §20). */
export interface DetailItem {
  label: string;
  value: ReactNode;
  /** What it is and what to do with it, shown under the label. */
  hint?: string;
  mono?: boolean;
  /** The raw text a Copy button copies. */
  copy?: string;
}

/** Breaks a long address only after / : . - so it never splits mid-word (DET-01). */
function breakable(text: string): ReactNode {
  const parts = text.split(/(?<=[/:.-])/);
  return parts.length < 2 ? text : parts.map((p, i) => <Fragment key={i}>{p}{i < parts.length - 1 && <wbr />}</Fragment>);
}

function DetailCopy({ label, text }: { label: string; text: string }) {
  const [state, copy] = useCopy();
  return (
    <Button size="sm" variant="ghost" icon={state === 'copied' ? <Check /> : <Copy />} onClick={() => void copy(text)} aria-label={`Copy ${label}`}>
      {state === 'copied' ? 'Copied' : state === 'failed' ? 'Select it' : undefined}
    </Button>
  );
}

/**
 * A resource's details as label and value pairs (DET-01 – DET-03): labels at their natural width with
 * their explanation under them, values on the same line, wrapping only at sensible places, each with an
 * optional copy button. On a narrow screen the label sits above the value.
 */
export function DetailList({ items, label = 'Details' }: { items: DetailItem[]; label?: string }) {
  return (
    <dl className="es-dl" aria-label={label}>
      {items.map((it) => (
        <div key={it.label} className="es-dl-row">
          <dt>
            <span className="es-dl-label">{it.label}</span>
            {it.hint && <span className="es-dl-hint">{it.hint}</span>}
          </dt>
          <dd>
            <span className={cx('es-dl-value', it.mono && 'es-mono')}>{typeof it.value === 'string' && it.mono ? breakable(it.value) : it.value}</span>
            {it.copy && <DetailCopy label={it.label} text={it.copy} />}
          </dd>
        </div>
      ))}
    </dl>
  );
}

/** Plain code value (a revealed secret). */
export function Code({ label, children }: { label: string; children: ReactNode }) {
  return <pre className={cx('es-code', 'es-code-block')} aria-label={label}>{children}</pre>;
}

/**
 * Untrusted HTML (an email's body) in a frame that runs nothing and loads nothing: an empty sandbox (no
 * scripts, forms, popups or navigation) and a content policy that blocks every remote fetch.
 */
export function HtmlPreview({ title, html }: { title: string; html: string }) {
  const doc = `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src data:; style-src 'unsafe-inline'">${html}`;
  return <iframe className="es-html-preview" title={title} sandbox="" srcDoc={doc} referrerPolicy="no-referrer" />;
}

export type TerminalLevel = 'dim' | 'info' | 'warn' | 'error' | 'plain';

/** Dark log surface. */
export function Terminal({ label, children }: { label: string; children: ReactNode }) {
  return <div className="es-terminal" role="log" aria-label={label}>{children}</div>;
}

export function TerminalText({ level = 'plain', children }: { level?: TerminalLevel; children: ReactNode }) {
  return <span className={level === 'plain' ? undefined : `es-terminal-${level}`}>{children}</span>;
}

export function TerminalLine({ children }: { children: ReactNode }) {
  return <div>{children}</div>;
}
