import { screen, waitFor, within } from '@testing-library/react';
import { beforeEach, describe, expect, it } from 'vitest';
import type { AlertSpec, ChannelSpec, StackSpec } from './api/types';
import { MAIN } from './api/mock-machines';
import { renderConsole, signIn } from './test-console';

// v0.8 flows (L2-001 CON-54 – CON-60) through real routes and components on the mock adapter.



const until = async (check: () => Promise<boolean>) => waitFor(async () => expect(await check()).toBe(true), { timeout: 4000 });

beforeEach(() => localStorage.clear());

describe('many machines in the console', () => {
  it('adds a machine, shows its join command, and shows it online once it joins (CON-54)', async () => {
    const { user, api } = renderConsole('/machines', { joinAfterMs: 200 });
    await signIn(user);
    const list = await screen.findByRole('region', { name: 'Machines list' });
    expect(await within(list).findByText(new RegExp(`^${MAIN.name}`))).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Add machine' }));
    const drawer = await screen.findByRole('dialog', { name: 'Add machine' });
    await user.type(within(drawer).getByLabelText('Name'), 'Bad Name');
    await user.click(within(drawer).getByRole('button', { name: 'Add and show the join command' }));
    expect(await within(drawer).findByText(/Use 3–40 characters|lowercase/i)).toBeInTheDocument();
    await user.clear(within(drawer).getByLabelText('Name'));
    await user.type(within(drawer).getByLabelText('Name'), 'rack-1');
    await user.click(within(drawer).getByRole('button', { name: 'Add and show the join command' }));
    const card = await screen.findByRole('status', { name: 'Join command' });
    expect(within(card).getByText(/enclave agent join .* --code /)).toBeInTheDocument();
    expect((await api.getMachineByName('rack-1')).state).toBe('waiting');
    await until(async () => (await api.getMachineByName('rack-1')).state === 'online');
    await user.click(within(card).getByRole('button', { name: 'Done' }));
    expect(await screen.findByRole('heading', { name: 'rack-1' })).toBeInTheDocument();
    // Back to the list, which shows both machines online (CON-54, CON-64).
    await user.click(screen.getByRole('link', { name: /All machines/ }));
    const machines = await screen.findByRole('region', { name: 'Machines list' });
    await waitFor(() => expect(within(machines).getAllByText('Online')).toHaveLength(2), { timeout: 7000 });
  }, 15000);

  it('shows the machine ID in grey and lets an admin rename the main machine without changing its ID (MCH-10)', async () => {
    const { user, api } = renderConsole(`/machines/${MAIN.name}`);
    await signIn(user);
    const header = await screen.findByRole('group', { name: 'Summary' });
    expect(within(header).getByTitle("The machine's ID")).toHaveTextContent(MAIN.id);
    await user.click(within(header).getByRole('button', { name: 'Rename' }));
    const drawer = await screen.findByRole('dialog', { name: 'Rename machine' });
    const field = within(drawer).getByLabelText('Display name');
    expect(field).toHaveValue('This Mac');
    await user.clear(field);
    await user.click(within(drawer).getByRole('button', { name: 'Save' }));
    expect(await within(drawer).findByText('Use 1–64 characters.')).toBeInTheDocument();
    await user.type(field, 'Home lab');
    await user.click(within(drawer).getByRole('button', { name: 'Save' }));
    expect(await screen.findByRole('heading', { name: 'Home lab', level: 1 })).toBeInTheDocument();
    const main = await api.getMachineByName(MAIN.name);
    expect([main.title, main.id, main.name]).toEqual(['Home lab', MAIN.id, MAIN.name]);
    // The sidebar and the breadcrumb follow the new name.
    await waitFor(() => expect(within(screen.getByRole('navigation', { name: 'Breadcrumb' })).getByText('Home lab')).toBeInTheDocument(), { timeout: 4000 });
  });

  it('offers no rename on a machine that has no display name of its own', async () => {
    const { user, api } = renderConsole('/machines');
    await signIn(user);
    await api.addMachine('rack-1', {});
    await user.click(await screen.findByRole('link', { name: 'rack-1' }));
    const header = await screen.findByRole('group', { name: 'Summary' });
    expect(within(header).queryByRole('button', { name: 'Rename' })).not.toBeInTheDocument();
    await expect(api.updateMachineByName('rack-1', { title: 'Rack' })).rejects.toMatchObject({ code: 'VALIDATION_FAILED' });
  });

  it('places a new database on a chosen machine and moves it (CON-56, CON-57)', async () => {
    const { user, api } = renderConsole('/databases', { machines: [{ name: 'rack-1', labels: { disk: 'ssd' } }] });
    await signIn(user);
    await user.click((await screen.findAllByRole('button', { name: 'New database' }))[0]!);
    const panel = await screen.findByRole('dialog', { name: 'New database' });
    await user.type(within(panel).getByLabelText('Name'), 'orders');
    const runOn = await within(panel).findByLabelText('Run on');
    expect(within(runOn).getByRole('option', { name: 'Choose for me' })).toBeInTheDocument();
    await user.selectOptions(runOn, within(runOn).getByRole('option', { name: /^rack-1/ }));
    expect(within(panel).queryByText('Required labels')).not.toBeInTheDocument(); // labels only while EnclaveStack chooses
    await user.click(within(panel).getByRole('button', { name: 'Create database' }));
    await until(async () => (await api.listResources('database')).some((r) => r.name === 'orders' && r.outputs.machine === 'rack-1'));
    expect(await screen.findByRole('link', { name: 'rack-1' })).toBeInTheDocument();

    await user.click(await screen.findByRole('button', { name: 'Move' }));
    const dialog = await screen.findByRole('alertdialog', { name: 'Move orders to another machine' });
    expect(within(dialog).getByText(/its data is copied/)).toBeInTheDocument();
    await user.selectOptions(within(dialog).getByLabelText('From rack-1 to'), within(dialog).getByRole('option', { name: new RegExp(`^${MAIN.name}`) }));
    await user.click(within(dialog).getByRole('button', { name: 'Move' }));
    await until(async () => (await api.listResources('database')).some((r) => r.name === 'orders' && r.outputs.machine === MAIN.name && r.status.phase === 'ready'));
    const orders = (await api.listResources('database')).find((r) => r.name === 'orders');
    expect((await api.getEvents(orders?.id ?? '')).map((e) => e.type)).toEqual(expect.arrayContaining(['move_started', 'moved']));
  }, 15000);

  it('shows that a resource is on an offline machine, and refuses to move its data (FAIL-01, CON-56, CON-57)', async () => {
    const { user, api, router } = renderConsole('/caches', { machines: [{ name: 'rack-1', offlineAfterMs: 400 }] });
    await signIn(user);
    const c = await api.createResource('cache', 'sessions', { engine: 'redis', version: '7', maxMemoryMiB: 256, machine: 'rack-1' } as never);
    await until(async () => (await api.getMachineByName('rack-1')).state === 'offline');
    await router.navigate(`/caches/${c.id}`);
    expect(await screen.findByText(/rack-1 is offline since .*Its data is there: it waits for the machine to come back/)).toBeInTheDocument();
    await user.click(await screen.findByRole('button', { name: 'Move' }));
    const dialog = await screen.findByRole('alertdialog', { name: 'Move sessions to another machine' });
    expect(await within(dialog).findByText(/rack-1 is offline, so the data of sessions can't be read/)).toBeInTheDocument();
    await user.selectOptions(within(dialog).getByLabelText('From rack-1 to'), within(dialog).getByRole('option', { name: new RegExp(`^${MAIN.name}`) }));
    await user.click(within(dialog).getByRole('button', { name: 'Move' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('Restore a backup to a new cache instead.');
  }, 15000);

  it('drains a machine: what is not pinned moves, what is pinned stays (CON-55)', async () => {
    const { user, api } = renderConsole('/machines/rack-1', { machines: [{ name: 'rack-1', labels: { zone: 'a' } }, { name: 'rack-2', labels: { zone: 'a' } }] });
    await signIn(user);
    await api.createResource('cache', 'free', { engine: 'redis', version: '7', maxMemoryMiB: 256, machineLabels: { zone: 'a' } } as never);
    await api.createResource('database', 'pinned', { engine: 'postgres', version: '16', machine: 'rack-1' } as never);
    await until(async () => (await api.listResources()).filter((r) => r.outputs.machine === 'rack-1').length === 2);
    await user.click(await screen.findByRole('button', { name: 'Drain' }));
    await user.click(within(await screen.findByRole('alertdialog', { name: 'Drain rack-1?' })).getByRole('button', { name: 'Drain' }));
    expect(await screen.findByText(/Pinned here, so not moved: database\/pinned/)).toBeInTheDocument();
    expect(screen.getAllByText('Not schedulable').length).toBeGreaterThan(0);
    await until(async () => (await api.listResources('cache')).some((r) => r.outputs.machine === 'rack-2'));
    await user.click(screen.getByRole('button', { name: 'Remove' }));
    await user.click(within(await screen.findByRole('alertdialog', { name: 'Remove rack-1?' })).getByRole('button', { name: 'Remove' }));
    expect(await screen.findByText(/rack-1 still runs 1 resources/)).toBeInTheDocument();
  }, 15000);

  it('alerts when a machine goes offline (CON-58)', async () => {
    const { user, api } = renderConsole('/alerts', { machines: [{ name: 'rack-1' }] });
    await signIn(user);
    await api.createResource('channel', 'ops', { type: 'webhook', webhook: { url: 'https://hooks.example.com/x' } } as ChannelSpec);
    await user.click((await screen.findAllByRole('button', { name: 'New alert' }))[0]!);
    const panel = await screen.findByRole('dialog', { name: 'New alert' });
    await user.type(within(panel).getByLabelText('Name'), 'rack-down');
    await user.click(within(panel).getByRole('radio', { name: /A machine goes offline/ }));
    expect(within(panel).queryByRole('checkbox', { name: MAIN.name })).not.toBeInTheDocument();
    await user.click(await within(panel).findByRole('checkbox', { name: 'rack-1' }));
    await user.click(await within(panel).findByRole('checkbox', { name: 'ops' }));
    await user.click(within(panel).getByRole('button', { name: 'Create alert' }));
    await until(async () => (await api.listResources('alert')).some((r) => r.name === 'rack-down'));
    const a = (await api.listResources('alert')).find((r) => r.name === 'rack-down');
    expect((a?.spec as AlertSpec).condition).toMatchObject({ type: 'machine_offline', resources: ['machine/rack-1'], forMinutes: 1 });
  });
});

describe('the catalogue and stacks in the console', () => {
  it('installs WordPress from the catalogue, shows its members, and a member says it is part of the stack (CON-59, CON-60)', async () => {
    const { user, api } = renderConsole('/catalogue');
    await signIn(user);
    const card = (await screen.findByRole('heading', { name: 'WordPress' })).closest('article');
    expect(card).not.toBeNull();
    await user.click(within(card as HTMLElement).getByRole('button', { name: 'Install' }));
    const drawer = await screen.findByRole('dialog', { name: 'Install WordPress' });
    await user.type(within(drawer).getByLabelText('Name'), 'blog');
    await user.type(within(drawer).getByLabelText('Domain (optional)'), 'not a host');
    await user.click(within(drawer).getByRole('button', { name: 'Install WordPress' }));
    expect(await within(drawer).findByText('Domain is a host name, like blog.example.com.')).toBeInTheDocument();
    await user.clear(within(drawer).getByLabelText('Domain (optional)'));
    await user.type(within(drawer).getByLabelText('Domain (optional)'), 'blog.localhost');
    expect(within(drawer).getByText('database blog-db')).toBeInTheDocument();
    await user.click(within(drawer).getByRole('button', { name: 'Install WordPress' }));
    expect(await screen.findByRole('heading', { name: 'blog' })).toBeInTheDocument();
    await until(async () => (await api.listResources('stack')).some((r) => r.status.phase === 'ready'));
    const members = await screen.findByRole('heading', { name: 'Members' });
    const membersCard = members.parentElement?.parentElement as HTMLElement;
    expect(await within(membersCard).findByRole('link', { name: 'blog-db' })).toBeInTheDocument();
    expect(screen.getByText('https://blog.localhost')).toBeInTheDocument();

    await user.click(within(membersCard).getByRole('link', { name: 'blog-db' }));
    expect(await screen.findByText('Part of stack blog')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Delete' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Edit' })).not.toBeInTheDocument();
  }, 15000);

  it('edits a stack\'s parameters and deletes it with its members (CON-60)', async () => {
    const { user, api, router } = renderConsole('/stacks');
    await signIn(user);
    const st = await api.createResource('stack', 'blog', { template: 'wordpress', parameters: { domain: 'blog.localhost' } } as StackSpec);
    await until(async () => (await api.getResource(st.id)).status.phase === 'ready');
    await router.navigate(`/stacks/${st.id}`);
    await user.click(await screen.findByRole('button', { name: 'Edit parameters' }));
    const drawer = await screen.findByRole('dialog', { name: 'Parameters of blog' });
    await user.clear(await within(drawer).findByLabelText('Domain (optional)'));
    await user.click(within(drawer).getByRole('button', { name: 'Save parameters' }));
    await until(async () => !(await api.listResources('domain')).some((d) => d.name === 'blog-site' && d.status.phase !== 'deleting'));

    await user.click(await screen.findByRole('button', { name: 'Delete' }));
    const dialog = await screen.findByRole('alertdialog', { name: 'Delete stack blog?' });
    expect(within(dialog).getByText(/database blog-db/)).toBeInTheDocument();
    await user.type(within(dialog).getByLabelText(/to confirm/), 'blog');
    await user.click(within(dialog).getByRole('button', { name: 'Delete stack' }));
    await until(async () => (await api.listResources()).filter((r) => r.name.startsWith('blog')).length === 0);
  }, 15000);

  it('adds a template of its own, and refuses a broken one with its line (CON-59)', async () => {
    const { user } = renderConsole('/catalogue');
    await signIn(user);
    await user.click(await screen.findByRole('button', { name: 'Add template' }));
    const drawer = await screen.findByRole('dialog', { name: 'Add template' });
    const body = within(drawer).getByLabelText('Template');
    await user.click(body);
    await user.paste('name: tiny\ntitle: Tiny\nversion: 1\nresources:\n  - kind: app\n    name: ""\n');
    await user.click(within(drawer).getByRole('button', { name: 'Add to the catalogue' }));
    expect(await within(drawer).findByText(/line 3: version/)).toBeInTheDocument();
    await user.clear(body);
    await user.click(body);
    await user.paste('name: tiny\ntitle: Tiny\nversion: 1.0.0\nresources:\n  - kind: app\n    name: ""\n');
    await user.click(within(drawer).getByRole('button', { name: 'Add to the catalogue' }));
    const card = (await screen.findByRole('heading', { name: 'Tiny' })).closest('article') as HTMLElement;
    expect(within(card).getByText('Yours')).toBeInTheDocument();
  });
});
