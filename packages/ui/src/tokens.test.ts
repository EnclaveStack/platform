import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';

// Vitest runs from the package root; jsdom's import.meta.url is not a file URL.
const here = join(process.cwd(), 'src');
const read = (f: string) => readFileSync(join(here, f), 'utf8');
const COLOUR = /#[0-9a-fA-F]{3,8}\b|\brgba?\(|\bhsla?\(|\boklch\(/;

function block(css: string, selector: string): string {
  const start = css.indexOf(selector);
  if (start < 0) throw new Error(`missing ${selector}`);
  let depth = 0;
  for (let i = css.indexOf('{', start); i < css.length; i++) {
    if (css[i] === '{') depth++;
    if (css[i] === '}' && --depth === 0) return css.slice(start, i);
  }
  throw new Error(`unclosed ${selector}`);
}

const names = (css: string) => new Set([...css.matchAll(/(--es-[a-z0-9-]+)\s*:/g)].map((m) => m[1] ?? ''));

describe('design tokens (ADR-014)', () => {
  const tokens = read('tokens.css');
  const light = names(block(tokens, ':root {'));
  const darkOs = names(block(tokens, ":root:not([data-theme='light'])"));
  const darkForced = names(block(tokens, ":root[data-theme='dark']"));

  it('dark mode overrides the same tokens whether chosen by the OS or forced', () => {
    expect([...darkOs].sort()).toEqual([...darkForced].sort());
  });

  it('dark mode only overrides tokens that exist in light mode', () => {
    for (const n of darkOs) expect(light, n).toContain(n);
  });

  it('every colour token has a dark value', () => {
    const colour = [...light].filter((n) => /^--es-(bg|panel|surface|sunk|hair|ink|muted|subtle|faint|accent|on-|act|ok|warn|bad|av-|series-|code-bg|wash|scrim)/.test(n));
    for (const n of colour) expect(darkOs, n).toContain(n);
  });

  it('component and layout CSS use only tokens: no colour literals', () => {
    for (const f of ['components.css', 'layout.css', 'pages.css']) {
      const offending = read(f).split('\n').filter((l) => COLOUR.test(l));
      expect(offending, f).toEqual([]);
    }
  });

  it('every var(--es-*) used by the CSS is defined', () => {
    const used = new Set<string>();
    for (const f of ['components.css', 'layout.css', 'pages.css', 'tokens.css']) for (const m of read(f).matchAll(/var\((--es-[a-z0-9-]+)/g)) if (m[1]) used.add(m[1]);
    // Set by a rule for its own children: the grid's column width, a guide part's tone.
    const defined = new Set([...light, '--es-grid-min', '--es-guide-tone']);
    for (const u of used) expect(defined, u).toContain(u);
  });

  it('components never take inline styles or colour literals', () => {
    const dir = join(here, 'components');
    for (const f of readdirSync(dir)) {
      const src = readFileSync(join(dir, f), 'utf8');
      expect(src, f).not.toMatch(/style=\{/);
      expect(src.split('\n').filter((l) => COLOUR.test(l)), f).toEqual([]);
    }
  });
});
