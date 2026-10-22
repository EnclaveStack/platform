import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { describe, expect, it } from 'vitest';
import { routes } from './App';
import { ApiProvider } from './api/context';
import { createMockApi, DEMO_ADMIN, type MockOptions } from './api/mock';
import type { AppSpec, VMSpec } from './api/types';
import { SessionProvider } from './auth/session';

// v0.5 operator flows (L2-001 CON-32 – CON-36) through real routes and components on the mock adapter.

const KEY = 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEXTqZbAPryM1c8TBIUjg6rtRL1Dfr2uR7E954v/Icd4 test@enclave';

function renderConsole(path: string, opts: MockOptions = {}) {
  const api = createMockApi({ latencyMs: 0, stepMs: 30, storage: null, ...opts });
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

describe('VMs, volumes and snapshots in the console', () => {
  it('creates a VM from the catalog with a size preset and shows how to reach it (CON-32)', async () => {
    const { user, api } = renderConsole('/vms');
    await signIn(user);
    await user.click((await screen.findAllByRole('button', { name: 'New VM' }))[0]!);
    const panel = await screen.findByRole('dialog', { name: 'New VM' });
    await user.type(within(panel).getByLabelText('Name'), 'worker');
    await user.click(within(panel).getByRole('radio', { name: /Debian 12/ }));
    await user.click(within(panel).getByRole('radio', { name: /Medium/ }));
    expect(within(panel).getByLabelText('CPUs')).toHaveValue('2');
    expect(within(panel).getByLabelText('Memory (MiB)')).toHaveValue('2048');
    await user.click(within(panel).getByRole('button', { name: 'Create VM' }));
    expect(within(panel).getByText(/Add at least one SSH public key/)).toBeInTheDocument();
    await user.type(within(panel).getByLabelText('Public keys'), 'not a key');
    await user.click(within(panel).getByRole('button', { name: 'Create VM' }));
    expect(within(panel).getByText(/OpenSSH format/)).toBeInTheDocument();
    await user.clear(within(panel).getByLabelText('Public keys'));
    await user.type(within(panel).getByLabelText('Public keys'), KEY);
    await user.click(within(panel).getByRole('button', { name: 'Create VM' }));

    expect(await screen.findByRole('heading', { name: 'worker' })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText('Ready')).toBeInTheDocument(), { timeout: 3000 });
    expect(screen.getByText('enclave vm ssh worker')).toBeInTheDocument();
    expect(screen.getByText(/^ssh -p \d+ enclave@127\.0\.0\.1$/)).toBeInTheDocument();
    expect(screen.getByText('worker.vm.internal')).toBeInTheDocument();
    expect(screen.getByText('2 CPUs · 2 GiB')).toBeInTheDocument();
    const vm = (await api.listResources('vm'))[0]!;
    expect((vm.spec as VMSpec).image).toBe('debian-12');

    await user.click(screen.getByRole('tab', { name: 'Boot console' }));
    expect(await screen.findByText('worker login:')).toBeInTheDocument();
  });

  it('stops and starts a VM, and asks before an edit restarts it (CON-32, CON-33)', async () => {
    const { user, api } = renderConsole('/vms');
    await signIn(user);
    await api.createResource('vm', 'worker', { image: 'debian-12', sshKeys: [KEY] });
    await user.click(await screen.findByRole('link', { name: /worker/ }));
    await waitFor(() => expect(screen.getByText('Ready')).toBeInTheDocument(), { timeout: 3000 });

    await user.click(screen.getByRole('button', { name: 'Edit' }));
    let panel = await screen.findByRole('dialog', { name: 'Edit vm worker' });
    expect(within(panel).getByText(/can't change: create a new VM/)).toBeInTheDocument();
    await user.clear(within(panel).getByLabelText('Memory (MiB)'));
    await user.type(within(panel).getByLabelText('Memory (MiB)'), '2048');
    await user.click(within(panel).getByRole('button', { name: 'Save changes' }));
    const confirm = await screen.findByRole('alertdialog', { name: 'Restart worker?' });
    expect(within(confirm).getByText(/restart to apply the new memory/)).toBeInTheDocument();
    await user.click(within(confirm).getByRole('button', { name: 'Cancel' }));
    expect(((await api.listResources('vm'))[0]!.spec as VMSpec).memoryMiB).toBe(1024);
    await user.click(within(panel).getByRole('button', { name: 'Save changes' }));
    await user.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Save and restart' }));
    await waitFor(async () => expect(((await api.listResources('vm'))[0]!.spec as VMSpec).memoryMiB).toBe(2048));

    // Growing the root disk restarts nothing: no question.
    await waitFor(() => expect(screen.getByText('Ready')).toBeInTheDocument(), { timeout: 3000 });
    await user.click(screen.getByRole('button', { name: 'Edit' }));
    panel = await screen.findByRole('dialog', { name: 'Edit vm worker' });
    await user.clear(within(panel).getByLabelText('Root disk (GiB)'));
    await user.type(within(panel).getByLabelText('Root disk (GiB)'), '20');
    await user.click(within(panel).getByRole('button', { name: 'Save changes' }));
    await waitFor(async () => expect(((await api.listResources('vm'))[0]!.spec as VMSpec).diskGiB).toBe(20));
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();

    await waitFor(() => expect(screen.getByText('Ready')).toBeInTheDocument(), { timeout: 3000 });
    await user.click(screen.getByRole('button', { name: 'Stop' }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Start' })).toBeInTheDocument(), { timeout: 3000 });
    expect(((await api.listResources('vm'))[0]!.spec as VMSpec).power).toBe('stopped');
    await user.click(screen.getByRole('button', { name: 'Start' }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Stop' })).toBeInTheDocument(), { timeout: 3000 });
  });

  it('mounts a filesystem volume in an app, snapshots it and starts a new volume from the snapshot (CON-34, CON-35)', async () => {
    const { user, api } = renderConsole('/volumes');
    await signIn(user);
    await user.click((await screen.findAllByRole('button', { name: 'New volume' }))[0]!);
    let panel = await screen.findByRole('dialog', { name: 'New volume' });
    await user.type(within(panel).getByLabelText('Name'), 'uploads');
    await user.click(within(panel).getByRole('button', { name: 'Create volume' }));
    await waitFor(() => expect(screen.getByText('Ready')).toBeInTheDocument(), { timeout: 3000 });
    expect(screen.getByText('Filesystem (for apps and jobs)')).toBeInTheDocument();

    await user.click(screen.getByRole('link', { name: /Apps/ }));
    await user.click((await screen.findAllByRole('button', { name: 'New app' }))[0]!);
    panel = await screen.findByRole('dialog', { name: 'New app' });
    await user.type(within(panel).getByLabelText('Name'), 'web');
    await user.type(within(panel).getByLabelText('Image'), 'nginx:1.27');
    await user.click(within(panel).getByRole('button', { name: 'Add volume' }));
    await user.selectOptions(within(panel).getByLabelText('Volume 1'), 'uploads');
    await user.type(within(panel).getByLabelText('Volume 1 path'), 'data');
    await user.click(within(panel).getByRole('button', { name: 'Create app' }));
    expect(within(panel).getByText(/absolute path/)).toBeInTheDocument();
    await user.clear(within(panel).getByLabelText('Volume 1 path'));
    await user.type(within(panel).getByLabelText('Volume 1 path'), '/data');
    await user.click(within(panel).getByRole('button', { name: 'Create app' }));
    await waitFor(async () => expect(((await api.listResources('app'))[0]?.spec as AppSpec | undefined)?.volumes).toEqual([{ volume: 'uploads', path: '/data' }]));

    await user.click(screen.getByRole('link', { name: /Volumes/ }));
    await user.click(await screen.findByRole('link', { name: /uploads/ }));
    await user.click(await screen.findByRole('button', { name: 'Take snapshot' }));
    panel = await screen.findByRole('dialog', { name: 'Take snapshot' });
    expect(within(panel).getByLabelText('Copy')).toHaveValue('volume/uploads');
    await user.type(within(panel).getByLabelText('Name'), 'before');
    await user.click(within(panel).getByRole('button', { name: 'Create snapshot' }));
    expect(await screen.findByRole('heading', { name: 'before' })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText('Ready')).toBeInTheDocument(), { timeout: 3000 });
    expect(screen.queryByRole('button', { name: 'Launch VM' })).not.toBeInTheDocument(); // a filesystem snapshot

    await user.click(screen.getByRole('button', { name: 'Create volume' }));
    panel = await screen.findByRole('dialog', { name: 'New volume from snapshot' });
    expect(within(panel).getByLabelText('Start from a snapshot')).toHaveValue('before');
    await user.type(within(panel).getByLabelText('Name'), 'restored');
    await user.click(within(panel).getByRole('button', { name: 'Create volume' }));
    expect(await screen.findByRole('heading', { name: 'restored' })).toBeInTheDocument();
  });

  it('says what to install when VMs cannot run, and disables creating one (CON-36)', async () => {
    const { user } = renderConsole('/vms', { noVMs: true });
    await signIn(user);
    expect(await screen.findByText('VMs unavailable', { selector: '.es-text.es-weight-semibold' })).toBeInTheDocument();
    expect(screen.getByText('brew install qemu')).toBeInTheDocument();
    await user.click((await screen.findAllByRole('button', { name: 'New VM' }))[0]!);
    const panel = await screen.findByRole('dialog', { name: 'New VM' });
    await waitFor(() => expect(within(panel).getByRole('button', { name: 'Create VM' })).toBeDisabled());
  });
});
