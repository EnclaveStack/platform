import {
  Avatar, Banner, Brand, Breadcrumbs, crumbLinkClass, Finder, FinderButton, Icons, IconButton, Inline, machineLinkClass, MenuSelect, MeterCard, NavGroup, NavItemContent, navItemClass,
  NavList, ProgressBar, Shell, Sidebar, SidebarFoot, Stack, Status, Text, ToastProvider, Topbar, UserRow,
} from '@enclavestack/ui';
import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { Link, NavLink, Outlet, useLocation, useNavigate } from 'react-router';
import { useApi } from '../api/context';
import { RULES } from '../api/generated';
import { useSession } from '../auth/session';
import { SectionIcon } from '../components/SectionIcon';
import { formatBytes, percentOf } from '../lib/format';
import { ProjectProvider, useProject } from '../lib/project';
import { adminProjects, isMachineAdmin } from '../lib/roles';
import { groupOfPath, inSentence, isNavPage, NAV_GROUPS, NAV_PAGES, SECTIONS, sectionBySlug } from '../lib/sections';
import { useNavPrefs } from '../lib/nav-prefs';
import { findItems } from '../lib/find';
import { ShellProvider } from '../lib/shell';
import { useTheme } from '../lib/theme';
import { useLoad } from '../lib/useLoad';
import { usePolling } from '../lib/usePolling';

const THEME_LABEL = { system: 'Auto', light: 'Light', dark: 'Dark' } as const;
const THEME_ICON = { system: Icons.Monitor, light: Icons.Sun, dark: Icons.Moon } as const;
const MACHINE_POLL_MS = 10_000;
const isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform);

export function ConsoleLayout() {
  return (
    <ToastProvider>
      <ProjectProvider>
        <ConsoleShell />
      </ProjectProvider>
    </ToastProvider>
  );
}

function ConsoleShell() {
  const api = useApi();
  const { state, signOut } = useSession();
  const location = useLocation();
  const navigate = useNavigate();
  const { project, setProject, projectsVersion } = useProject();
  const [menuOpen, setMenuOpen] = useState(false);
  const [pageTitle, setPageTitle] = useState<string | null>(null);
  const [finding, setFinding] = useState(false);
  const [findQuery, setFindQuery] = useState('');
  // ⌘K / Ctrl+K opens Find from anywhere (CON-62).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault();
        setFinding(true);
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);
  const everything = useLoad(finding ? `find:${project}` : 'find:closed', () => (finding ? api.listResources(undefined, project || undefined) : Promise.resolve([])));
  const { closed, rail, toggleGroup, toggleRail } = useNavPrefs();
  const [theme, cycleTheme] = useTheme();
  const machine = useLoad('machine', () => api.getMachine());
  const projects = useLoad(`nav-projects:${projectsVersion}`, () => api.listProjects());
  const counts = useLoad(`counts:${project}`, async () => {
    const all = await api.listResources(undefined, project || undefined);
    const out: Record<string, number> = {};
    for (const s of SECTIONS) out[s.slug] = all.filter((r) => r.kind === s.kind && r.status.phase !== 'deleting').length;
    return out;
  });
  usePolling(true, MACHINE_POLL_MS, machine.reload);
  // Refresh counts and memory on every navigation, keeping the old values on screen meanwhile.
  const reloadMachine = machine.reload;
  const reloadCounts = counts.reload;
  const reloadProjects = projects.reload;
  useEffect(() => {
    reloadMachine();
    reloadCounts();
    reloadProjects();
  }, [location.pathname, reloadMachine, reloadCounts, reloadProjects]);

  const session = state.status === 'signed-in' ? state.session : null;
  const email = session?.user.email ?? '';
  const machineAdmin = isMachineAdmin(session);
  const peopleAdmin = machineAdmin || adminProjects(session, (projects.data ?? []).map((p) => p.name)).length > 0;
  // The projects this person can see: the server lists only those (PRJ-03).
  const projectSearch = project ? `?project=${encodeURIComponent(project)}` : '';
  const choose = (p: string) => {
    setProject(p);
    if (current) navigate(`/${current.slug}${p ? `?project=${encodeURIComponent(p)}` : ''}`);
  };
  // A chosen project that isn't listed (not visible to this person) still shows, so the menu never lies.
  const projectNames = [...(projects.data ?? []).map((p) => p.name), ...(project && !projects.data?.some((p) => p.name === project) ? [project] : [])];
  const m = machine.data;
  const current = SECTIONS.find((s) => location.pathname.startsWith(`/${s.slug}`));
  const SETTINGS: Record<string, string> = { '/catalogue': 'Catalogue', '/settings/tokens': 'API tokens', '/settings/people': 'People', '/settings/projects': 'Projects', '/settings/audit': 'Audit log', '/settings/account': 'Your account' };
  const close = () => setMenuOpen(false);
  const ThemeIcon = THEME_ICON[theme];
  const openGroup = groupOfPath(location.pathname);
  const shell = useMemo(() => ({ machineName: m?.name ?? '', setPageTitle }), [m?.name]);

  const navLink = (to: string | { pathname: string; search: string }, icon: ReactNode, label: string, count?: number) => (
    <NavLink key={typeof to === 'string' ? to : to.pathname} to={to} className={navItemClass} onClick={close} title={rail ? label : undefined}>
      <NavItemContent icon={icon} label={label} count={count === undefined ? undefined : counts.data ? count : '·'}
        countLabel={count === undefined ? undefined : `${count} ${inSentence(label)}`} />
    </NavLink>
  );

  // Where you are, every step but the last a link (CON-63).
  const crumbs: ReactNode[] = [];
  const machineName = m?.name ?? 'This machine';
  const machineTo = `/machines/${encodeURIComponent(RULES.mainMachine)}`; // this machine's own page
  const [, first = '', second] = location.pathname.split('/');
  const section = sectionBySlug(first);
  if (section) {
    const listTo = `/${section.slug}${projectSearch}`;
    crumbs.push(<Link key="m" to={machineTo} className={crumbLinkClass}>{machineName}</Link>);
    crumbs.push(second ? <Link key="p" to={listTo} className={crumbLinkClass}>{project || 'All projects'}</Link> : <span key="p">{project || 'All projects'}</span>);
    crumbs.push(second ? <Link key="s" to={listTo} className={crumbLinkClass}>{section.label}</Link> : <span key="s">{section.label}</span>);
    if (second) crumbs.push(<span key="r">{pageTitle ?? '…'}</span>);
  } else if (first === 'guide' && sectionBySlug(second)) {
    const s = sectionBySlug(second);
    crumbs.push(<Link key="m" to={machineTo} className={crumbLinkClass}>{machineName}</Link>);
    crumbs.push(<Link key="s" to={`/${s?.slug ?? ''}${projectSearch}`} className={crumbLinkClass}>{s?.label}</Link>);
    crumbs.push(<span key="g">Guide</span>);
  } else if (first === 'machines') {
    crumbs.push(<Link key="m" to={machineTo} className={crumbLinkClass}>{machineName}</Link>);
    crumbs.push(second ? <Link key="s" to="/machines" className={crumbLinkClass}>Machines</Link> : <span key="s">Machines</span>);
    if (second) crumbs.push(<span key="r">{pageTitle ?? decodeURIComponent(second)}</span>);
  } else {
    crumbs.push(<Link key="m" to={machineTo} className={crumbLinkClass}>{machineName}</Link>);
    if (SETTINGS[location.pathname]) crumbs.push(<span key="s">{SETTINGS[location.pathname]}</span>);
  }

  const sidebar = (
    <Sidebar label="Console navigation" open={menuOpen}>
      <Inline justify="between">
        <Brand />
        <IconButton size="sm" variant="ghost" label={rail ? 'Expand the sidebar' : 'Fold the sidebar to icons'} icon={rail ? <Icons.PanelLeftOpen /> : <Icons.PanelLeftClose />} onClick={toggleRail} />
      </Inline>
      <MenuSelect label="Project" value={project} onChange={choose} icon={<Icons.FolderKanban />}
        options={[{ value: '', label: 'All projects' }, ...projectNames.map((n) => ({ value: n, label: n }))]} />
      <Link to={machineTo} className={machineLinkClass} aria-label={`${m?.name ?? 'This machine'}, open its page`} onClick={close}>
        <Stack gap={0} grow>
          <Text weight="bold" truncate title={m?.hostname}>{m?.name ?? '…'}</Text>
          {m && <Status tone={m.engine.available ? 'ok' : 'bad'}>{m.engine.available ? 'Online' : 'Engine down'}</Status>}
          {m?.vms && !m.vms.available && <Text size="xs" tone="subtle" title={m.vms.message}>VMs unavailable</Text>}
        </Stack>
      </Link>
      <NavList label="Start">
        {navLink({ pathname: '/', search: projectSearch }, <Icons.LayoutDashboard />, 'Overview')}
      </NavList>
      {NAV_GROUPS.map((g) => (
        <NavGroup key={g.id} heading={g.heading} open={g.id === openGroup || !closed.includes(g.id)} onToggle={() => toggleGroup(g.id)}>
          {g.items.map((item) => {
            if (isNavPage(item)) {
              const page = NAV_PAGES[item];
              return navLink(page.to, item === 'catalogue' ? <Icons.Store /> : <Icons.Server />, page.label);
            }
            const s = sectionBySlug(item);
            return s ? navLink({ pathname: `/${s.slug}`, search: projectSearch }, <SectionIcon kind={s.kind} />, s.label, counts.data?.[s.slug] ?? 0) : null;
          })}
        </NavGroup>
      ))}
      <NavGroup heading="Settings" open={!closed.includes('settings') || location.pathname.startsWith('/settings')} onToggle={() => toggleGroup('settings')}>
        {navLink('/settings/account', <Icons.Lock />, 'Your account')}
        {navLink('/settings/tokens', <Icons.KeyRound />, 'API tokens')}
        {peopleAdmin && navLink('/settings/people', <Icons.Users />, 'People')}
        {machineAdmin && navLink('/settings/projects', <Icons.FolderKanban />, 'Projects')}
        {machineAdmin && navLink('/settings/audit', <Icons.History />, 'Audit log')}
      </NavGroup>
      <SidebarFoot>
        <MeterCard>
          <Inline justify="between" align="baseline">
            <Text size="sm" weight="semibold">Memory reserved</Text>
            <Text size="sm" mono>{m ? formatBytes(m.memoryReservedBytes) : '…'}</Text>
          </Inline>
          <ProgressBar label="Memory reserved" value={m ? percentOf(m.memoryReservedBytes, m.memoryTotalBytes) : 0} />
          <Text size="xs" tone="subtle">{m?.memoryTotalBytes ? `of ${formatBytes(m.memoryTotalBytes)} available to the engine` : 'Total memory unknown'}</Text>
        </MeterCard>
        <UserRow>
          <Avatar name={email || '?'} size="sm" aria-label={`Signed in as ${email}`} />
          {!rail && <Text size="sm" weight="semibold" truncate title={email}>{email}</Text>}
          <Inline gap={1}>
            <IconButton size="sm" variant="ghost" label={`Theme: ${THEME_LABEL[theme]}. Change theme`} icon={<ThemeIcon />} onClick={cycleTheme} />
            <IconButton size="sm" variant="ghost" label="Sign out" icon={<Icons.LogOut />} onClick={() => void signOut()} />
          </Inline>
        </UserRow>
      </SidebarFoot>
    </Sidebar>
  );

  return (
    <Shell sidebar={sidebar} menuOpen={menuOpen} onCloseMenu={close} rail={rail}>
      <Topbar>
        <Inline gap={2}>
          <IconButton extraClass="menu" label="Open menu" aria-expanded={menuOpen} icon={<Icons.Menu />} onClick={() => setMenuOpen(true)} />
          <Breadcrumbs>{crumbs}</Breadcrumbs>
        </Inline>
        <FinderButton onOpen={() => setFinding(true)} shortcut={isMac ? '⌘K' : 'Ctrl K'} />
      </Topbar>
      {finding && (
        <Finder
          query={findQuery}
          onQuery={setFindQuery}
          items={findItems(findQuery, everything.data ?? [], { peopleAdmin, machineAdmin })}
          onChoose={(item) => {
            setFinding(false);
            setFindQuery('');
            navigate(item.id);
          }}
          onClose={() => { setFinding(false); setFindQuery(''); }}
        />
      )}
      {m && !m.engine.available && (
        <Banner>{m.engine.message || "The container engine isn't running. Changes are queued and will apply when it's back."}</Banner>
      )}
      <ShellProvider value={shell}>
        <Outlet />
      </ShellProvider>
    </Shell>
  );
}
