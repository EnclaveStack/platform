import { screen, waitFor, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import type { AppSpec, DatabaseSpec, FunctionSpec } from './api/types';
import { renderConsole, signIn } from './test-console';

// v0.10 flows (L2-016 CON-82 – CON-87) through real routes and components on the mock adapter.

describe('v0.10 in the console', () => {
  it('creates a function with a schedule, invokes it and shows the event in its run (CON-82)', async () => {
    const { user, api } = renderConsole('/functions');
    await signIn(user);
    await user.click((await screen.findAllByRole('button', { name: 'New function' }))[0]!);
    const panel = await screen.findByRole('dialog', { name: 'New function' });
    await user.type(within(panel).getByLabelText('Name'), 'thumbs');
    await user.type(within(panel).getByLabelText('Image'), 'alpine:3');
    await user.click(within(panel).getByRole('button', { name: 'Add trigger' }));
    await user.type(within(panel).getByLabelText('Schedule'), 'every day');
    await user.click(within(panel).getByRole('button', { name: 'Create function' }));
    expect(await within(panel).findByText(/Use 5 cron fields/)).toBeInTheDocument();
    await user.clear(within(panel).getByLabelText('Schedule'));
    await user.type(within(panel).getByLabelText('Schedule'), '@daily');
    await user.click(within(panel).getByRole('button', { name: 'Create function' }));

    expect(await screen.findByRole('heading', { name: 'thumbs' })).toBeInTheDocument();
    const fn = (await api.listResources('function'))[0]!;
    expect((fn.spec as FunctionSpec).triggers).toEqual([{ schedule: '@daily' }]);
    await user.click(await screen.findByRole('button', { name: 'Invoke' }, { timeout: 5000 }));
    const drawer = await screen.findByRole('dialog', { name: 'Invoke thumbs' });
    await user.click(within(drawer).getByRole('button', { name: 'Invoke' }));
    expect(await within(drawer).findByText('Succeeded', {}, { timeout: 3000 })).toBeInTheDocument();
    expect(within(drawer).getByText(/enclave function invoke thumbs --data/)).toBeInTheDocument();
  });

  it('turns previews on for a Git app and makes one from a branch (CON-84)', async () => {
    const { user, api } = renderConsole('/apps');
    await signIn(user);
    const app = await api.createResource('app', 'web', { source: { git: 'https://github.com/acme/web.git', ref: 'main' }, port: 8080 } as AppSpec);
    await user.click(await screen.findByRole('link', { name: /web/ }));
    await waitFor(() => expect(screen.getByText('Ready')).toBeInTheDocument(), { timeout: 5000 });
    await user.click(screen.getByRole('tab', { name: 'Previews' }));
    expect(await screen.findByText(/Previews are off/)).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Edit' }));
    const panel = await screen.findByRole('dialog', { name: 'Edit app web' });
    await user.click(within(panel).getByRole('checkbox', { name: 'Make a preview of each pull request' }));
    await user.type(within(panel).getByLabelText('Preview domain'), 'preview.localhost');
    await user.click(within(panel).getByRole('button', { name: 'Save changes' }));
    await waitFor(async () => expect(((await api.getResource(app.id)).spec as AppSpec).previews).toMatchObject({ enabled: true, domain: { suffix: 'preview.localhost' } }));

    await user.click(screen.getByRole('tab', { name: 'Previews' }));
    await user.type(await screen.findByLabelText('Branch or ref'), 'feature/login');
    await user.click(screen.getByRole('button', { name: 'Create preview' }));
    expect(await screen.findByText('web-feature-login')).toBeInTheDocument();
    expect(screen.getByText('https://web-feature-login.preview.localhost')).toBeInTheDocument();
  });

  it('makes a TLS database reachable from other computers and shows its CA (CON-86)', async () => {
    const { user, api } = renderConsole('/databases');
    await signIn(user);
    await user.click((await screen.findAllByRole('button', { name: 'New database' }))[0]!);
    const panel = await screen.findByRole('dialog', { name: 'New database' });
    await user.type(within(panel).getByLabelText('Name'), 'shop');
    const exposed = within(panel).getByRole('checkbox', { name: 'Reachable from other computers on this network' });
    expect(exposed).toBeDisabled();
    await user.click(within(panel).getByRole('checkbox', { name: 'Encrypted connections only (TLS)' }));
    await user.click(exposed);
    await user.click(within(panel).getByRole('button', { name: 'Create database' }));
    expect(await screen.findByRole('heading', { name: 'shop' })).toBeInTheDocument();
    expect(((await api.listResources('database'))[0]!.spec as DatabaseSpec)).toMatchObject({ tls: true, exposed: true });
    await waitFor(() => expect(screen.getByText('CA certificate')).toBeInTheDocument(), { timeout: 5000 });
    // This computer, and each address other computers use: both ask for TLS.
    expect(screen.getAllByText(/sslmode=require/).length).toBeGreaterThanOrEqual(2);
    expect(screen.getByText(/From other computers/)).toBeInTheDocument();
  });

  it('adds the standby and shows how it takes over (CON-85)', async () => {
    const { user } = renderConsole('/machines');
    await signIn(user);
    await user.click(await screen.findByRole('button', { name: 'Add a standby' }));
    const panel = await screen.findByRole('dialog', { name: 'Add a standby' });
    await user.type(within(panel).getByLabelText('Name'), 'spare');
    await user.click(within(panel).getByRole('button', { name: 'Add and show the join command' }));
    expect(await screen.findByText(/--standby --code/)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Done' }));
    expect(await screen.findByText('enclave standby promote')).toBeInTheDocument();
  });
});
