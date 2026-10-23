import { screen, waitFor, within } from '@testing-library/react';
import { beforeEach, describe, expect, it } from 'vitest';
import type { Resource } from './api/types';
import { needsAttention, summaryLine } from './routes/OverviewPage';
import { renderConsole, signIn } from './test-console';

// The overview (L2-001 CON-65, L2.5-002 §16) through real routes on the mock adapter.

// The chosen project is remembered in this browser; each test starts from All projects.
beforeEach(() => localStorage.clear());

describe('overview rules', () => {
  const r = (over: Partial<Resource>): Resource => ({
    id: 'x', kind: 'app', name: 'x', project: 'default', spec: {}, generation: 1, outputs: {}, ownerId: null, stack: null,
    status: { phase: 'ready', message: '', steps: [], observedGeneration: 1, lastTransitionAt: '', attempts: 0 }, createdAt: '', updatedAt: '', ...over,
  } as Resource);

  it('needs attention for failed resources and firing alerts only', () => {
    const failed = r({ id: 'a' });
    failed.status = { ...failed.status, phase: 'failed', message: 'x' };
    const firing = r({ id: 'b', kind: 'alert', outputs: { state: 'firing' } });
    const quiet = r({ id: 'c', kind: 'alert', outputs: { state: 'ok' } });
    expect(needsAttention([failed, firing, quiet, r({ id: 'd' })]).map((x) => x.id)).toEqual(['a', 'b']);
  });

  it('says in one line whether anything needs attention', () => {
    expect(summaryLine(0)).toBe('Everything is running.');
    expect(summaryLine(1)).toBe('1 resource needs attention.');
    expect(summaryLine(3)).toBe('3 resources need attention.');
  });
});

describe('the overview page (CON-65)', () => {
  it('opens at / with the summary, headline numbers and what is running, following the project', async () => {
    const { user, router } = renderConsole('/', { seed: 'demo' });
    await signIn(user);
    expect(router.state.location.pathname).toBe('/');
    const head = await screen.findByRole('group', { name: 'Overview' });
    expect(within(head).getByRole('heading', { level: 1 })).toHaveTextContent(/^Good (morning|afternoon|evening)$/);
    await waitFor(() => expect(within(head).getByText('Everything is running.')).toBeInTheDocument(), { timeout: 5000 });
    const tiles = screen.getByRole('group', { name: 'Headline numbers' });
    await waitFor(() => expect(within(tiles).getByRole('group', { name: 'Need attention' })).toHaveTextContent('0'));
    expect(within(tiles).getByRole('group', { name: 'Machines online' })).toHaveTextContent('2 of 2');
    expect(screen.getByText('Nothing needs attention')).toBeInTheDocument();
    const running = screen.getByRole('region', { name: "What's running" });
    expect(within(running).getByRole('link', { name: 'Databases 1' })).toHaveAttribute('href', '/databases');

    // The project picker narrows everything: shop has one app.
    await user.click(screen.getByRole('button', { name: /^Project:/ }));
    await user.click(await screen.findByRole('option', { name: 'shop' }));
    await waitFor(() => expect(within(screen.getByRole('region', { name: 'Recently changed' })).getAllByRole('link')).toHaveLength(1));
    expect(within(screen.getByRole('region', { name: 'Recently changed' })).getByRole('link', { name: 'storefront' })).toBeInTheDocument();
  });

  it('lists a failed resource under Needs attention, with Retry', async () => {
    const { user, api } = renderConsole('/');
    await signIn(user);
    const attention = await screen.findByRole('region', { name: 'Needs attention' });
    expect(await within(attention).findByText('Nothing needs attention', {}, { timeout: 5000 })).toBeInTheDocument();
    await api.createResource('app', 'fail-web', { image: 'nginx:1.27-alpine', port: 80 });
    expect(await within(attention).findByRole('link', { name: 'fail-web' }, { timeout: 15_000 })).toBeInTheDocument();
    expect(screen.getByRole('group', { name: 'Overview' })).toHaveTextContent('1 resource needs attention.');
    // The number counts up to its value.
    await waitFor(() => expect(within(screen.getByRole('group', { name: 'Need attention' })).getByText('1')).toBeInTheDocument());
    await user.click(within(attention).getByRole('button', { name: 'Retry' }));
    expect(await screen.findByText('Retrying fail-web')).toBeInTheDocument();
  }, 30_000);

  it('opens a create form from its quick actions, and offers none to a viewer', async () => {
    const { user, router } = renderConsole('/');
    await signIn(user);
    await user.click(within(await screen.findByRole('group', { name: 'Overview' })).getByRole('link', { name: 'New app' }));
    expect(router.state.location.pathname).toBe('/apps');
    expect(await screen.findByRole('dialog', { name: 'New app' })).toBeInTheDocument();
  });

  it('offers no create actions to a viewer', async () => {
    const { user } = renderConsole('/', { as: { machineRole: null, projectRoles: { default: 'viewer' } } });
    await signIn(user);
    expect(await screen.findByRole('group', { name: 'Overview' })).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'New app' })).not.toBeInTheDocument();
    expect(within(screen.getByRole('group', { name: 'Overview' })).getByRole('link', { name: /Catalogue/ })).toBeInTheDocument();
  });
});
