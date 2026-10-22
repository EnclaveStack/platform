import { Check, Copy } from 'lucide-react';
import { useEffect, useState, type ReactNode } from 'react';
import { Button } from './controls';
import { cx } from './primitives';

/** Labelled code with a copy button; when the clipboard is blocked it asks the reader to select the text. */
export function CodeBlock({ label, code }: { label: string; code: string }) {
  const [state, setState] = useState<'idle' | 'copied' | 'failed'>('idle');
  useEffect(() => {
    if (state === 'idle') return;
    const t = setTimeout(() => setState('idle'), 1800);
    return () => clearTimeout(t);
  }, [state]);
  async function copy() {
    try {
      await navigator.clipboard.writeText(code);
      setState('copied');
    } catch {
      setState('failed');
    }
  }
  return (
    <div className="es-code-block">
      <div className="es-code-head">
        <span>{label}</span>
        <Button size="sm" variant="ghost" icon={state === 'copied' ? <Check /> : <Copy />} onClick={copy} aria-label={`Copy ${label}`}>
          {state === 'copied' ? 'Copied' : state === 'failed' ? 'Select it' : 'Copy'}
        </Button>
      </div>
      <pre className="es-code" aria-label={label}>{code}</pre>
    </div>
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
