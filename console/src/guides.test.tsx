import { screen, waitFor, within } from '@testing-library/react';
import { beforeEach, describe, expect, it } from 'vitest';
import { findItems } from './lib/find';
import { GUIDES } from './lib/guides';
import { inSentence, SECTIONS, sectionBySlug } from './lib/sections';
import { renderConsole, signIn } from './test-console';

// Section summaries, guides and Find (L2-001 CON-62, CON-66, CON-67; L2.5-002 §16).

beforeEach(() => localStorage.clear());

// Other companies' products a guide must not compare EnclaveStack with. Services EnclaveStack connects to
// (an S3 bucket, Route 53, AWS Secrets Manager, Vault, Slack…) are named because they are integrations.
const COMPARISONS = /\b(Amplify|EC2|RDS|ElastiCache|Lambda|Heroku|Vercel|Netlify|Cloud Run|App Runner|ECS|Fargate|Azure|Google Cloud|DigitalOcean|Fly\.io|SQS|SNS|CloudFront|Kubernetes)\b|\b(like|similar to|just like|works like) (AWS|Amazon|Google|Microsoft)\b/i;

describe('guide content (CON-67)', () => {
  const slugs = SECTIONS.filter((s) => s.slug !== 'records').map((s) => s.slug);

  it('has a complete guide for every section', () => {
    expect(Object.keys(GUIDES).sort()).toEqual([...slugs].sort());
    for (const [slug, g] of Object.entries(GUIDES)) {
      expect(g.what.length, slug).toBeGreaterThan(0);
      expect(g.when.length, slug).toBeGreaterThanOrEqual(2);
      expect(g.how.length, slug).toBeGreaterThanOrEqual(3);
      expect(g.example.length, slug).toBeGreaterThan(0);
      expect(g.note.length, slug).toBeGreaterThan(0);
      expect(g.command, slug).toMatch(/^enclave [a-z]+ create [a-z0-9-]+ /);
      for (const r of g.related) {
        expect(sectionBySlug(r), `${slug} → ${r}`).toBeDefined();
        expect(r, slug).not.toBe(slug);
      }
    }
  });

  it('explains EnclaveStack on its own terms, without comparing it with other companies’ products', () => {
    for (const [slug, g] of Object.entries(GUIDES)) {
      const text = [...g.what, ...g.when, ...g.how, ...g.example, ...g.note].join(' ');
      expect(text, slug).not.toMatch(COMPARISONS);
    }
  });
});

describe('labels inside a sentence', () => {
  it('lowercases words but keeps abbreviations', () => {
    expect(inSentence('Load balancers')).toBe('load balancers');
    expect(inSentence('VMs')).toBe('VMs');
    expect(inSentence('CDN')).toBe('CDN');
    expect(inSentence('DNS')).toBe('DNS');
    expect(inSentence('Secret stores')).toBe('secret stores');
  });
});

describe('find (CON-62)', () => {
  const roles = { peopleAdmin: false, machineAdmin: false };

  it('finds pages and guides by name first, then resources, and nothing for an empty query', () => {
    expect(findItems('', [], roles)).toEqual([]);
    const items = findItems('data', [], roles);
    expect(items.map((i) => i.label)).toEqual(['Databases', 'Databases guide']);
    expect(items[0]?.id).toBe('/databases');
    expect(items[1]?.id).toBe('/guide/databases');
  });

  it('leaves out settings a role can’t open', () => {
    expect(findItems('audit', [], roles)).toEqual([]);
    expect(findItems('audit', [], { peopleAdmin: true, machineAdmin: true }).map((i) => i.id)).toEqual(['/settings/audit']);
  });

  it('opens with ⌘K, finds a resource and goes to it', async () => {
    const { user, router } = renderConsole('/apps', { seed: 'demo' });
    await signIn(user);
    await screen.findByRole('region', { name: 'Apps list' });
    await user.keyboard('{Meta>}k{/Meta}');
    const dialog = await screen.findByRole('dialog', { name: 'Find' });
    await user.type(within(dialog).getByRole('combobox', { name: 'Find a page or a resource' }), 'sess');
    const option = await within(dialog).findByRole('option', { name: /sessions/ });
    expect(option).toHaveAttribute('aria-selected', 'true');
    await user.keyboard('{Enter}');
    await waitFor(() => expect(router.state.location.pathname).toMatch(/^\/caches\/cache_/));
    expect(screen.queryByRole('dialog', { name: 'Find' })).not.toBeInTheDocument();
    expect(await screen.findByRole('heading', { name: 'sessions', level: 1 })).toBeInTheDocument();
  });

  it('opens from the top bar and closes with Escape', async () => {
    const { user } = renderConsole('/apps');
    await signIn(user);
    await user.click(await screen.findByRole('button', { name: /Find/ }));
    expect(await screen.findByRole('dialog', { name: 'Find' })).toBeInTheDocument();
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog', { name: 'Find' })).not.toBeInTheDocument();
  });
});

describe('section summaries and guide pages (CON-66, CON-67)', () => {
  it('sums a section up by state and opens its guide, with the five parts in order and the command', async () => {
    const { user, router } = renderConsole('/databases', { seed: 'demo' });
    await signIn(user);
    const summary = await screen.findByRole('group', { name: 'Databases by state' });
    await waitFor(() => expect(within(summary).getByRole('group', { name: 'Total' })).toHaveTextContent('1'));
    await user.click(within(screen.getByRole('group', { name: 'Databases' })).getByRole('link', { name: /Guide/ }));
    expect(router.state.location.pathname).toBe('/guide/databases');
    expect(await screen.findByRole('heading', { name: 'Getting started with databases', level: 1 })).toBeInTheDocument();
    const parts = screen.getAllByRole('region').map((r) => r.getAttribute('aria-labelledby') && within(r).getByRole('heading', { level: 2 }).textContent);
    expect(parts.filter(Boolean)).toEqual(['What it is', 'When to use it', 'How to use it', 'Example', 'Good to know']);
    expect(screen.getByText(GUIDES.databases.command)).toBeInTheDocument();
    const crumbs = screen.getByRole('navigation', { name: 'Breadcrumb' });
    expect(within(crumbs).getByRole('link', { name: 'Databases' })).toHaveAttribute('href', '/databases');
    await user.click(screen.getByRole('link', { name: 'Backups' }));
    expect(router.state.location.pathname).toBe('/guide/backups');
  });

  it('links each guide to its full guide on the website, in a new tab', async () => {
    const { user } = renderConsole('/guide/buckets');
    await signIn(user);
    const link = await screen.findByRole('link', { name: /Full buckets guide on enclavestack\.soupler\.com/ });
    expect(link).toHaveAttribute('href', 'https://enclavestack.soupler.com/services/buckets/');
    expect(link).toHaveAttribute('target', '_blank');
    expect(link).toHaveAttribute('rel', 'noreferrer');
  });

  it('sends a DNS record’s guide to the zones page on the website', async () => {
    const { user } = renderConsole('/guide/records');
    await signIn(user);
    expect(await screen.findByRole('link', { name: /Full DNS guide/ })).toHaveAttribute('href', 'https://enclavestack.soupler.com/services/zones/');
  });

  it('links an empty section to its guide', async () => {
    const { user } = renderConsole('/queues');
    await signIn(user);
    expect(await screen.findByText('No queues yet.')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Read the guide' })).toHaveAttribute('href', '/guide/queues');
  });
});
