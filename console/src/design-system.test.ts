import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';

// ADR-014: every visual attribute of the console — colour, type, spacing, radius, shadow — comes from
// @enclavestack/ui. The console holds behaviour and copy only, so these checks fail on any local styling.
const ROOT = join(process.cwd(), 'src');
const SELF = 'design-system.test.ts';
const COLOUR = /#[0-9a-fA-F]{3,8}\b|\brgba?\(|\bhsla?\(|\boklch\(/;

function files(dir: string): string[] {
  return readdirSync(dir).flatMap((f) => {
    const p = join(dir, f);
    return statSync(p).isDirectory() ? files(p) : [p];
  });
}

const all = files(ROOT).map((p) => relative(ROOT, p));
const source = all.filter((f) => /\.(ts|tsx)$/.test(f) && f !== SELF && f !== 'api/generated.ts');
const offences = (test: RegExp) =>
  source.flatMap((f) => readFileSync(join(ROOT, f), 'utf8').split('\n').flatMap((line, i) => (test.test(line) ? [`${f}:${i + 1}: ${line.trim()}`] : [])));

describe('console uses the design system only (ADR-014)', () => {
  it('has no stylesheets of its own', () => {
    expect(all.filter((f) => /\.(css|scss|sass|less)$/.test(f))).toEqual([]);
  });

  it('has no inline styles', () => {
    expect(offences(/\bstyle=\{/)).toEqual([]);
  });

  it('has no colour literals', () => {
    expect(offences(COLOUR)).toEqual([]);
  });

  it('has no hand-written class names; classes come from @enclavestack/ui components and helpers', () => {
    expect(offences(/className=["'`]|className=\{\s*["'`]/)).toEqual([]);
  });

  it('imports the design system styles exactly once, at the entry point', () => {
    expect(offences(/@enclavestack\/ui\/styles\.css/)).toEqual(["main.tsx:1: import '@enclavestack/ui/styles.css';"]);
  });
});
