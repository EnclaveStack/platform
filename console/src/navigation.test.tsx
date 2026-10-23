import { cleanup, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { isNavPage, NAV_GROUPS, SECTIONS } from './lib/sections';
import { renderConsole, signIn } from './test-console';

// Console v4 navigation (L2-001 CON-61, CON-63, CON-64; L2.5-002 §16) through real routes on the mock adapter.

beforeEach(() => localStorage.clear());
afterEach(() => localStorage.clear());

describe('sidebar groups (CON-61)', () => {
  it('puts every section but records in exactly one group', () => {
    const grouped = NAV_GROUPS.flatMap((g) => g.items.filter((i) => !isNavPage(i)));
    const sections = SECTIONS.filter((s) => s.slug !== 'records').map((s) => s.slug);
    expect([...grouped].sort()).toEqual([...sections].sort());
    expect(new Set(grouped).size).toBe(grouped.length);
  });

  it('groups the sidebar, remembers a closed group, and keeps the current page’s group open', async () => {
    const { user } = renderConsole('/apps');
    await signIn(user);
    const data = await screen.findByRole('button', { name: 'Data' });
    expect(data).toHaveAttribute('aria-expanded', 'true');
    expect(within(screen.getByRole('navigation', { name: 'Compute' })).getByRole('link', { name: /^Apps/ })).toHaveAttribute('aria-current', 'page');
    await user.click(data);
    expect(data).toHaveAttribute('aria-expanded', 'false');
    expect(JSON.parse(localStorage.getItem('enclave.nav') ?? '{}')).toMatchObject({ closed: ['data'] });

    // A reload keeps it closed; opening a page in it opens it.
    cleanup();
    const again = renderConsole('/apps');
    await signIn(again.user);
    expect(await screen.findByRole('button', { name: 'Data' })).toHaveAttribute('aria-expanded', 'false');
    await again.router.navigate('/databases');
    await waitFor(() => expect(screen.getByRole('button', { name: 'Data' })).toHaveAttribute('aria-expanded', 'true'));
  });

  it('folds to a rail of icons and remembers it', async () => {
    const { user } = renderConsole('/apps');
    await signIn(user);
    await user.click(await screen.findByRole('button', { name: 'Fold the sidebar to icons' }));
    expect(screen.getByRole('button', { name: 'Expand the sidebar' })).toBeInTheDocument();
    // Labels stay for assistive technology, and links name themselves in a tooltip.
    expect(within(screen.getByRole('navigation', { name: 'Compute' })).getByRole('link', { name: /^Apps/ })).toHaveAttribute('title', 'Apps');
    expect(JSON.parse(localStorage.getItem('enclave.nav') ?? '{}')).toMatchObject({ rail: true });
  });
});

describe('full pages and breadcrumbs (CON-63, CON-64)', () => {
  it('lists a section full width and opens a resource on its own page, with every breadcrumb step a link', async () => {
    const { user, api, router } = renderConsole('/caches', { seed: 'demo' });
    await signIn(user);
    const list = await screen.findByRole('region', { name: 'Caches list' });
    await user.click(await within(list).findByRole('link', { name: 'sessions' }));
    expect(await screen.findByRole('heading', { name: 'sessions', level: 1 })).toBeInTheDocument();
    expect(screen.queryByRole('region', { name: 'Caches list' })).not.toBeInTheDocument();
    const cache = (await api.listResources('cache')).find((r) => r.name === 'sessions');
    expect(router.state.location.pathname).toBe(`/caches/${cache?.id ?? ''}`);

    const crumbs = screen.getByRole('navigation', { name: 'Breadcrumb' });
    expect(within(crumbs).getByText('sessions').closest('li')).toHaveAttribute('aria-current', 'page');
    expect(within(crumbs).getByRole('link', { name: 'All projects' })).toHaveAttribute('href', '/caches');
    expect(within(crumbs).getByRole('link', { name: 'Caches' })).toHaveAttribute('href', '/caches');
    await user.click(within(crumbs).getByRole('link', { name: 'Caches' }));
    expect(await screen.findByRole('region', { name: 'Caches list' })).toBeInTheDocument();

    // Back from a resource, and the machine step opens that machine's own page.
    await user.click(await within(screen.getByRole('region', { name: 'Caches list' })).findByRole('link', { name: 'sessions' }));
    await user.click(await screen.findByRole('link', { name: /All caches/ }));
    expect(router.state.location.pathname).toBe('/caches');
    await user.click(within(screen.getByRole('navigation', { name: 'Breadcrumb' })).getByRole('link', { name: 'This Mac' }));
    expect(router.state.location.pathname).toBe('/machines/main');
    expect(await screen.findByRole('heading', { name: 'This Mac', level: 1 })).toBeInTheDocument();
  });

  it('the sidebar machine card opens the machine\'s page', async () => {
    const { user, router } = renderConsole('/caches');
    await signIn(user);
    await user.click(await screen.findByRole('link', { name: /open its page/ }));
    expect(router.state.location.pathname).toBe('/machines/main');
  });

  it('opens a machine on its own page, with the breadcrumb back to Machines', async () => {
    const { user, router } = renderConsole('/machines');
    await signIn(user);
    const list = await screen.findByRole('region', { name: 'Machines list' });
    await user.click(await within(list).findByRole('link', { name: /^main/ }));
    // Named by the machine's own name; "main" is only what commands call it.
    expect(await screen.findByRole('heading', { name: 'This Mac', level: 1 })).toBeInTheDocument();
    expect(screen.getByText(/called main in commands/)).toBeInTheDocument();
    await user.click(within(screen.getByRole('navigation', { name: 'Breadcrumb' })).getByRole('link', { name: 'Machines' }));
    expect(router.state.location.pathname).toBe('/machines');
  });
});
