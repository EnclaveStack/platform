import { screen, waitFor, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { renderConsole, signIn } from './test-console';

// Public access flows (L2-001 CON-76 – CON-80, L2.5-002 §21) through real routes and components on the mock adapter.

describe('public access in the console', () => {
  it('installs a quick link from the catalogue and shows its temporary address (CON-76, CON-80)', async () => {
    const { user, api } = renderConsole('/catalogue');
    await signIn(user);
    await api.createResource('app', 'web', { image: 'nginx:1.27-alpine', port: 80 });
    const group = await screen.findByRole('region', { name: 'Public access · An address for one app' });
    expect(screen.getByRole('region', { name: 'Public access · Your own domain' })).toBeInTheDocument();
    // A way in that needs an account links to its sign-up page.
    const tailscale = within(group).getByRole('article', { name: 'Tailscale' });
    expect(within(tailscale).getByRole('link', { name: /Sign up/ })).toHaveAttribute('href', 'https://login.tailscale.com/start');
    await user.click(within(within(group).getByRole('article', { name: 'Quick link' })).getByRole('button', { name: 'Install' }));

    const panel = await screen.findByRole('dialog', { name: 'New tunnel' });
    expect(within(panel).getByRole('radio', { name: /Quick link/ })).toBeChecked();
    await user.type(within(panel).getByLabelText('Name'), 'demo');
    await user.selectOptions(within(panel).getByLabelText('App'), 'web');
    await user.click(within(panel).getByRole('button', { name: 'Create tunnel' }));
    expect(await screen.findByRole('heading', { name: 'demo' })).toBeInTheDocument();
    const address = await screen.findByRole('region', { name: 'Public address' });
    await waitFor(() => expect(within(address).getByText(/\.trycloudflare\.com$/)).toBeInTheDocument(), { timeout: 3000 });
    expect(screen.getByText(/Temporary: this address changes/)).toBeInTheDocument();
    expect(screen.getAllByText('Connected').length).toBeGreaterThan(1); // the header chip and the Connection detail
    expect(screen.getByText(/Gate: Sign in/)).toBeInTheDocument();
  });

  it('makes an app public from its page and lists its public addresses (CON-80)', async () => {
    const { user, api, router } = renderConsole('/apps');
    await signIn(user);
    const app = await api.createResource('app', 'web', { image: 'nginx:1.27-alpine', port: 80 });
    await user.click(await screen.findByRole('link', { name: 'web' }));
    await user.click(await screen.findByRole('button', { name: 'Make public' }));
    const choose = await screen.findByRole('dialog', { name: 'Make web public' });
    expect(within(choose).getByText('No Cloudflare or own-server tunnel yet.')).toBeInTheDocument();
    await user.click(within(choose).getByRole('button', { name: 'Use Quick link' }));
    const panel = await screen.findByRole('dialog', { name: 'Make web public' });
    expect(within(panel).getByLabelText('App')).toHaveValue('web');
    await user.type(within(panel).getByLabelText('Name'), 'web-link');
    // Opening it to everyone asks first.
    await user.click(within(panel).getByRole('radio', { name: /Open to everyone/ }));
    const ask = await screen.findByRole('alertdialog', { name: 'Open it to everyone?' });
    await user.click(within(ask).getByRole('button', { name: 'Open it to everyone' }));
    expect(within(panel).getByRole('radio', { name: /Open to everyone/ })).toBeChecked();
    await user.click(within(panel).getByRole('button', { name: 'Create tunnel' }));
    expect(await screen.findByRole('heading', { name: 'web-link' })).toBeInTheDocument();
    await waitFor(async () => expect((await api.listResources('tunnel'))[0]?.status.phase).toBe('ready'), { timeout: 3000 });

    await router.navigate(`/apps/${app.id}`);
    const card = await screen.findByRole('region', { name: 'Public addresses' });
    expect(within(card).getAllByText(/trycloudflare\.com/).length).toBeGreaterThan(0);
    expect(within(card).getByText(/gate Open to everyone/)).toBeInTheDocument();
  });

  it('serves a domain from the internet through Cloudflare and shows its record (CON-77)', async () => {
    const { user, api } = renderConsole('/domains');
    await signIn(user);
    await api.createResource('app', 'web', { image: 'nginx:1.27-alpine', port: 80 });
    await api.createResource('secret', 'cf-token', { value: '{"apiToken":"t"}' });
    await api.createResource('zone', 'example', { provider: 'cloudflare', zone: 'example.com', credentialsSecret: 'cf-token' });
    await api.createResource('tunnel', 'home', { provider: 'cloudflare', credentialsSecret: 'cf-token', accountId: '0123456789abcdef0123456789abcdef' });
    await user.click((await screen.findAllByRole('button', { name: 'New domain' }))[0]!);
    const panel = await screen.findByRole('dialog', { name: 'New domain' });
    await user.type(within(panel).getByLabelText('Name'), 'stage');
    await user.type(within(panel).getByLabelText('Hostname'), 'stage.example.com');
    await user.selectOptions(within(panel).getByLabelText('App'), 'web');
    await user.click(within(panel).getByRole('radio', { name: /From the internet/ }));
    await user.selectOptions(within(panel).getByLabelText('Tunnel'), 'home');
    await user.selectOptions(within(panel).getByLabelText('Zone'), 'example');
    await user.click(within(panel).getByRole('button', { name: 'Create domain' }));
    expect(await screen.findByRole('heading', { name: 'stage' })).toBeInTheDocument();
    expect(screen.getByText('Public via home')).toBeInTheDocument();
    const card = await screen.findByRole('region', { name: 'Public access' });
    await waitFor(() => expect(within(card).getByText('Served by Cloudflare')).toBeInTheDocument(), { timeout: 3000 });
    expect(within(card).getByText(/CNAME stage\.example\.com → .+\.cfargotunnel\.com · Created/)).toBeInTheDocument();
    expect(within(card).getByText('Sign in')).toBeInTheDocument();
  });

  it('adds an edge machine with its join command and shows it relays only (CON-78)', async () => {
    const { user } = renderConsole('/machines');
    await signIn(user);
    await user.click(await screen.findByRole('button', { name: 'Add an edge machine' }));
    const panel = await screen.findByRole('dialog', { name: 'Add an edge machine' });
    await user.type(within(panel).getByLabelText('Name'), 'vps1');
    await user.type(within(panel).getByLabelText('Public address'), 'not an address!');
    await user.click(within(panel).getByRole('button', { name: 'Add and show the join command' }));
    expect(within(panel).getByText(/public IPv4 address or host name/)).toBeInTheDocument();
    await user.clear(within(panel).getByLabelText('Public address'));
    await user.type(within(panel).getByLabelText('Public address'), '203.0.113.9');
    await user.click(within(panel).getByRole('button', { name: 'Add and show the join command' }));
    const join = await screen.findByRole('status', { name: 'Join command' });
    expect(within(join).getByText(/enclave agent join --edge --code/)).toBeInTheDocument();
    expect(within(join).getByText(/80\/tcp and 443\/tcp/)).toBeInTheDocument();
    await user.click(within(join).getByRole('button', { name: 'Done' }));
    expect(await screen.findByRole('heading', { name: 'vps1' })).toBeInTheDocument();
    expect(screen.getByText('Edge')).toBeInTheDocument();
    expect(screen.getByText('Edge (only relays visitors)')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Pause placing' })).not.toBeInTheDocument();
    expect(await screen.findByRole('region', { name: 'Relays' })).toBeInTheDocument();
  });
});
