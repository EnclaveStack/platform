import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';

// CON-69: every field of a form that creates or changes a resource says what it does and what to enter.
// Sign-in, setup and account forms are left out: their fields (email, password) explain themselves.
const FORMS = [
  'routes/ResourceForm.tsx', 'routes/CataloguePage.tsx', 'components/ComputeFields.tsx', 'components/DataFields.tsx', 'components/DataPanels.tsx',
  'components/MachineFields.tsx', 'components/NetworkingFields.tsx', 'components/OperateFields.tsx', 'components/StackFields.tsx',
];

/** The opening tag starting at `at`: up to the first `>` outside braces, so `(e) => …` in a prop doesn't end it. */
function openingTag(src: string, at: number): string {
  let depth = 0;
  for (let i = at; i < src.length; i++) {
    if (src[i] === '{') depth++;
    else if (src[i] === '}') depth--;
    else if (src[i] === '>' && depth === 0) return src.slice(at, i + 1);
  }
  return src.slice(at);
}

describe('form fields have hints (CON-69)', () => {
  it('gives every Field of a resource form a hint', () => {
    const missing = FORMS.flatMap((f) => {
      const src = readFileSync(join(process.cwd(), 'src', f), 'utf8');
      return [...src.matchAll(/<Field\b/g)]
        .filter((m) => !/\bhint=/.test(openingTag(src, m.index)))
        .map((m) => `${f}:${src.slice(0, m.index).split('\n').length}`);
    });
    expect(missing).toEqual([]);
  });
});
