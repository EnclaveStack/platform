import { RULES } from '../api/generated';
import type { TranscoderSpec } from '../api/types';

// What a transcoder writes and where (L2-012 TRX-02, TRX-03, TRX-07), as internal/transcode and the fake
// engine: the mock adapter's runs and the transcoder's page use the same names.

/** The ladder's master playlist under a run's folder. */
export const masterPlaylist = (s: Pick<TranscoderSpec, 'hls'>): string | undefined => (s.hls ? `${s.hls.name || RULES.hlsNameDefault}/master.m3u8` : undefined);

/** Every file one run writes, relative to its folder, in the order they are written. */
export function transcodeFiles(s: Pick<TranscoderSpec, 'presets' | 'renditions' | 'hls'>): string[] {
  const files: string[] = [];
  for (const p of s.presets ?? []) {
    const f = RULES.presetFiles[p];
    files.push(f, ...(f.endsWith('.m3u8') ? [f.replace(/index\.m3u8$/, 'segment-000.ts')] : []));
  }
  for (const r of s.renditions ?? []) files.push(`${r.name}.${r.container}`);
  if (s.hls) {
    const name = s.hls.name || RULES.hlsNameDefault;
    for (const r of s.hls.rungs) files.push(`${name}/${r.name}/index.m3u8`, `${name}/${r.name}/segment-000.ts`);
    files.push(`${name}/master.m3u8`);
  }
  return files;
}

const placeholder = /\{(name|meta:([^{}]*))\}/g;
const tagValue = /^[A-Za-z0-9._-]{1,128}$/;

export const hasPlaceholders = (prefix: string) => /\{(name|meta:[^{}]*)\}/.test(prefix);

/** Where a run's outputs go, or the reason it can't run (TRN-06, TRX-07). */
export function runFolder(outPrefix: string, inPrefix: string, key: string, tags: Record<string, string> = {}): { folder: string } | { error: string } {
  const file = key.split('/').pop() ?? key;
  const name = file.replace(/\.[^.]*$/, '');
  if (!hasPlaceholders(outPrefix)) return { folder: `${outPrefix}${key.slice(inPrefix.length).replace(/\.[^./]*$/, '')}/` };
  let error = '';
  const folder = outPrefix.replace(placeholder, (_, what: string, tag?: string) => {
    if (what === 'name') return name;
    const v = tags[tag ?? ''];
    if (!v) error = `the input has no tag ${tag ?? ''}, which the output folder ${outPrefix} needs`;
    else if (!tagValue.test(v) || v === '.' || v === '..') error = `the input's tag ${tag ?? ''} is "${v}"; a folder takes letters, digits, '.', '_' and '-' only`;
    return v ?? '';
  });
  return error ? { error } : { folder };
}
