import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import detailView from './routes/ResourceDetailView.tsx?raw';
import { DETAIL_HELP, OUTPUT_LABELS } from './lib/details';

// Every detail a page shows has its explanation (L2-013 DET-02), and the user guide's reference repeats
// each one word for word (DET-04).

/** The labels the Overview builds from a resource's spec: ['Label', value] pairs in its source. */
function specLabels(): string[] {
  const overview = detailView.slice(detailView.indexOf('function Overview('), detailView.indexOf('/** How to reach a VM over SSH'));
  return [...overview.matchAll(/\['([A-Z][^'$]*)', /g)].map((m) => m[1]!);
}

describe('detail explanations', () => {
  it('cover every output and spec label', () => {
    const labels = new Set([...Object.values(OUTPUT_LABELS), ...specLabels(), 'Created']);
    expect(specLabels().length).toBeGreaterThan(50); // the scan still finds the Overview's labels
    const missing = [...labels].filter((l) => !DETAIL_HELP[l]);
    expect(missing).toEqual([]);
  });

  it('are in the user guide word for word', () => {
    const guide = readFileSync(resolve(__dirname, '../../docs/L5-operations/L5-004-user-guide-v1.md'), 'utf8');
    const ref = guide.slice(guide.indexOf('## 14 · What each detail means'));
    const missing = Object.entries(DETAIL_HELP).filter(([label, help]) => !ref.includes(`| ${label} | ${help.replaceAll('|', '\\\\|')} |`));
    expect(missing.map(([l]) => l)).toEqual([]);
  });
});
