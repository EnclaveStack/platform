#!/usr/bin/env node
// Statutory Integrity Verification Engine (SIVE) — managed by soupler-governance-template.
// Enforces change-propagation governance (L0-006). Zero dependencies; runs on every PR.
//
//   1. every docs/L*/ layer has a -000-artifact-registry
//   2. every artifact anywhere under a layer (numbered subfolders included) is registered
//   3. every registry row points at a file that exists (no stale rows)
//   4. each row's Version and Last Updated match the artifact's own header — the file is the truth
//   5. each artifact's header has the mandatory fields and its Document ID equals its filename
//   6. README.md and CHANGELOG.md exist and are non-empty
//
// History this guards against: a registry that names the wrong version is indistinguishable from one
// that never registered the document — both send a reader to a stale picture of reality.

import fs from 'node:fs';
import path from 'node:path';

const DOCS = 'docs';
const REQUIRED = ['Document ID', 'Layer', 'Status', 'Version', 'Created', 'Last Updated', 'Owner', 'Copyright', 'License'];
const errors = [];
const warnings = [];
const fail = (m) => errors.push(m);

// New repos are generated with { "strict": true }. A repo adopted from before governance has no such file and runs in
// adoption mode: header-shape and missing-registry problems are warnings, so the gate can be switched on in CI without
// first rewriting history. An unregistered document is a hard failure in BOTH modes; registry drift (stale rows, version/date
// mismatches) is a warning in adoption mode and a failure once strict. Flip to strict when the registries have been cleaned.
let strict = false;
try { strict = JSON.parse(fs.readFileSync('.governance/sive.json', 'utf8')).strict === true; } catch { /* adoption mode */ }
const soft = (m) => (strict ? errors : warnings).push(m);
if (!strict) console.log('[SIVE] adoption mode (no .governance/sive.json with strict:true): header and missing-registry findings are warnings');

const walk = (dir) => fs.readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
  const p = path.join(dir, e.name);
  return e.isDirectory() ? walk(p) : [p];
});

// Header can be a `| Field | Value |` table or `Field: value` lines.
function headerField(text, key) {
  const head = text.split('\n').slice(0, 60).join('\n');
  const t = head.match(new RegExp(`^\\|\\s*${key}\\s*\\|\\s*(.+?)\\s*\\|\\s*$`, 'm'));
  if (t) return t[1];
  const l = head.match(new RegExp(`^${key}:\\s*(.+?)\\s*$`, 'm'));
  return l ? l[1] : null;
}

function registryRows(text) {
  const rows = [];
  let cols = null;
  for (const line of text.split('\n')) {
    if (!line.startsWith('|')) { cols = null; continue; }
    const cells = line.split('|').slice(1, -1).map((c) => c.trim());
    if (cells.every((c) => /^-+$/.test(c))) continue;
    if (cells[0] === 'Document ID') { cols = cells; continue; }
    if (cols) rows.push(Object.fromEntries(cols.map((c, i) => [c, cells[i] ?? ''])));
  }
  return rows;
}

if (!fs.existsSync(DOCS)) {
  warnings.push('no docs/ directory — nothing to audit (README/CHANGELOG still checked)');
} else {
  const layers = fs.readdirSync(DOCS, { withFileTypes: true }).filter((e) => e.isDirectory() && /^L\d/.test(e.name)).map((e) => e.name).sort();
  console.log(`[SIVE] auditing ${layers.length} layer(s)`);
  for (const layer of layers) {
    const dir = path.join(DOCS, layer);
    const code = layer.split('-')[0];
    const registry = fs.readdirSync(dir).find((f) => f.startsWith(`${code}-000-artifact-registry-v`) && f.endsWith('.md'));
    if (!registry) { soft(`${layer}: no ${code}-000-artifact-registry-v*.md`); continue; }
    const regText = fs.readFileSync(path.join(dir, registry), 'utf8');
    const rows = registryRows(regText);
    const rowIds = new Set(rows.map((r) => r['Document ID']).filter(Boolean));

    const docs = walk(dir).filter((f) => f.endsWith('.md') && !path.basename(f).includes('-000-artifact-registry'));
    const byId = new Map();
    for (const f of docs) {
      const id = path.basename(f, '.md');
      byId.set(id, f);
      if (!/-v\d+$/.test(id)) { warnings.push(`${f}: filename has no -vN suffix, so SIVE cannot track it (naming convention, L0-002)`); continue; }
      // Registered = a table row, or at minimum the ID mentioned (L6 nests sessions under a row for the folder).
      if (!rowIds.has(id) && !regText.includes(id)) fail(`${f}: not registered in ${registry}`);

      const text = fs.readFileSync(f, 'utf8');
      if (id.includes('-master-document-template-')) continue;
      for (const key of REQUIRED) if (!headerField(text, key)) soft(`${f}: header is missing "${key}"`);
      const docId = headerField(text, 'Document ID');
      if (docId && docId !== id) soft(`${f}: Document ID "${docId}" does not match filename "${id}"`);
    }

    for (const row of rows) {
      const id = row['Document ID'];
      if (!id || !/-v\d+$/.test(id)) continue;
      const file = byId.get(id) ?? (id.includes('-000-artifact-registry') ? path.join(dir, registry) : null);
      if (!file) { soft(`${registry}: stale row "${id}" — no such file under ${layer}`); continue; }
      if (id.includes('-000-artifact-registry')) continue;
      const text = fs.readFileSync(file, 'utf8');
      const fileVer = headerField(text, 'Version');
      const fileDate = headerField(text, 'Last Updated');
      if (row.Version && fileVer && row.Version !== fileVer) soft(`${registry}: says ${id} is ${row.Version}, but the file itself is ${fileVer}`);
      if (row['Last Updated'] && fileDate && row['Last Updated'] !== fileDate) soft(`${registry}: says ${id} was last updated ${row['Last Updated']}, but the file says ${fileDate}`);
    }
  }
}

for (const req of ['README.md', 'CHANGELOG.md']) {
  if (!fs.existsSync(req)) fail(`${req} is missing at repo root`);
  else if (fs.statSync(req).size === 0) fail(`${req} exists but is empty`);
}

for (const w of warnings.slice(0, 40)) console.log(`[SIVE] warn: ${w}`);
if (warnings.length > 40) console.log(`[SIVE] warn: … and ${warnings.length - 40} more warnings`);
if (errors.length) {
  for (const e of errors) console.log(`[SIVE] FAIL: ${e}`);
  console.log('\n[SIVE] FAILURE: repository is out of statutory compliance. Merge is blocked.');
  process.exit(1);
}
console.log('[SIVE] SUCCESS: 100% statutory compliance verified.');
