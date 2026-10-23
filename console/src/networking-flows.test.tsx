import { screen, waitFor, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { renderConsole, signIn } from './test-console';

// v0.4 operator flows (L2-001 CON-26 – CON-31) through real routes and components on the mock adapter.



describe('networking in the console', () => {
  it('creates a network with a subnet, checking the ranges as you type (CON-26)', async () => {
    const { user } = renderConsole('/networks');
    await signIn(user);
    await user.click((await screen.findAllByRole('button', { name: 'New network' }))[0]!);
    const panel = await screen.findByRole('dialog', { name: 'New network' });
    await user.type(within(panel).getByLabelText('Name'), 'backend');
    await user.type(within(panel).getByLabelText('Address range'), '10.40.0.0/16');
    await user.tab();
    expect(within(panel).getByText(/65,536 addresses/)).toBeInTheDocument();
    await user.click(within(panel).getByRole('button', { name: 'Add subnet' }));
    await user.type(within(panel).getByLabelText('Subnets name 1'), 'data');
    await user.type(within(panel).getByLabelText('Subnets value 1'), '10.41.1.0/24');
    await user.click(within(panel).getByRole('button', { name: 'Create network' }));
    expect(within(panel).getByText(/must lie inside/)).toBeInTheDocument();
    await user.clear(within(panel).getByLabelText('Subnets value 1'));
    await user.type(within(panel).getByLabelText('Subnets value 1'), '10.40.1.0/24');
    await user.click(within(panel).getByRole('button', { name: 'Create network' }));
    expect(await screen.findByRole('heading', { name: 'backend' })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText('Ready')).toBeInTheDocument(), { timeout: 3000 });
    expect(screen.getByText('es-net-backend')).toBeInTheDocument();
    expect(screen.getByText('data 10.40.1.0/24')).toBeInTheDocument();
  });

  it('places a database in a subnet and protects it with a firewall rule read as a sentence (CON-26, CON-27)', async () => {
    const { user, api } = renderConsole('/databases');
    await signIn(user);
    await api.createResource('network', 'backend', { cidr: '10.40.0.0/16', subnets: [{ name: 'apps', cidr: '10.40.2.0/24' }, { name: 'data', cidr: '10.40.1.0/24' }] });
    await api.createResource('app', 'api', { image: 'nginx:1.27', port: 8080, network: 'backend' });
    await user.click((await screen.findAllByRole('button', { name: 'New database' }))[0]!);
    const panel = await screen.findByRole('dialog', { name: 'New database' });
    await user.type(within(panel).getByLabelText('Name'), 'orders');
    await user.selectOptions(within(panel).getByLabelText('Network'), 'backend');
    await user.selectOptions(within(panel).getByLabelText('Subnet'), 'data');
    await user.click(within(panel).getByRole('button', { name: 'Create database' }));
    await waitFor(() => expect(screen.getByText('Ready')).toBeInTheDocument(), { timeout: 3000 });
    expect(screen.getByText('backend · data')).toBeInTheDocument();
    expect(screen.getByText('orders.database.internal')).toBeInTheDocument();
    expect(screen.getByText(/Accepts everything/)).toBeInTheDocument();

    await api.createResource('firewall', 'db-access', { targets: ['database/orders'], ingress: [{ from: ['app/api'], protocol: 'tcp', ports: ['5432'] }] });
    await user.click(screen.getByRole('link', { name: /Firewalls/ }));
    // A section lists its resources; each opens on a page of its own (CON-64).
    await user.click(await screen.findByRole('link', { name: 'db-access' }));
    expect(await screen.findByRole('heading', { name: 'db-access' })).toBeInTheDocument();
    expect(await screen.findByText('Allow tcp 5432 from app/api')).toBeInTheDocument();
    expect(screen.getAllByText(/published host ports stay reachable/i).length).toBeGreaterThan(0);
  });

  it("lists a zone's records under it and adds one with the zone filled in (CON-29)", async () => {
    const { user, api } = renderConsole('/zones');
    await signIn(user);
    expect(screen.queryByRole('link', { name: /DNS records/ })).not.toBeInTheDocument();
    await api.createResource('secret', 'cf-token', { value: '{"apiToken":"t"}' });
    const zone = await api.createResource('zone', 'example', { provider: 'cloudflare', zone: 'example.com', credentialsSecret: 'cf-token' });
    await api.createResource('record', 'www', { zone: 'example', name: 'www', type: 'CNAME', values: ['web.example.net'] });
    await user.click(await screen.findByRole('link', { name: /example/ }));
    expect(await screen.findByRole('heading', { name: 'example' })).toBeInTheDocument();
    await user.click(screen.getByRole('tab', { name: 'Records' }));
    const table = await screen.findByRole('table', { name: 'Records in example.com' });
    expect(within(table).getByText('www.example.com')).toBeInTheDocument();
    expect(within(table).getByText('web.example.net')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Add record' }));
    const panel = await screen.findByRole('dialog', { name: 'New record' });
    expect(within(panel).getByLabelText('Zone')).toHaveValue('example');
    await user.type(within(panel).getByLabelText('Name'), 'apex');
    await user.type(within(panel).getByLabelText('Record name'), '@');
    await user.type(within(panel).getByLabelText('Values'), '203.0.113.7');
    await user.click(within(panel).getByRole('button', { name: 'Create record' }));
    expect(await screen.findByRole('heading', { name: 'apex' })).toBeInTheDocument();
    const records = await api.listResources('record');
    expect(records.map((x) => x.name).sort()).toEqual(['apex', 'www']);
    expect(zone.kind).toBe('zone');
  });
});
