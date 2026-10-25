import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { Breadcrumbs, Card, Finder, GuidePart, NavGroup, StatTile, useCountUp, type FinderItem } from './index';

// Console v4 building blocks (L2.5-001 §7, L2.5-002 §16).

function setMotion(reduced: boolean | null) {
  if (reduced === null) {
    // @ts-expect-error -- jsdom has no matchMedia unless a test gives it one
    delete window.matchMedia;
    return;
  }
  window.matchMedia = ((query: string) => ({ matches: reduced && query.includes('reduce'), media: query, addEventListener() {}, removeEventListener() {} })) as unknown as typeof window.matchMedia;
}

function Count({ to }: { to: number }) {
  return <span>{useCountUp(to)}</span>;
}

describe('StatTile and useCountUp', () => {
  afterEach(() => setMotion(null));

  it('shows the number at once when the system asks for reduced motion', () => {
    setMotion(true);
    render(<StatTile label="Running" value={13} note="of 14" />);
    expect(screen.getByRole('group', { name: 'Running' })).toHaveTextContent('Running13of 14');
  });

  it('counts up to the number with motion on, frame by frame', () => {
    setMotion(false);
    const frames: FrameRequestCallback[] = [];
    const raf = vi.spyOn(window, 'requestAnimationFrame').mockImplementation((cb) => frames.push(cb));
    render(<Count to={10} />);
    expect(screen.getByText('0')).toBeInTheDocument();
    const start = performance.now();
    act(() => frames.shift()?.(start + 300));
    const mid = Number(screen.getByText(/^\d+$/).textContent);
    expect(mid).toBeGreaterThan(0);
    expect(mid).toBeLessThan(10);
    act(() => frames.shift()?.(start + 1000));
    expect(screen.getByText('10')).toBeInTheDocument();
    raf.mockRestore();
  });
});

describe('Card, GuidePart, Breadcrumbs and NavGroup', () => {
  it('names a titled section card by its title', () => {
    render(<Card as="section" title="Needs attention">nothing</Card>);
    expect(screen.getByRole('region', { name: 'Needs attention' })).toHaveTextContent('nothing');
  });

  it('labels a guide part by its heading, so the colour is never the only cue', () => {
    render(<GuidePart tone="note" title="Good to know">Keep backups.</GuidePart>);
    expect(screen.getByRole('region', { name: 'Good to know' })).toHaveTextContent('Keep backups.');
  });

  it('marks the last breadcrumb step as the current page', () => {
    render(<Breadcrumbs><a href="/a">Apps</a><span>api</span></Breadcrumbs>);
    const nav = screen.getByRole('navigation', { name: 'Breadcrumb' });
    expect(nav.querySelector('[aria-current="page"]')).toHaveTextContent('api');
    expect(screen.getByRole('link', { name: 'Apps' })).toBeInTheDocument();
  });

  it('opens and closes a nav group from its heading', async () => {
    function Group() {
      const [open, setOpen] = useState(true);
      return <NavGroup heading="Data" open={open} onToggle={() => setOpen(!open)}><a href="/db">Databases</a></NavGroup>;
    }
    render(<Group />);
    const head = screen.getByRole('button', { name: 'Data' });
    expect(head).toHaveAttribute('aria-expanded', 'true');
    await userEvent.click(head);
    expect(head).toHaveAttribute('aria-expanded', 'false');
  });
});

describe('Finder', () => {
  const items: FinderItem[] = [{ id: '/databases', label: 'Databases', meta: 'Page' }, { id: '/guide/databases', label: 'Databases guide', meta: 'Guide' }];

  it('moves with the arrow keys and opens with Enter', async () => {
    const chosen: string[] = [];
    function Find() {
      const [q, setQ] = useState('');
      return <Finder query={q} onQuery={setQ} items={q ? items : []} onChoose={(i) => chosen.push(i.id)} onClose={() => {}} />;
    }
    render(<Find />);
    const box = screen.getByRole('combobox', { name: 'Find a page or a resource' });
    expect(screen.getByText('Type to find a page or a resource.')).toBeInTheDocument();
    await userEvent.type(box, 'data');
    expect(screen.getByRole('option', { name: 'Databases, Page' })).toHaveAttribute('aria-selected', 'true');
    await userEvent.keyboard('{ArrowDown}');
    expect(screen.getByRole('option', { name: 'Databases guide, Guide' })).toHaveAttribute('aria-selected', 'true');
    expect(box).toHaveAttribute('aria-activedescendant', screen.getByRole('option', { name: 'Databases guide, Guide' }).id);
    await userEvent.keyboard('{Enter}');
    expect(chosen).toEqual(['/guide/databases']);
  });

  it('closes with Escape and says when nothing matches', async () => {
    let closed = false;
    render(<Finder query="zzz" onQuery={() => {}} items={[]} onChoose={() => {}} onClose={() => { closed = true; }} />);
    expect(screen.getByText('Nothing matches “zzz”.')).toBeInTheDocument();
    await userEvent.keyboard('{Escape}');
    expect(closed).toBe(true);
  });
});
