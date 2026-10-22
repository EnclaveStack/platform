import { ROLES } from '../api/generated';
import type { Role, Session } from '../api/types';

// Roles in the interface (ADR-036, CON-48): the same arithmetic as the server. The effective role in a
// project is the stronger of the machine role and the project role, narrowed by a token's role and
// projects. The server still decides; this only hides and disables what would be refused.

const rank = (r: Role | null | undefined) => (r ? ROLES.indexOf(r) + 1 : 0);

/** The caller's role in a project, or null when they can't see it. */
export function roleIn(s: Session | null, project: string): Role | null {
  if (!s) return null;
  let best = rank(s.machineRole) >= rank(s.projectRoles[project]) ? s.machineRole : (s.projectRoles[project] ?? null);
  if (best && s.token) {
    if (s.token.projects && !s.token.projects.includes(project)) return null;
    if (rank(s.token.role) < rank(best)) best = s.token.role;
  }
  return best;
}

/** Whether the caller has at least `need` in the project. */
export function can(s: Session | null, project: string, need: Role): boolean {
  return rank(roleIn(s, project)) >= rank(need);
}

/** Machine-wide powers: projects, people of every project, audit, machine settings. */
export const isMachineAdmin = (s: Session | null) => s?.machineRole === 'admin' && (!s.token || (s.token.role === 'admin' && !s.token.projects));

/** Projects the caller administers (they see People for these). */
export function adminProjects(s: Session | null, projects: string[]): string[] {
  return projects.filter((p) => can(s, p, 'admin'));
}

export const ROLE_LABEL: Record<Role, string> = { viewer: 'Viewer', editor: 'Editor', admin: 'Admin' };

/** "Editors can do this. Ask an admin of shop." (L2.5-002 §14). */
export function needRole(need: Role, project: string): string {
  return `${ROLE_LABEL[need]}s can do this. Ask an admin of ${project}.`;
}
