import {
  Avatar, Banner, Brand, Crumbs, Icons, IconButton, Inline, MachineCard, MeterCard, NavItemContent, navItemClass, NavList, ProgressBar,
  Select, Shell, Sidebar, SidebarFoot, SidebarSection, Stack, Status, Text, ToastProvider, Topbar, UserRow, VisuallyHidden,
} from '@enclavestack/ui';
import { useEffect, useState } from 'react';
import { NavLink, Outlet, useLocation, useNavigate } from 'react-router';
import { useApi } from '../api/context';
import { useSession } from '../auth/session';
import { SectionIcon } from '../components/SectionIcon';
import { formatBytes, percentOf } from '../lib/format';
import { ProjectProvider, useProject } from '../lib/project';
import { adminProjects, isMachineAdmin } from '../lib/roles';
import { NAV_SECTIONS, OPERATE_SECTIONS, SECTIONS } from '../lib/sections';
import { useTheme } from '../lib/theme';
import { useLoad } from '../lib/useLoad';
import { usePolling } from '../lib/usePolling';

const THEME_LABEL = { system: 'Auto', light: 'Light', dark: 'Dark' } as const;
const THEME_ICON = { system: Icons.Monitor, light: Icons.Sun, dark: Icons.Moon } as const;
const MACHINE_POLL_MS = 10_000;

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
  const m = machine.data;
  const current = SECTIONS.find((s) => location.pathname.startsWith(`/${s.slug}`));
  const SETTINGS: Record<string, string> = { '/settings/tokens': 'API tokens', '/settings/people': 'People', '/settings/projects': 'Projects', '/settings/audit': 'Audit log', '/settings/account': 'Your account' };
  const crumb = current?.label ?? SETTINGS[location.pathname] ?? '';
  const close = () => setMenuOpen(false);
  const ThemeIcon = THEME_ICON[theme];

  const sidebar = (
    <Sidebar label="Console navigation" open={menuOpen}>
      <Brand />
      <Stack gap={1}>
        <VisuallyHidden><label htmlFor="project-switcher">Project</label></VisuallyHidden>
        <Select id="project-switcher" value={project} onChange={(e) => choose(e.target.value)}>
          <option value="">All projects</option>
          {(projects.data ?? []).map((p) => <option key={p.name} value={p.name}>{p.name}</option>)}
          {project && !projects.data?.some((p) => p.name === project) && <option value={project}>{project}</option>}
        </Select>
      </Stack>
      <MachineCard>
        <Avatar name={m?.name ?? 'machine'} size="md"><Icons.Server /></Avatar>
        <Stack gap={0} grow>
          <Text weight="bold" truncate>{m?.name ?? '…'}</Text>
          <Text size="xs" tone="subtle" mono truncate title={m?.hostname}>{m?.hostname ?? ''}</Text>
          {m && <Status tone={m.engine.available ? 'ok' : 'bad'}>{m.engine.available ? 'Online' : 'Engine down'}</Status>}
          {m?.vms && !m.vms.available && <Text size="xs" tone="subtle" title={m.vms.message}>VMs unavailable</Text>}
          {m?.broker?.available && <Text size="xs" tone="subtle" title="The message broker behind queues and topics">Message broker running</Text>}
        </Stack>
      </MachineCard>
      <SidebarSection heading="Resources">
        <NavList label="Resources">
          {NAV_SECTIONS.map((s) => (
            <NavLink key={s.slug} to={{ pathname: `/${s.slug}`, search: projectSearch }} className={navItemClass} onClick={close}>
              <NavItemContent icon={<SectionIcon kind={s.kind} />} label={s.label} count={counts.data ? counts.data[s.slug] : '·'}
                countLabel={`${counts.data?.[s.slug] ?? 0} ${s.label.toLowerCase()}`} />
            </NavLink>
          ))}
        </NavList>
      </SidebarSection>
      <SidebarSection heading="Operate">
        <NavList label="Operate">
          {OPERATE_SECTIONS.map((s) => (
            <NavLink key={s.slug} to={{ pathname: `/${s.slug}`, search: projectSearch }} className={navItemClass} onClick={close}>
              <NavItemContent icon={<SectionIcon kind={s.kind} />} label={s.label} count={counts.data ? counts.data[s.slug] : '·'}
                countLabel={`${counts.data?.[s.slug] ?? 0} ${s.label.toLowerCase()}`} />
            </NavLink>
          ))}
        </NavList>
      </SidebarSection>
      <SidebarSection heading="Settings">
        <NavList label="Settings">
          <NavLink to="/settings/account" className={navItemClass} onClick={close}>
            <NavItemContent icon={<Icons.Lock />} label="Your account" />
          </NavLink>
          <NavLink to="/settings/tokens" className={navItemClass} onClick={close}>
            <NavItemContent icon={<Icons.KeyRound />} label="API tokens" />
          </NavLink>
          {peopleAdmin && (
            <NavLink to="/settings/people" className={navItemClass} onClick={close}>
              <NavItemContent icon={<Icons.Users />} label="People" />
            </NavLink>
          )}
          {machineAdmin && (
            <NavLink to="/settings/projects" className={navItemClass} onClick={close}>
              <NavItemContent icon={<Icons.FolderKanban />} label="Projects" />
            </NavLink>
          )}
          {machineAdmin && (
            <NavLink to="/settings/audit" className={navItemClass} onClick={close}>
              <NavItemContent icon={<Icons.History />} label="Audit log" />
            </NavLink>
          )}
        </NavList>
      </SidebarSection>
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
          <Text size="sm" weight="semibold" truncate title={email}>{email}</Text>
          <Inline gap={1}>
            <IconButton size="sm" variant="ghost" label={`Theme: ${THEME_LABEL[theme]}. Change theme`} icon={<ThemeIcon />} onClick={cycleTheme} />
            <IconButton size="sm" variant="ghost" label="Sign out" icon={<Icons.LogOut />} onClick={() => void signOut()} />
          </Inline>
        </UserRow>
      </SidebarFoot>
    </Sidebar>
  );

  return (
    <Shell sidebar={sidebar} menuOpen={menuOpen} onCloseMenu={close}>
      <Topbar>
        <Crumbs>
          <IconButton extraClass="menu" label="Open menu" aria-expanded={menuOpen} icon={<Icons.Menu />} onClick={() => setMenuOpen(true)} />
          <span>{m?.name ?? 'This machine'}</span>
          <span aria-hidden="true">/</span>
          {current && <><span>{project || 'All projects'}</span><span aria-hidden="true">/</span></>}
          <strong>{crumb}</strong>
        </Crumbs>
      </Topbar>
      {m && !m.engine.available && (
        <Banner>{m.engine.message || "The container engine isn't running. Changes are queued and will apply when it's back."}</Banner>
      )}
      <Outlet />
    </Shell>
  );
}
