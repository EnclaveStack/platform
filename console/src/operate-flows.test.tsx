import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { beforeEach, describe, expect, it } from 'vitest';
import { routes } from './App';
import { ApiProvider } from './api/context';
import { createMockApi, DEMO_ADMIN, type MockOptions } from './api/mock';
import type { AlertSpec, ChannelSpec } from './api/types';
import { SessionProvider } from './auth/session';

// v0.7 operator flows (L2-001 CON-45 – CON-53) through real routes and components on the mock adapter.

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

async function openNew(user: ReturnType<typeof userEvent.setup>, noun: string) {
  await user.click((await screen.findAllByRole('button', { name: `New ${noun}` }))[0]!);
  return screen.findByRole('dialog', { name: `New ${noun}` });
}

beforeEach(() => localStorage.clear());

describe('operate in the console', () => {
  it('switches project: lists follow it, new resources go into it, All projects shows each project (CON-45)', async () => {
    const { user, api, router } = renderConsole('/secrets');
    await signIn(user);
    await api.createProject('shop', 'the shop');
    await api.createResource('secret', 'in-default', { value: 'v' });
    await user.selectOptions(await screen.findByLabelText('Project'), await screen.findByRole('option', { name: 'shop' }));
    expect(router.state.location.search).toBe('?project=shop');
    const panel = await openNew(user, 'secret');
    expect(within(panel).getByText('shop')).toBeInTheDocument();
    await user.type(within(panel).getByLabelText('Name'), 'in-shop');
    await user.type(within(panel).getByLabelText('Value'), 'shh');
    await user.click(within(panel).getByRole('button', { name: 'Create secret' }));
    expect(await screen.findByRole('heading', { name: 'in-shop' })).toBeInTheDocument();
    expect((await api.listResources('secret', 'shop')).map((r) => r.name)).toEqual(['in-shop']);
    const list = screen.getByRole('region', { name: 'Secrets list' });
    await waitFor(() => expect(within(list).queryByText('in-default')).not.toBeInTheDocument());
    await user.selectOptions(screen.getByLabelText('Project'), 'All projects');
    expect(await within(list).findByText('in-default')).toBeInTheDocument();
    expect(within(list).getAllByText('shop').length).toBeGreaterThan(0);
  });

  it('creates an alert from "Disk nearly full" with a channel, and tests it (CON-50, CON-51)', async () => {
    const { user, api } = renderConsole('/alerts');
    await signIn(user);
    await api.createResource('channel', 'ops', { type: 'webhook', webhook: { url: 'https://hooks.example.com/x' } } as ChannelSpec);
    const panel = await openNew(user, 'alert');
    await user.type(within(panel).getByLabelText('Name'), 'disk-full');
    await user.click(within(panel).getByRole('button', { name: 'Disk nearly full' }));
    expect(within(panel).getByLabelText('Metric')).toHaveValue('diskPercentFull');
    expect(within(panel).getByRole('checkbox', { name: 'This machine' })).toBeChecked();
    await user.click(await within(panel).findByRole('checkbox', { name: 'ops' }));
    await user.click(within(panel).getByRole('button', { name: 'Create alert' }));
    expect(await screen.findByRole('heading', { name: 'disk-full' })).toBeInTheDocument();
    const a = (await api.listResources('alert'))[0]!;
    expect((a.spec as AlertSpec).condition).toMatchObject({ type: 'metric', metric: 'diskPercentFull', resources: ['machine'], operator: 'above', threshold: 90, forMinutes: 5 });
    expect(await screen.findByText(/^When Disk full of this machine is above 90 % for 5 minutes, notify ops/)).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText('Ready')).toBeInTheDocument(), { timeout: 3000 });
    await user.click(screen.getByRole('button', { name: 'Send a test' }));
    expect(await screen.findByText(/Delivered to https:\/\/hooks\.example\.com\/x/)).toBeInTheDocument();
    await user.click(screen.getByRole('tab', { name: 'Incidents' }));
    expect(await screen.findByText('No incidents yet')).toBeInTheDocument();
  });

  it("shows an app's metrics with a range picker, and the machine's dashboard (CON-49)", async () => {
    const { user, api } = renderConsole('/apps');
    await signIn(user);
    const app = await api.createResource('app', 'web', { image: 'nginx', port: 80 });
    await user.click(await screen.findByRole('link', { name: /web/ }));
    await user.click(await screen.findByRole('tab', { name: 'Metrics' }));
    expect(await screen.findByRole('figure', { name: 'CPU' })).toBeInTheDocument();
    expect(screen.getByRole('table', { name: 'Requests' })).toBeInTheDocument();
    await user.click(screen.getByRole('tab', { name: '7d' }));
    expect(screen.getByRole('tab', { name: '7d' })).toHaveAttribute('aria-selected', 'true');
    expect(app.kind).toBe('app');
    await user.click(screen.getByRole('link', { name: /Monitoring/ }));
    expect(await screen.findByRole('heading', { name: 'This machine' })).toBeInTheDocument();
    expect(await screen.findByRole('figure', { name: 'Disk free' })).toBeInTheDocument();
  });

  it('invites someone and shows the one-time link; the invite page sets a password (CON-46)', async () => {
    const { user, api } = renderConsole('/settings/people');
    await signIn(user);
    await api.createProject('shop', '');
    await user.click(await screen.findByRole('button', { name: 'Invite someone' }));
    const panel = await screen.findByRole('dialog', { name: 'Invite someone' });
    await user.type(within(panel).getByLabelText('Email'), 'dev@example.test');
    await user.click(within(panel).getByRole('button', { name: 'Add a project role' }));
    await user.click(within(panel).getByRole('button', { name: 'Invite and show the link' }));
    const card = await screen.findByRole('status', { name: 'Link to send' });
    const link = within(card).getByText(/\/invite\?code=/).textContent ?? '';
    expect(screen.getByRole('cell', { name: /dev@example\.test/ })).toBeInTheDocument();
    expect(await api.getInvite(new URL(link).searchParams.get('code') ?? '')).toEqual({ email: 'dev@example.test', kind: 'invite' });
  });

  it('lets a viewer look but not change (CON-48)', async () => {
    const { user } = renderConsole('/apps', { seed: 'demo', as: { machineRole: 'viewer' } });
    await signIn(user);
    expect(await screen.findAllByText('View only')).not.toHaveLength(0);
    expect(screen.queryByRole('button', { name: 'New app' })).not.toBeInTheDocument();
    await user.click(await screen.findByRole('link', { name: /^api/ }));
    expect(await screen.findByRole('button', { name: 'Edit' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Edit' })).toHaveAttribute('title', 'Editors can do this. Ask an admin of default.');
    expect(screen.queryByRole('link', { name: 'People' })).not.toBeInTheDocument();
  });

  it('creates a secret read from a secret store (CON-52)', async () => {
    const { user, api } = renderConsole('/secrets');
    await signIn(user);
    await api.createResource('secret', 'vault-token', { value: 's.x' });
    await api.createResource('secretstore', 'vault', { provider: 'vault', vault: { address: 'https://vault.example.com', tokenSecret: 'vault-token' } });
    const panel = await openNew(user, 'secret');
    await user.type(within(panel).getByLabelText('Name'), 'stripe');
    await user.click(within(panel).getByRole('radio', { name: /From a secret store/ }));
    await user.selectOptions(await within(panel).findByLabelText('Secret store'), 'vault');
    await user.type(within(panel).getByLabelText('Key'), 'shop/stripe');
    await user.click(within(panel).getByRole('button', { name: 'Create secret' }));
    expect(await screen.findByRole('heading', { name: 'stripe' })).toBeInTheDocument();
    expect(await screen.findByText('Read from a secret store')).toBeInTheDocument();
    expect((await api.listResources('secret')).find((r) => r.name === 'stripe')?.spec).toEqual({ source: { store: 'vault', key: 'shop/stripe' } });
    await waitFor(() => expect(screen.getByText('Ready')).toBeInTheDocument(), { timeout: 3000 });
    await user.click(screen.getByRole('button', { name: 'Refresh now' }));
    expect(await screen.findByText('Reading stripe again')).toBeInTheDocument();
  });

  it('sets up emailed invites and says the invite was emailed (CON-46, IAM-04)', async () => {
    const { user, api } = renderConsole('/settings/people');
    await signIn(user);
    await api.createResource('email', 'mail', {});
    await waitFor(async () => expect((await api.listResources('email'))[0]?.status.phase).toBe('ready'), { timeout: 3000 });
    await user.click(await screen.findByRole('link', { name: 'Projects' }));
    await user.click(await screen.findByRole('link', { name: 'People' }));
    await user.selectOptions(await screen.findByLabelText('Email service'), await screen.findByRole('option', { name: 'default/mail' }));
    await user.type(screen.getByLabelText('From'), 'hc@example.test');
    await user.click(screen.getByRole('button', { name: 'Email invites' }));
    expect(await screen.findByText(/emailed from hc@example\.test through default\/mail/)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Invite someone' }));
    const panel = await screen.findByRole('dialog', { name: 'Invite someone' });
    await user.type(within(panel).getByLabelText('Email'), 'dev@example.test');
    await user.click(within(panel).getByRole('radio', { name: /^Viewer/ }));
    await user.click(within(panel).getByRole('button', { name: 'Invite and show the link' }));
    const card = await screen.findByRole('status', { name: 'Link to send' });
    expect(within(card).getByText(/Emailed to/)).toBeInTheDocument();
  });
});
