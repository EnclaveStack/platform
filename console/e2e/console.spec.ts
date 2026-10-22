import { expect, test, type Page } from '@playwright/test';
import { readFileSync } from 'node:fs';

// One operator journey through the real server, in order (serial): setup → every kind → edit →
// delete → tokens → sign out. Each step checks the UI and, where it matters, the API itself.

test.describe.configure({ mode: 'serial' });

const ADMIN = { email: 'e2e@example.com', password: 'e2e-password-123' };
let page: Page;

test.beforeAll(async ({ browser }) => {
  page = await browser.newPage();
});

async function create(kind: string, fill: (panel: ReturnType<Page['getByRole']>) => Promise<void>) {
  const section = { app: 'apps', database: 'databases', cache: 'caches', bucket: 'buckets', secret: 'secrets' }[kind]!;
  await page.getByRole('link', { name: new RegExp(`^${section[0]!.toUpperCase()}${section.slice(1)}`) }).click();
  await page.getByRole('button', { name: `New ${kind}` }).first().click();
  const panel = page.getByRole('dialog', { name: `New ${kind}` });
  await fill(panel);
  await panel.getByRole('button', { name: `Create ${kind}` }).click();
  await expect(page.getByRole('group', { name: 'Summary' }).getByText('Ready')).toBeVisible({ timeout: 20_000 });
}

test('first-run setup with the printed code', async () => {
  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'First-run setup' })).toBeVisible();
  const code = readFileSync(process.env.ES_E2E_SETUP_CODE_FILE!, 'utf8').trim();
  await page.getByLabel('Setup code', { exact: true }).fill(code);
  await page.getByLabel('Machine name', { exact: true }).fill('E2E machine');
  await page.getByLabel('Admin email', { exact: true }).fill(ADMIN.email);
  await page.getByLabel('Password', { exact: true }).fill(ADMIN.password);
  await page.getByRole('button', { name: 'Create admin and open console' }).click();
  await expect(page.getByRole('region', { name: 'Apps list' })).toBeVisible();
  await expect(page.getByText('No apps yet.')).toBeVisible();
});

test('create every kind from the console', async () => {
  await create('secret', async (p) => {
    await p.getByLabel('Name', { exact: true }).fill('stripe-key');
    await p.getByLabel('Value', { exact: true }).fill('sk_e2e_value');
  });
  await create('database', async (p) => {
    await p.getByLabel('Name', { exact: true }).fill('orders');
    await p.getByLabel('PostgreSQL version').selectOption('17');
  });
  await expect(page.getByText('orders-password', { exact: true })).toBeVisible();
  await create('cache', async (p) => {
    await p.getByLabel('Name', { exact: true }).fill('sessions');
    await p.getByLabel('Max memory (MiB)').fill('128');
  });
  await create('bucket', async (p) => {
    await p.getByLabel('Name', { exact: true }).fill('media');
    await p.getByLabel(/versioning/).check();
  });
  await create('app', async (p) => {
    await p.getByLabel('Name', { exact: true }).fill('web');
    await p.getByLabel('Image', { exact: true }).fill('nginx:1.27-alpine');
    await p.getByLabel('Port', { exact: true }).fill('80');
    await p.getByLabel('Publish the port on this machine').check();
    await p.getByRole('button', { name: 'Add secret variable' }).click();
    await p.getByLabel('Secret environment variables name 1').fill('STRIPE_KEY');
    await p.getByLabel('Secret environment variables secret 1').selectOption('stripe-key');
  });
  // Parity: the API sees exactly what the console made (ADR-003).
  const res = await page.request.get('/api/v1/resources');
  const names = (await res.json()).items.map((r: { kind: string; name: string }) => `${r.kind}/${r.name}`).sort();
  expect(names).toEqual(['app/web', 'bucket/media', 'cache/sessions', 'database/orders', 'secret/objectstore-credentials', 'secret/orders-password', 'secret/stripe-key']);
});

test('validation and conflicts show next to the field', async () => {
  await page.getByRole('link', { name: /^Caches/ }).click();
  await page.getByRole('button', { name: 'New cache' }).first().click();
  const panel = page.getByRole('dialog', { name: 'New cache' });
  await panel.getByLabel('Name', { exact: true }).fill('sessions');
  await panel.getByRole('button', { name: 'Create cache' }).click();
  await expect(panel.getByText('A cache named sessions already exists.')).toBeVisible();
  await panel.getByLabel('Name', { exact: true }).fill('x');
  await panel.getByLabel('Max memory (MiB)').fill('9000');
  await panel.getByRole('button', { name: 'Create cache' }).click();
  await expect(panel.getByText(/lowercase letters/)).toBeVisible();
  await expect(panel.getByText('Use 64–4096 MiB.')).toBeVisible();
  await page.keyboard.press('Escape');
});

test('edit an app in place and redeploy it', async () => {
  await page.getByRole('link', { name: /^Apps/ }).click();
  await page.getByRole('link', { name: /web/ }).click();
  await page.getByRole('button', { name: 'Edit' }).click();
  const panel = page.getByRole('dialog', { name: 'Edit app web' });
  await panel.getByLabel('Image', { exact: true }).fill('nginx:1.28-alpine');
  await panel.getByRole('button', { name: 'Save changes' }).click();
  await expect(page.getByRole('group', { name: 'Summary' }).getByText('Ready')).toBeVisible({ timeout: 20_000 });
  await expect(page.getByRole('region', { name: 'Details' }).getByText('nginx:1.28-alpine', { exact: true })).toBeVisible();
  await expect(page.getByRole('group', { name: 'Summary' }).getByText('Ready')).toBeVisible({ timeout: 20_000 });
  await page.getByRole('button', { name: 'Redeploy' }).click();
  await expect(page.getByRole('group', { name: 'Summary' }).getByText('Ready')).toBeVisible({ timeout: 20_000 });
  await page.getByRole('tab', { name: 'Events' }).click();
  await expect(page.getByText('Image pulled again and a new revision deployed.')).toBeVisible();
});

test('reveal a secret, and refuse deleting it while the app uses it', async () => {
  await page.getByRole('link', { name: /^Secrets/ }).click();
  await page.getByRole('link', { name: /stripe-key/ }).click();
  await page.getByRole('button', { name: 'Reveal value' }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Reveal' }).click();
  await expect(page.getByLabel('Secret value')).toHaveText('sk_e2e_value');
  await page.getByRole('button', { name: 'Delete' }).click();
  const dialog = page.getByRole('alertdialog');
  await dialog.getByLabel(/Type/).fill('stripe-key');
  await dialog.getByRole('button', { name: 'Delete secret' }).click();
  await expect(dialog.getByText(/Used by the app web/)).toBeVisible();
  await dialog.getByRole('button', { name: 'Cancel' }).click();
});

test('serve the app on a domain, then remove the domain (CON-19, CON-20)', async () => {
  await page.getByRole('link', { name: /^Apps/ }).click();
  await page.getByRole('link', { name: /web/ }).click();
  await page.getByRole('button', { name: 'Add domain' }).click();
  const panel = page.getByRole('dialog', { name: 'New domain' });
  await expect(panel.getByLabel('App', { exact: true })).toHaveValue('web');
  await panel.getByLabel('Name', { exact: true }).fill('web-local');
  await panel.getByLabel('Hostname', { exact: true }).fill('web.localhost');
  await panel.getByRole('button', { name: 'Create domain' }).click();
  await expect(page.getByRole('group', { name: 'Summary' }).getByText('Ready')).toBeVisible({ timeout: 20_000 });
  await expect(page.getByRole('region', { name: 'Details' }).getByText('https://web.localhost', { exact: true })).toBeVisible();
  await expect(page.getByRole('heading', { name: "Trust this machine's certificates" })).toBeVisible();
  await page.getByRole('button', { name: 'Delete' }).click();
  const dialog = page.getByRole('alertdialog');
  await dialog.getByLabel(/Type/).fill('web-local');
  await dialog.getByRole('button', { name: 'Delete domain' }).click();
  await expect(page.getByText('No domains yet.')).toBeVisible({ timeout: 20_000 });
});

test('run a job from the console and read its output (CON-22)', async () => {
  await page.getByRole('link', { name: /^Jobs/ }).click();
  await page.getByRole('button', { name: 'New job' }).first().click();
  const panel = page.getByRole('dialog', { name: 'New job' });
  await panel.getByLabel('Name', { exact: true }).fill('tick');
  await panel.getByLabel('Schedule', { exact: true }).fill('@daily');
  await panel.getByLabel('Run with', { exact: true }).selectOption('image');
  await panel.getByLabel('Image', { exact: true }).fill('busybox:1.37');
  await panel.getByLabel('Command', { exact: true }).fill('echo from-e2e');
  await panel.getByRole('button', { name: 'Create job' }).click();
  await expect(page.getByRole('group', { name: 'Summary' }).getByText('Ready')).toBeVisible({ timeout: 20_000 });
  await page.getByRole('button', { name: 'Run now' }).click();
  const runs = page.getByRole('table', { name: 'Runs' });
  await expect(runs.getByText('Succeeded')).toBeVisible({ timeout: 20_000 });
  await runs.getByRole('button', { name: /Output of run/ }).click();
  await expect(page.getByLabel('Run output')).toContainText('tick: echo from-e2e');
  await page.keyboard.press('Escape');
  await page.getByRole('button', { name: 'Delete' }).click();
  const dialog = page.getByRole('alertdialog');
  await dialog.getByLabel(/Type/).fill('tick');
  await dialog.getByRole('button', { name: 'Delete job' }).click();
  await expect(page.getByText('No jobs yet.')).toBeVisible({ timeout: 20_000 });
});

test('back up a cache, restore it into a new one, and delete the backup (CON-23)', async () => {
  await page.getByRole('link', { name: /^Caches/ }).click();
  await page.getByRole('link', { name: /sessions/ }).click();
  await page.getByRole('tab', { name: 'Backups' }).click();
  await page.getByRole('button', { name: 'Set up backups' }).click();
  const panel = page.getByRole('dialog', { name: 'New backup' });
  await panel.getByLabel('Name', { exact: true }).fill('sessions-nightly');
  await expect(panel.getByLabel('Cache', { exact: true })).toHaveValue('sessions');
  await panel.getByLabel('Bucket', { exact: true }).selectOption('media');
  await panel.getByRole('button', { name: 'Create backup' }).click();
  await expect(page.getByRole('group', { name: 'Summary' }).getByText('Ready')).toBeVisible({ timeout: 20_000 });
  await page.getByRole('button', { name: 'Back up now' }).click();
  const runs = page.getByRole('table', { name: 'Backup runs' });
  await expect(runs.getByText('Succeeded')).toBeVisible({ timeout: 20_000 });
  await runs.getByRole('button', { name: /Details of run/ }).click();
  await expect(page.getByRole('dialog', { name: /^Run run_/ })).toContainText(/enclave-backups\/sessions\/\d{8}T\d{6}\.\d{3}Z-run_[a-z2-7]{16}\.rdb/);
  await page.keyboard.press('Escape');
  await runs.getByRole('button', { name: /Restore run/ }).click();
  const drawer = page.getByRole('dialog', { name: 'Restore backup' });
  await expect(drawer.getByRole('radio', { name: /Into a new cache/ })).toBeChecked();
  await drawer.getByLabel('New cache name').fill('sessions-copy');
  await drawer.getByRole('button', { name: 'Restore into sessions-copy' }).click();
  await expect(page.getByRole('heading', { name: 'sessions-copy' })).toBeVisible();
  await expect(page.getByRole('group', { name: 'Summary' }).getByText('Ready')).toBeVisible({ timeout: 20_000 });
  const copy = (await (await page.request.get('/api/v1/resources?kind=cache')).json()).items.find((r: { name: string }) => r.name === 'sessions-copy');
  expect(copy.outputs.restoredFrom).toMatch(/^sessions-nightly run run_/);

  await page.getByRole('link', { name: /^Backups/ }).click();
  await page.getByRole('link', { name: /sessions-nightly/ }).click();
  await page.getByRole('button', { name: 'Delete' }).click();
  const dialog = page.getByRole('alertdialog');
  await expect(dialog).toContainText('Backups already stored in the bucket are kept');
  await dialog.getByLabel(/Type/).fill('sessions-nightly');
  await dialog.getByRole('button', { name: 'Delete backup' }).click();
  await expect(page.getByText('No backups yet.')).toBeVisible({ timeout: 20_000 });
});

test('build an app from Git and read its build log (CON-24)', async () => {
  await page.getByRole('link', { name: /^Apps/ }).click();
  await page.getByRole('button', { name: 'New app' }).first().click();
  const panel = page.getByRole('dialog', { name: 'New app' });
  await panel.getByLabel('Name', { exact: true }).fill('site');
  await panel.getByRole('radio', { name: /A Git repository/ }).check();
  await panel.getByLabel('Repository', { exact: true }).fill('https://git.example.com/acme/site.git');
  await panel.getByRole('button', { name: 'Add build argument' }).click();
  await panel.getByLabel('Build arguments name 1').fill('MODE');
  await panel.getByLabel('Build arguments value 1').fill('e2e');
  await panel.getByRole('button', { name: 'Create app' }).click();
  await expect(page.getByRole('group', { name: 'Summary' }).getByText('Ready')).toBeVisible({ timeout: 20_000 });
  await expect(page.getByText('https://git.example.com/acme/site.git @ main')).toBeVisible();
  await page.getByRole('tab', { name: 'Build log' }).click();
  await expect(page.getByLabel('Build output')).toContainText('naming to enclave.local/site:r1-');
  const app = (await (await page.request.get('/api/v1/resources?kind=app')).json()).items.find((r: { name: string }) => r.name === 'site');
  expect(app.outputs.builtCommit).toMatch(/^[0-9a-f]{40}$/);
  expect(app.spec.source).toMatchObject({ git: 'https://git.example.com/acme/site.git', ref: 'main', buildArgs: { MODE: 'e2e' } });
  await page.getByRole('button', { name: 'Delete' }).click();
  const dialog = page.getByRole('alertdialog');
  await dialog.getByLabel(/Type/).fill('site');
  await dialog.getByRole('button', { name: 'Delete app' }).click();
  await expect(page.getByRole('heading', { name: 'site' })).toHaveCount(0, { timeout: 20_000 });
});

test('networks, firewalls, load balancers, zones and records (CON-26 – CON-30)', async () => {
  const open = async (label: RegExp, kind: string) => {
    await page.getByRole('link', { name: label }).click();
    await page.getByRole('button', { name: `New ${kind}` }).first().click();
    return page.getByRole('dialog', { name: `New ${kind}` });
  };
  const ready = () => expect(page.getByRole('group', { name: 'Summary' }).getByText('Ready')).toBeVisible({ timeout: 20_000 });

  let p = await open(/^Networks/, 'network');
  await p.getByLabel('Name', { exact: true }).fill('backend');
  await p.getByLabel('Address range').fill('10.40.0.0/16');
  await p.getByRole('button', { name: 'Add subnet' }).click();
  await p.getByLabel('Subnets name 1').fill('data');
  await p.getByLabel('Subnets value 1').fill('10.40.1.0/24');
  await p.getByRole('button', { name: 'Create network' }).click();
  await ready();
  await expect(page.getByText('es-net-backend')).toBeVisible();

  p = await open(/^Firewalls/, 'firewall');
  await p.getByLabel('Name', { exact: true }).fill('web-in');
  await p.getByLabel('app/web').check();
  await p.getByRole('button', { name: 'Add rule' }).click();
  await p.getByLabel('Rule 1 ports').fill('80');
  await p.getByLabel('Rule 1 sources').fill('any');
  await p.getByRole('button', { name: 'Create firewall' }).click();
  await ready();
  await expect(page.getByText('Allow tcp 80 from any')).toBeVisible();

  p = await open(/^Load balancers/, 'load balancer');
  await p.getByLabel('Name', { exact: true }).fill('web-lb');
  await p.getByLabel('Port', { exact: true }).fill('8080');
  await p.getByRole('button', { name: 'Add target' }).click();
  await p.getByLabel('Target 1 app').selectOption('web');
  await p.getByRole('button', { name: 'Create load balancer' }).click();
  await ready();
  await expect(page.getByText('web-lb.loadbalancer.internal')).toBeVisible();

  await create('secret', async (s) => {
    await s.getByLabel('Name', { exact: true }).fill('cf-token');
    await s.getByLabel('Value', { exact: true }).fill('{"apiToken":"e2e"}');
  });
  p = await open(/^DNS/, 'zone');
  await p.getByLabel('Name', { exact: true }).fill('example');
  await p.getByLabel('Zone', { exact: true }).fill('example.com');
  await p.getByLabel('Credentials secret').selectOption('cf-token');
  await p.getByRole('button', { name: 'Create zone' }).click();
  await ready();
  // Records are added from their zone, which is filled in (CON-29).
  await page.getByRole('tab', { name: 'Records' }).click();
  await page.getByRole('button', { name: 'Add record' }).click();
  p = page.getByRole('dialog', { name: 'New record' });
  await expect(p.getByLabel('Zone', { exact: true })).toHaveValue('example');
  await p.getByLabel('Name', { exact: true }).fill('www');
  await p.getByLabel('Record name').fill('www');
  await p.getByLabel('Type', { exact: true }).selectOption('CNAME');
  await p.getByLabel('Values').fill('web.example.net');
  await p.getByRole('button', { name: 'Create record' }).click();
  await ready();
  await page.getByRole('link', { name: /^DNS/ }).click();
  await page.getByRole('link', { name: /example/ }).click();
  await page.getByRole('tab', { name: 'Records' }).click();
  await expect(page.getByRole('table', { name: 'Records in example.com' }).getByText('www.example.com')).toBeVisible();
  const rec = (await (await page.request.get('/api/v1/resources?kind=record')).json()).items[0];
  expect(rec.outputs.fqdn).toBe('www.example.com');

  // Remove what names the app, so the next step can delete it.
  for (const [label, name, kind] of [[/^Load balancers/, 'web-lb', 'load balancer'], [/^Firewalls/, 'web-in', 'firewall']] as const) {
    await page.getByRole('link', { name: label }).click();
    await page.getByRole('link', { name: new RegExp(name) }).click();
    await page.getByRole('button', { name: 'Delete' }).click();
    const dialog = page.getByRole('alertdialog');
    await dialog.getByLabel(/Type/).fill(name);
    await dialog.getByRole('button', { name: `Delete ${kind}` }).click();
    await expect(page.getByRole('heading', { name })).toHaveCount(0, { timeout: 20_000 });
  }
});

test('VMs, volumes and snapshots (CON-32 – CON-36)', async () => {
  const KEY = 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEXTqZbAPryM1c8TBIUjg6rtRL1Dfr2uR7E954v/Icd4 e2e@enclave';
  const summary = () => page.getByRole('group', { name: 'Summary' });
  const ready = () => expect(summary().getByText('Ready')).toBeVisible({ timeout: 20_000 });
  const open = async (label: RegExp, kind: string) => {
    await page.getByRole('link', { name: label }).click();
    await page.getByRole('button', { name: `New ${kind}` }).first().click();
    return page.getByRole('dialog', { name: `New ${kind}` });
  };

  let p = await open(/^Volumes/, 'volume');
  await p.getByLabel('Name', { exact: true }).fill('data');
  await p.getByRole('radio', { name: /Disk/ }).check();
  await p.getByLabel('Size (GiB)').fill('20');
  await p.getByRole('button', { name: 'Create volume' }).click();
  await ready();

  p = await open(/^VMs/, 'VM');
  await p.getByLabel('Name', { exact: true }).fill('worker');
  await p.getByRole('radio', { name: /Debian 12/ }).check();
  await p.getByRole('radio', { name: /Medium/ }).check();
  await p.getByLabel('Public keys').fill(KEY);
  await p.getByLabel('data', { exact: true }).check();
  await p.getByRole('button', { name: 'Create VM' }).click();
  await ready();
  await expect(page.getByText('enclave vm ssh worker')).toBeVisible();
  await expect(page.getByText('worker.vm.internal')).toBeVisible();

  // An edit that restarts asks first (CON-33); stop and start (VM-06).
  await page.getByRole('button', { name: 'Edit' }).click();
  p = page.getByRole('dialog', { name: 'Edit vm worker' });
  await p.getByLabel('CPUs', { exact: true }).fill('4');
  await p.getByRole('button', { name: 'Save changes' }).click();
  await expect(page.getByRole('alertdialog')).toContainText('restart to apply the new CPUs');
  await page.getByRole('alertdialog').getByRole('button', { name: 'Save and restart' }).click();
  await ready();
  await summary().getByRole('button', { name: 'Stop' }).click();
  await expect(summary().getByRole('button', { name: 'Start' })).toBeVisible({ timeout: 20_000 });
  await summary().getByRole('button', { name: 'Start' }).click();
  await expect(summary().getByRole('button', { name: 'Stop' })).toBeVisible({ timeout: 20_000 });

  // A snapshot of the VM, and a new VM from it (CON-35).
  await summary().getByRole('button', { name: 'Take snapshot' }).click();
  p = page.getByRole('dialog', { name: 'Take snapshot' });
  await p.getByLabel('Name', { exact: true }).fill('worker-1');
  await p.getByRole('button', { name: 'Create snapshot' }).click();
  await ready();
  await summary().getByRole('button', { name: 'Launch VM' }).click();
  p = page.getByRole('dialog', { name: 'Launch VM from snapshot' });
  await expect(p.getByLabel('Snapshot', { exact: true })).toHaveValue('worker-1');
  await p.getByLabel('Name', { exact: true }).fill('clone');
  await p.getByLabel('Public keys').fill(KEY);
  await p.getByRole('button', { name: 'Create VM' }).click();
  await ready();
  const vms = (await (await page.request.get('/api/v1/resources?kind=vm')).json()).items;
  expect(vms.map((v: { spec: { image: string } }) => v.spec.image).sort()).toEqual(['debian-12', 'snapshot/worker-1']);

  // Clean up, VMs first: their disk can't go while attached.
  for (const [label, name, kind] of [[/^VMs/, 'clone', 'VM'], [/^VMs/, 'worker', 'VM'], [/^Snapshots/, 'worker-1', 'snapshot'], [/^Volumes/, 'data', 'volume']] as const) {
    await page.getByRole('link', { name: label }).click();
    await page.getByRole('link', { name: new RegExp(`^${name}`) }).click();
    await page.getByRole('button', { name: 'Delete' }).click();
    const dialog = page.getByRole('alertdialog');
    await dialog.getByLabel(/Type/).fill(name);
    await dialog.getByRole('button', { name: `Delete ${kind}` }).click();
    await expect(page.getByRole('heading', { name, exact: true })).toHaveCount(0, { timeout: 20_000 });
  }
});

test('queues, topics, a MySQL database and email (CON-37 – CON-39, CON-43)', async () => {
  const summary = () => page.getByRole('group', { name: 'Summary' });
  const ready = () => expect(summary().getByText('Ready')).toBeVisible({ timeout: 20_000 });
  const open = async (label: RegExp, kind: string) => {
    await page.getByRole('link', { name: label }).click();
    await page.getByRole('button', { name: `New ${kind}` }).first().click();
    return page.getByRole('dialog', { name: `New ${kind}` });
  };

  let p = await open(/^Databases/, 'database');
  await p.getByLabel('Name', { exact: true }).fill('legacy');
  await p.getByRole('radio', { name: /MySQL/ }).check();
  await expect(p.getByLabel('MySQL version')).toHaveValue('8.4');
  await p.getByRole('button', { name: 'Create database' }).click();
  await ready();
  await expect(page.getByText(/^mysql:\/\/legacy:<password>@/)).toBeVisible();

  p = await open(/^Queues/, 'queue');
  await p.getByLabel('Name', { exact: true }).fill('emails');
  await p.getByRole('button', { name: 'Create queue' }).click();
  await ready();

  // A topic fans out to the queue; the queue's Messages tab receives and deletes (CON-38, CON-39).
  p = await open(/^Topics/, 'topic');
  await p.getByLabel('Name', { exact: true }).fill('orders');
  await p.getByRole('button', { name: 'Add subscription' }).click();
  await p.getByLabel('Queue').selectOption('emails');
  await p.getByRole('button', { name: 'Create topic' }).click();
  await ready();
  await page.getByRole('tab', { name: 'Deliveries' }).click();
  await page.getByLabel('Message').fill('order 1');
  await page.getByRole('button', { name: 'Publish' }).click();
  await expect(page.getByText('queue/emails')).toBeVisible();

  await page.getByRole('link', { name: /^Queues/ }).click();
  await page.getByRole('link', { name: /^emails/ }).click();
  await page.getByRole('tab', { name: 'Messages' }).click();
  await expect(page.getByText('1 waiting')).toBeVisible({ timeout: 20_000 });
  await page.getByRole('button', { name: 'Receive up to 10' }).click();
  const received = page.getByRole('table', { name: 'Received messages' });
  await expect(received).toContainText('order 1');
  await received.getByRole('button', { name: 'Delete' }).click();
  await expect(received).toHaveCount(0);

  // Email: a test message is kept and opens (CON-43).
  p = await open(/^Email/, 'email service');
  await p.getByLabel('Name', { exact: true }).fill('shop-mail');
  await p.getByLabel('Domain').fill('example.com');
  await p.getByRole('button', { name: 'Create email service' }).click();
  await ready();
  await page.getByRole('tab', { name: 'Messages' }).click();
  await page.getByRole('button', { name: 'Send a test message' }).click();
  const drawer = page.getByRole('dialog', { name: 'Send a test message' });
  await drawer.getByLabel('To').fill('a@b.test');
  await drawer.getByRole('button', { name: 'Send' }).click();
  await page.getByRole('button', { name: 'A test from EnclaveStack' }).click();
  await expect(page.getByRole('dialog', { name: 'A test from EnclaveStack' })).toContainText('Hello from EnclaveStack.');
  await page.keyboard.press('Escape');

  for (const [label, name, kind] of [[/^Topics/, 'orders', 'topic'], [/^Queues/, 'emails', 'queue'], [/^Email/, 'shop-mail', 'email service'], [/^Databases/, 'legacy', 'database']] as const) {
    await page.getByRole('link', { name: label }).click();
    await page.getByRole('link', { name: new RegExp(`^${name}`) }).click();
    await page.getByRole('button', { name: 'Delete' }).click();
    const dialog = page.getByRole('alertdialog');
    await dialog.getByLabel(/Type/).fill(name);
    await dialog.getByRole('button', { name: `Delete ${kind}` }).click();
    await expect(page.getByRole('heading', { name, exact: true })).toHaveCount(0, { timeout: 20_000 });
  }
});

test('projects keep names apart; the switcher scopes lists and creates (CON-45)', async () => {
  await page.getByRole('link', { name: 'Projects' }).click();
  await page.getByRole('button', { name: 'New project' }).click();
  const panel = page.getByRole('dialog', { name: 'New project' });
  await panel.getByLabel('Name', { exact: true }).fill('shop');
  await panel.getByLabel('Description').fill('the shop');
  await panel.getByRole('button', { name: 'Create project' }).click();
  await expect(page.getByRole('cell', { name: 'shop', exact: true })).toBeVisible();
  await page.getByLabel('Project', { exact: true }).selectOption('shop');
  await page.getByRole('link', { name: /^Secrets/ }).click();
  await expect(page).toHaveURL(/\?project=shop/);
  await page.getByRole('button', { name: 'New secret' }).first().click();
  const sp = page.getByRole('dialog', { name: 'New secret' });
  await expect(sp.getByText('shop', { exact: true })).toBeVisible();
  await sp.getByLabel('Name', { exact: true }).fill('stripe-key');
  await sp.getByLabel('Value', { exact: true }).fill('sk_shop');
  await sp.getByRole('button', { name: 'Create secret' }).click();
  await expect(page.getByRole('group', { name: 'Summary' }).getByText('Ready')).toBeVisible({ timeout: 20_000 });
  // The same name lives in default and in shop (ADR-035).
  const items = (await (await page.request.get('/api/v1/resources?kind=secret&name=stripe-key')).json()).items as { project: string }[];
  expect(items.map((r) => r.project).sort()).toEqual(['default', 'shop']);
  await page.getByLabel('Project', { exact: true }).selectOption('');
});

test('monitoring: metrics of an app, the machine dashboard, a channel and an alert (CON-49 – CON-51)', async () => {
  await page.getByRole('link', { name: /^Apps/ }).click();
  await page.getByRole('link', { name: /web/ }).click();
  await page.getByRole('tab', { name: 'Metrics' }).click();
  await expect(page.getByRole('tab', { name: '1h' })).toHaveAttribute('aria-selected', 'true');
  await expect(page.getByText('CPU', { exact: true }).first()).toBeVisible();
  await page.getByRole('link', { name: /^Monitoring/ }).click();
  await expect(page.getByRole('heading', { name: 'This machine' })).toBeVisible();
  await expect(page.getByText('Disk free', { exact: true }).first()).toBeVisible();

  await page.getByRole('link', { name: /^Channels/ }).click();
  await page.getByRole('button', { name: 'New channel' }).first().click();
  const cp = page.getByRole('dialog', { name: 'New channel' });
  await cp.getByLabel('Name', { exact: true }).fill('ops');
  await cp.getByLabel('URL', { exact: true }).fill('http://127.0.0.1:9/hook');
  await cp.getByRole('button', { name: 'Create channel' }).click();
  await expect(page.getByRole('group', { name: 'Summary' }).getByText('Ready')).toBeVisible({ timeout: 20_000 });
  await page.getByRole('button', { name: 'Send a test' }).click();
  await expect(page.getByRole('status').filter({ hasText: /127\.0\.0\.1:9/ })).toBeVisible({ timeout: 20_000 });

  await page.getByRole('link', { name: /^Alerts/ }).click();
  await page.getByRole('button', { name: 'New alert' }).first().click();
  const ap = page.getByRole('dialog', { name: 'New alert' });
  await ap.getByLabel('Name', { exact: true }).fill('disk-full');
  await ap.getByRole('button', { name: 'Disk nearly full' }).click();
  await ap.getByRole('checkbox', { name: 'ops' }).check();
  await ap.getByRole('button', { name: 'Create alert' }).click();
  await expect(page.getByRole('group', { name: 'Summary' }).getByText('Ready')).toBeVisible({ timeout: 20_000 });
  await expect(page.getByText(/When Disk full of this machine is above 90 % for 5 minutes, notify ops/)).toBeVisible();
  await page.getByRole('button', { name: 'Delete' }).click();
  const dialog = page.getByRole('alertdialog');
  await dialog.getByLabel(/Type/).fill('disk-full');
  await dialog.getByRole('button', { name: 'Delete alert' }).click();
  await expect(page.getByText('No alerts yet.')).toBeVisible({ timeout: 20_000 });
});

test('invite a viewer who joins from the link and can only look (CON-46, CON-48)', async ({ browser }) => {
  await page.getByRole('link', { name: 'People' }).click();
  await page.getByRole('button', { name: 'Invite someone' }).click();
  const panel = page.getByRole('dialog', { name: 'Invite someone' });
  await panel.getByLabel('Email').fill('viewer@example.com');
  await panel.getByRole('radio', { name: /^Viewer/ }).check();
  await panel.getByRole('button', { name: 'Invite and show the link' }).click();
  const link = (await page.getByRole('status', { name: 'Link to send' }).getByLabel('Link', { exact: true }).textContent())!.trim();
  expect(link).toMatch(/\/invite\?code=/);
  await page.getByRole('button', { name: 'Done' }).click();

  const other = await browser.newPage();
  await other.goto(link);
  await expect(other.getByRole('heading', { name: 'Join this machine' })).toBeVisible();
  await expect(other.getByLabel('Email', { exact: true })).toHaveValue('viewer@example.com');
  await other.getByLabel('Password', { exact: true }).fill('viewer-password-1');
  await other.getByLabel('The same password again').fill('viewer-password-1');
  await other.getByRole('button', { name: 'Join' }).click();
  await expect(other.getByRole('region', { name: 'Apps list' })).toBeVisible();
  await expect(other.getByText('View only').first()).toBeVisible();
  await expect(other.getByRole('button', { name: 'New app' })).toHaveCount(0);
  expect((await other.request.post('/api/v1/resources', { data: { kind: 'secret', name: 'nope', spec: { value: 'x' } }, headers: { 'X-Enclave-CSRF': '1' } })).status()).toBe(403);
  await other.close();

  await page.reload();
  await expect(page.getByRole('row', { name: /viewer@example\.com/ }).getByText('Active')).toBeVisible({ timeout: 10_000 });
  await page.getByRole('link', { name: 'Audit log' }).click();
  await expect(page.getByText('user_invited').first()).toBeVisible();
});

test('delete the app with the typed name', async () => {
  await page.getByRole('link', { name: /^Apps/ }).click();
  await page.getByRole('link', { name: /web/ }).click();
  await page.getByRole('button', { name: 'Delete' }).click();
  const dialog = page.getByRole('alertdialog');
  await expect(dialog.getByRole('button', { name: 'Delete app' })).toBeDisabled();
  await dialog.getByLabel(/Type/).fill('web');
  await dialog.getByRole('button', { name: 'Delete app' }).click();
  await expect(page.getByText('No apps yet.')).toBeVisible({ timeout: 20_000 });
});

test('API tokens work for code and stop working when revoked', async ({ playwright }) => {
  await page.getByRole('link', { name: 'API tokens' }).click();
  await page.getByLabel('Name', { exact: true }).fill('ci');
  await page.getByRole('button', { name: 'Create token' }).click();
  const token = (await page.getByRole('status', { name: 'New token' }).getByLabel('Token', { exact: true }).textContent())!.trim();
  expect(token).toMatch(/^es_pat_/);
  const cli = await playwright.request.newContext({ baseURL: page.url().split('/settings')[0], extraHTTPHeaders: { Authorization: `Bearer ${token}` } });
  expect((await cli.get('/api/v1/machine')).status()).toBe(200);
  await page.getByLabel("I've copied it").check();
  await page.getByRole('button', { name: 'Done' }).click();
  const row = page.getByRole('row', { name: /ci/ });
  await row.getByRole('button', { name: 'Revoke' }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Revoke token' }).click();
  await expect(row).toHaveCount(0);
  expect((await cli.get('/api/v1/machine')).status()).toBe(401);
  await cli.dispose();
});

test('sign out and back in', async () => {
  await page.getByRole('button', { name: 'Sign out' }).click();
  await expect(page.getByRole('heading', { name: 'Sign in to the console' })).toBeVisible();
  await page.getByLabel('Email', { exact: true }).fill(ADMIN.email);
  await page.getByLabel('Password', { exact: true }).fill('wrong-password-x');
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page.getByRole('alert')).toContainText("don't match");
  await page.getByLabel('Password', { exact: true }).fill(ADMIN.password);
  await page.getByRole('button', { name: 'Sign in' }).click();
  // CON-03: back to the page that was open when the session ended.
  await expect(page.getByRole('heading', { name: 'API tokens' })).toBeVisible();
});
