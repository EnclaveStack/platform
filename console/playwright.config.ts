import { defineConfig } from '@playwright/test';
import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

// Console e2e (L5-002): real Chrome → the real Go control plane (fake engine) serving the built console.
// The config is evaluated by the runner and again by each worker; the env var keeps one directory for all.
process.env.ES_E2E_WORK ||= mkdtempSync(join(tmpdir(), 'enclave-console-e2e-'));
const work = process.env.ES_E2E_WORK;
process.env.ES_E2E_SETUP_CODE_FILE = join(work, 'setup-code');
const PORT = 7181;

export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  workers: 1,
  timeout: 60_000,
  expect: { timeout: 10_000 },
  reporter: [['list']],
  use: { baseURL: `http://127.0.0.1:${PORT}`, channel: 'chrome', headless: true, trace: 'retain-on-failure' },
  webServer: {
    command: `pnpm build && cd .. && go run ./cmd/enclave server --driver fake --fake-step-delay 150ms --data-dir ${join(work, 'data')} --listen 127.0.0.1:${PORT} --console-dir console/dist --setup-code-file ${process.env.ES_E2E_SETUP_CODE_FILE} --log-format json`,
    url: `http://127.0.0.1:${PORT}/healthz`,
    timeout: 120_000,
    reuseExistingServer: false,
    stdout: 'ignore',
    stderr: 'pipe',
  },
});
