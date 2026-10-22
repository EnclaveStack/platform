import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { describe, expect, it } from 'vitest';
import { routes } from './App';
import { ApiProvider } from './api/context';
import { createMockApi, DEMO_ADMIN, DEMO_SETUP_CODE, type MockOptions } from './api/mock';
import { SessionProvider } from './auth/session';

// Operator flows through real routes and components, with the mock adapter behind EnclaveApi.

function renderConsole(path: string, options: MockOptions = {}) {
  const api = createMockApi({ latencyMs: 0, stepMs: 30, storage: null, ...options });
  const router = createMemoryRouter(routes, { initialEntries: [path] });
  render(
    <ApiProvider api={api}>
      <SessionProvider>
        <RouterProvider router={router} />
      </SessionProvider>
    </ApiProvider>,
  );
  return { router, api, user: userEvent.setup() };
}

async function signIn(user: ReturnType<typeof userEvent.setup>) {
  await user.type(await screen.findByLabelText('Email'), DEMO_ADMIN.email);
  await user.type(screen.getByLabelText('Password'), DEMO_ADMIN.password);
  await user.click(screen.getByRole('button', { name: 'Sign in' }));
}

describe('console', () => {
  it('first-run setup needs the code, then lands in the console (CON-02)', async () => {
    const { user, router } = renderConsole('/apps', { setupRequired: true });
    expect(await screen.findByRole('heading', { name: 'First-run setup' })).toBeInTheDocument();
    await user.type(screen.getByLabelText('Setup code'), 'WRONG-CODE');
    await user.type(screen.getByLabelText('Admin email'), 'me@example.com');
    await user.type(screen.getByLabelText('Password'), 'long-enough-pw');
    await user.click(screen.getByRole('button', { name: 'Create admin and open console' }));
    expect(await screen.findByText(/setup code isn't right/)).toBeInTheDocument();
    await user.clear(screen.getByLabelText('Setup code'));
    await user.type(screen.getByLabelText('Setup code'), DEMO_SETUP_CODE);
    await user.click(screen.getByRole('button', { name: 'Create admin and open console' }));
    expect(await screen.findByRole('region', { name: 'Apps list' })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/apps');
  });

  it('shows an empty state and creates a database through the form, following it to Ready (CON-05, CON-14)', async () => {
    const { user } = renderConsole('/databases');
    await signIn(user);
    expect(await screen.findByText('No databases yet.')).toBeInTheDocument();
    await user.click(screen.getAllByRole('button', { name: 'New database' })[0]!);
    const panel = await screen.findByRole('dialog', { name: 'New database' });
    await user.type(within(panel).getByLabelText('Name'), 'Bad_Name');
    await user.click(within(panel).getByRole('button', { name: 'Create database' }));
    expect(within(panel).getByText(/lowercase letters/)).toBeInTheDocument();
    await user.clear(within(panel).getByLabelText('Name'));
    await user.type(within(panel).getByLabelText('Name'), 'orders');
    await user.click(within(panel).getByRole('button', { name: 'Create database' }));
    expect(await screen.findByRole('heading', { name: 'orders' })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText('Ready')).toBeInTheDocument(), { timeout: 3000 });
    expect(screen.getByText('Password secret')).toBeInTheDocument();
    expect(screen.getByText('orders-password')).toBeInTheDocument();
  });

  it('creates an app with a secret variable, edits it in place, and shows Use from code (CON-14, CON-15, CON-18)', async () => {
    const { user, api } = renderConsole('/apps');
    await signIn(user);
    await api.createResource('secret', 'stripe-key', { value: 'sk_live' });
    await user.click((await screen.findAllByRole('button', { name: 'New app' }))[0]!);
    const panel = await screen.findByRole('dialog', { name: 'New app' });
    await user.type(within(panel).getByLabelText('Name'), 'web');
    await user.type(within(panel).getByLabelText('Image'), 'nginx:1.27-alpine');
    await user.type(within(panel).getByLabelText('Port'), '80');
    await user.click(within(panel).getByLabelText('Publish the port on this machine'));
    await user.click(within(panel).getByRole('button', { name: 'Add secret variable' }));
    await user.type(within(panel).getByLabelText('Secret environment variables name 1'), 'STRIPE_KEY');
    await user.selectOptions(within(panel).getByLabelText('Secret environment variables secret 1'), 'stripe-key');
    await user.click(within(panel).getByRole('button', { name: 'Create app' }));
    expect(await screen.findByRole('heading', { name: 'web' })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText('Ready')).toBeInTheDocument(), { timeout: 3000 });

    await user.click(screen.getByRole('tab', { name: 'Environment' }));
    expect(screen.getByText('STRIPE_KEY')).toBeInTheDocument();
    expect(screen.getByText('secret · stripe-key')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Edit' }));
    const edit = await screen.findByRole('dialog', { name: 'Edit app web' });
    expect(within(edit).getByLabelText('Name')).toHaveAttribute('readonly');
    await user.clear(within(edit).getByLabelText('Image'));
    await user.type(within(edit).getByLabelText('Image'), 'nginx:1.28-alpine');
    await user.click(within(edit).getByRole('button', { name: 'Save changes' }));
    await waitFor(() => expect(screen.getByText('Ready')).toBeInTheDocument(), { timeout: 3000 });
    await user.click(screen.getByRole('tab', { name: 'Overview' }));
    expect(screen.getByText('nginx:1.28-alpine')).toBeInTheDocument();
    await user.click(screen.getByText('Use from code'));
    expect(screen.getByText(/enclave app create web/)).toBeInTheDocument();
    expect(screen.getByText(/resource "enclave_app" "web"/)).toBeInTheDocument();
  });

  it('sets a health check and strategy, and shows the serving revision (CON-21)', async () => {
    const { user } = renderConsole('/apps');
    await signIn(user);
    await user.click((await screen.findAllByRole('button', { name: 'New app' }))[0]!);
    const panel = await screen.findByRole('dialog', { name: 'New app' });
    await user.type(within(panel).getByLabelText('Name'), 'web');
    await user.type(within(panel).getByLabelText('Image'), 'nginx:1.27-alpine');
    await user.type(within(panel).getByLabelText('Port'), '80');
    await user.selectOptions(within(panel).getByLabelText('Health check'), 'http');
    await user.clear(within(panel).getByLabelText('Path'));
    await user.type(within(panel).getByLabelText('Path'), 'healthz');
    await user.click(within(panel).getByRole('button', { name: 'Create app' }));
    expect(within(panel).getByText('Use a path that starts with /, like /healthz.')).toBeInTheDocument();
    await user.clear(within(panel).getByLabelText('Path'));
    await user.type(within(panel).getByLabelText('Path'), '/healthz');
    await user.selectOptions(within(panel).getByLabelText('Deploy strategy'), 'recreate');
    await user.click(within(panel).getByRole('button', { name: 'Create app' }));
    await waitFor(() => expect(screen.getByText('Ready')).toBeInTheDocument(), { timeout: 3000 });
    expect(screen.getByText('HTTP /healthz every 5 s, up to 12 times')).toBeInTheDocument();
    expect(screen.getByText('Recreate')).toBeInTheDocument();
    expect(screen.getByText('Serving revision')).toBeInTheDocument();
    expect(screen.getByText('Succeeded')).toBeInTheDocument();
    expect(screen.queryByText('latestRevision')).not.toBeInTheDocument();
  });

  it('adds a domain from an app, shows its certificate, how to trust it, and machine settings (CON-19, CON-20)', async () => {
    const { user, api } = renderConsole('/databases');
    await signIn(user);
    await api.createResource('app', 'web', { image: 'nginx:1.27-alpine', port: 80 });
    await user.click(await screen.findByRole('link', { name: /^Apps/ }));
    await user.click(await screen.findByRole('link', { name: /web/ }));
    await waitFor(() => expect(screen.getByText('Ready')).toBeInTheDocument(), { timeout: 3000 });
    await user.click(screen.getByRole('button', { name: 'Add domain' }));
    const panel = await screen.findByRole('dialog', { name: 'New domain' });
    await waitFor(() => expect(within(panel).getByLabelText('App')).toHaveValue('web'));
    await user.type(within(panel).getByLabelText('Name'), 'web-local');
    await user.type(within(panel).getByLabelText('Hostname'), 'intranet');
    await user.click(within(panel).getByRole('button', { name: 'Create domain' }));
    expect(within(panel).getByText('Use a hostname like api.example.com or web.localhost.')).toBeInTheDocument();
    await user.clear(within(panel).getByLabelText('Hostname'));
    await user.type(within(panel).getByLabelText('Hostname'), 'web.localhost');
    await user.click(within(panel).getByRole('button', { name: 'Create domain' }));
    expect(await screen.findByRole('heading', { name: 'web-local' })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText('Ready')).toBeInTheDocument(), { timeout: 3000 });
    expect(screen.getByText('https://web.localhost')).toBeInTheDocument();
    expect(screen.getByText('EnclaveStack certificate')).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: "Trust this machine's certificates" })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Download root certificate' })).toBeInTheDocument();
    await user.type(screen.getByLabelText('Public address'), '203.0.113.7');
    await user.click(screen.getByRole('button', { name: 'Save settings' }));
    await waitFor(async () => expect((await api.getMachine()).publicAddress).toBe('203.0.113.7'));
    expect(screen.getByRole('link', { name: /^Domains/ })).toBeInTheDocument();
  });

  it('creates a job, runs it now and shows the run and its output (CON-22)', async () => {
    const { user } = renderConsole('/jobs');
    await signIn(user);
    await user.click((await screen.findAllByRole('button', { name: 'New job' }))[0]!);
    const panel = await screen.findByRole('dialog', { name: 'New job' });
    await user.type(within(panel).getByLabelText('Name'), 'tick');
    await user.type(within(panel).getByLabelText('Schedule'), '@every 5m');
    await user.selectOptions(within(panel).getByLabelText('Run with'), 'image');
    await user.type(within(panel).getByLabelText('Image'), 'busybox:1.37');
    await user.click(within(panel).getByRole('button', { name: 'Create job' }));
    expect(within(panel).getByText(/Use 5 cron fields/)).toBeInTheDocument();
    await user.clear(within(panel).getByLabelText('Schedule'));
    await user.type(within(panel).getByLabelText('Schedule'), '0 3 * * *');
    await user.tab(); // fields revalidate on blur
    expect(within(panel).getByText(/Every day at 03:00/)).toBeInTheDocument();
    await user.type(within(panel).getByLabelText('Command'), 'echo hello');
    await user.click(within(panel).getByRole('button', { name: 'Create job' }));
    await waitFor(() => expect(screen.getByText('Ready')).toBeInTheDocument(), { timeout: 3000 });
    expect(screen.getByText('Every day at 03:00')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Run now' }));
    const table = await screen.findByRole('table', { name: 'Runs' });
    await waitFor(() => expect(within(table).getByText('Succeeded')).toBeInTheDocument(), { timeout: 3000 });
    expect(within(table).getByText('Manual')).toBeInTheDocument();
    await user.click(within(table).getByRole('button', { name: /Output of run/ }));
    const drawer = await screen.findByRole('dialog', { name: /^Run run_/ });
    await waitFor(() => expect(within(drawer).getByText('tick: echo hello')).toBeInTheDocument());
  });

  it('sets up backups from a database, backs up now, and restores into a new database or over the source (CON-23)', async () => {
    const { user, api, router } = renderConsole('/databases');
    await signIn(user);
    const shop = await api.createResource('database', 'shop', { engine: 'postgres', version: '16' });
    await api.createResource('bucket', 'vault', {});
    await user.click(await screen.findByRole('link', { name: /shop/ }, { timeout: 7000 }));
    await user.click(await screen.findByRole('tab', { name: 'Backups' }));
    await user.click(await screen.findByRole('button', { name: 'Set up backups' }));
    const panel = await screen.findByRole('dialog', { name: 'New backup' });
    await user.type(within(panel).getByLabelText('Name'), 'nightly');
    await waitFor(() => expect(within(panel).getByLabelText('Database')).toHaveValue('shop'));
    await user.click(within(panel).getByRole('button', { name: 'Create backup' }));
    expect(await within(panel).findByText('Choose a bucket.')).toBeInTheDocument();
    await user.selectOptions(within(panel).getByLabelText('Bucket'), 'vault');
    expect(within(panel).getByText(/Every day at 03:00/)).toBeInTheDocument();
    await user.click(within(panel).getByRole('button', { name: 'Create backup' }));
    expect(await screen.findByRole('heading', { name: 'nightly' })).toBeInTheDocument();
    expect(router.state.location.pathname).toMatch(/^\/backups\/bak_/);
    await waitFor(() => expect(screen.getByText('Ready')).toBeInTheDocument(), { timeout: 3000 });
    expect(screen.getByText('Database shop')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Back up now' }));
    const table = await screen.findByRole('table', { name: 'Backup runs' });
    await waitFor(() => expect(within(table).getByText('Succeeded')).toBeInTheDocument(), { timeout: 3000 });
    expect(within(table).getByText(/^\d+ (MB|KB)$/)).toBeInTheDocument();

    // Replace needs the typed name; the default is a new database.
    await user.click(within(table).getByRole('button', { name: /Restore run/ }));
    let drawer = await screen.findByRole('dialog', { name: 'Restore backup' });
    expect(within(drawer).getByRole('radio', { name: /Into a new database/ })).toBeChecked();
    await user.click(within(drawer).getByRole('radio', { name: /Replace the data in shop/ }));
    const replace = within(drawer).getByRole('button', { name: 'Replace data in shop' });
    expect(replace).toBeDisabled();
    await user.type(within(drawer).getByLabelText('Type shop to confirm'), 'shop');
    expect(replace).toBeEnabled();
    await user.click(replace);
    await waitFor(() => expect(router.state.location.pathname).toBe(`/databases/${shop.id}`));
    expect(await screen.findByText(/Restoring from backup nightly/)).toBeInTheDocument();

    await router.navigate(-1);
    await user.click(await screen.findByRole('tab', { name: 'Runs' }));
    const runs = await screen.findByRole('table', { name: 'Backup runs' });
    expect(await within(runs).findByText('Before restore')).toBeInTheDocument();
    await waitFor(() => expect(within(runs).queryByText('Running')).not.toBeInTheDocument(), { timeout: 3000 });
    await user.click(within(runs).getAllByRole('button', { name: /Restore run/ })[0]!);
    drawer = await screen.findByRole('dialog', { name: 'Restore backup' });
    expect(within(drawer).getByLabelText('New database name')).toHaveValue('shop-restored');
    await user.click(within(drawer).getByRole('button', { name: 'Restore into shop-restored' }));
    expect(await screen.findByRole('heading', { name: 'shop-restored' })).toBeInTheDocument();
  });

  it('creates an app from Git, shows the built commit and the build log, and names a failing step (CON-24)', async () => {
    const { user } = renderConsole('/apps');
    await signIn(user);
    await user.click((await screen.findAllByRole('button', { name: 'New app' }))[0]!);
    const panel = await screen.findByRole('dialog', { name: 'New app' });
    await user.type(within(panel).getByLabelText('Name'), 'site');
    await user.click(within(panel).getByRole('radio', { name: /A Git repository/ }));
    expect(within(panel).queryByLabelText('Image')).not.toBeInTheDocument();
    await user.type(within(panel).getByLabelText('Repository'), 'github.com/acme/site');
    await user.click(within(panel).getByRole('button', { name: 'Create app' }));
    expect(within(panel).getByText(/Use an https:\/\/ or ssh:\/\/ repository URL/)).toBeInTheDocument();
    await user.clear(within(panel).getByLabelText('Repository'));
    await user.type(within(panel).getByLabelText('Repository'), 'https://github.com/acme/site.git');
    expect(within(panel).getByLabelText('Branch, tag or commit')).toHaveValue('main');
    await user.click(within(panel).getByRole('button', { name: 'Create app' }));
    expect(await screen.findByRole('heading', { name: 'site' })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText('Ready')).toBeInTheDocument(), { timeout: 3000 });
    expect(screen.getByText('https://github.com/acme/site.git @ main')).toBeInTheDocument();
    expect(screen.getByText('Built commit')).toBeInTheDocument();
    await user.click(screen.getByRole('tab', { name: 'Build log' }));
    expect(await screen.findByText('#4 [3/4] RUN npm ci')).toBeInTheDocument();
  });

  it('a failing build says which step failed (BLD-03)', async () => {
    const { user, api } = renderConsole('/apps');
    await signIn(user);
    await api.createResource('app', 'broken', { source: { git: 'https://github.com/acme/fail-tests.git', ref: 'main' } });
    await user.click(await screen.findByRole('link', { name: /broken/ }, { timeout: 7000 }));
    expect(await screen.findByText(/npm test" did not complete successfully: exit code: 1/, undefined, { timeout: 3000 })).toBeInTheDocument();
    await user.click(screen.getByRole('tab', { name: 'Build log' }));
    expect(await screen.findByText(/#5 ERROR: process/)).toBeInTheDocument();
  });

  it('deleting needs the typed name and lists dependants when refused (CON-16)', async () => {
    const { user, api } = renderConsole('/secrets');
    await signIn(user);
    const sec = await api.createResource('secret', 'stripe-key', { value: 'v' });
    await api.createResource('app', 'web', { image: 'nginx', secretEnv: { K: 'stripe-key' } });
    await user.click(await screen.findByRole('link', { name: /stripe-key/ }, { timeout: 7000 }));
    await user.click(await screen.findByRole('button', { name: 'Delete' }));
    const dialog = await screen.findByRole('alertdialog');
    const confirm = within(dialog).getByRole('button', { name: 'Delete secret' });
    expect(confirm).toBeDisabled();
    await user.type(within(dialog).getByLabelText(/Type/), 'stripe-key');
    expect(confirm).toBeEnabled();
    await user.click(confirm);
    expect(await within(dialog).findByText(/Used by the app web/)).toBeInTheDocument();
    expect((await api.getResource(sec.id)).status.phase).not.toBe('deleting');
  });

  it('reveals a secret only after confirming, and hides it again (CON-08)', async () => {
    const { user, api } = renderConsole('/secrets');
    await signIn(user);
    await api.createResource('secret', 'api-key', { value: 'sk_live_TOPSECRET' });
    await user.click(await screen.findByRole('link', { name: /api-key/ }, { timeout: 7000 }));
    expect(screen.queryByText('sk_live_TOPSECRET')).not.toBeInTheDocument();
    await user.click(await screen.findByRole('button', { name: 'Reveal value' }));
    await user.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Reveal' }));
    expect(await screen.findByLabelText('Secret value')).toHaveTextContent('sk_live_TOPSECRET');
    await user.click(screen.getByRole('button', { name: 'Hide' }));
    expect(screen.queryByText('sk_live_TOPSECRET')).not.toBeInTheDocument();
  });

  it('a failed resource shows its message and Retry (CON-17)', async () => {
    const { user, api } = renderConsole('/apps');
    await signIn(user);
    const r = await api.createResource('app', 'fail-web', { image: 'nginx' });
    await user.click(await screen.findByRole('link', { name: /fail-web/ }, { timeout: 7000 }));
    expect(await screen.findByRole('alert', {}, { timeout: 3000 })).toHaveTextContent('image not found');
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument();
    expect(r.id).toBeTruthy();
  });

  it('creates an API token shown once, lists it without the secret, and revokes it (CON-13)', async () => {
    const { user } = renderConsole('/settings/tokens');
    await signIn(user);
    await user.type(await screen.findByLabelText('Name'), 'laptop');
    await user.click(screen.getByRole('button', { name: 'Create token' }));
    await screen.findByText(/won't see this token again/);
    const block = screen.getByRole('status', { name: 'New token' });
    const secret = within(block).getAllByText(/^hc_pat_/)[0]!.textContent ?? '';
    expect(within(block).getByRole('button', { name: 'Done' })).toBeDisabled();
    await user.click(within(block).getByLabelText("I've copied it"));
    await user.click(within(block).getByRole('button', { name: 'Done' }));
    const row = (await screen.findByText('laptop')).closest('tr') as HTMLElement;
    expect(row.textContent).not.toContain(secret);
    await user.click(within(row).getByRole('button', { name: 'Revoke' }));
    await user.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Revoke token' }));
    await waitFor(() => expect(screen.queryByText('laptop')).not.toBeInTheDocument());
  });

  it('redirects to sign-in and back, and signs out (CON-03, CON-12)', async () => {
    const { user, router } = renderConsole('/caches');
    expect(await screen.findByRole('heading', { name: 'Sign in to the console' })).toBeInTheDocument();
    await signIn(user);
    expect(await screen.findByRole('region', { name: 'Caches list' })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/caches');
    await user.click(screen.getByRole('button', { name: 'Sign out' }));
    expect(await screen.findByRole('heading', { name: 'Sign in to the console' })).toBeInTheDocument();
  });
});
