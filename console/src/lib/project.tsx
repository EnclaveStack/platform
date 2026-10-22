import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import { useSearchParams } from 'react-router';
import { RULES } from '../api/generated';
import type { Resource, Role } from '../api/types';
import { useSession } from '../auth/session';
import { can, roleIn } from './roles';

// The current project (CON-45, L2.5-002 §14): "" means all projects. It lives here, is remembered in this
// browser, and is mirrored into ?project= so links open in the right project.

const STORAGE_KEY = 'enclave.project';

interface ProjectValue {
  /** The chosen project, or "" for all projects. */
  project: string;
  setProject: (p: string) => void;
  /** Where "New …" creates: the chosen project, or default under "All projects". */
  createIn: string;
  /** Whether the caller has `need` in a resource's project (or in createIn). */
  allowed: (need: Role, r?: Pick<Resource, 'project'>) => boolean;
  role: (project: string) => Role | null;
  /** Bumped when projects are created or deleted, so the switcher reloads its list. */
  projectsVersion: number;
  projectsChanged: () => void;
}

const ProjectContext = createContext<ProjectValue | null>(null);

function remembered(): string {
  try {
    return localStorage.getItem(STORAGE_KEY) ?? '';
  } catch {
    return '';
  }
}

export function ProjectProvider({ children }: { children: ReactNode }) {
  const [params, setParams] = useSearchParams();
  const [stored, setStored] = useState(remembered);
  const [projectsVersion, setVersion] = useState(0);
  const projectsChanged = useCallback(() => setVersion((v) => v + 1), []);
  const fromUrl = params.get('project');
  // A link with ?project= wins; otherwise the remembered choice.
  const project = fromUrl ?? stored;
  const { state } = useSession();
  const session = state.status === 'signed-in' ? state.session : null;

  // The URL shows the remembered choice, so a copied link opens in the same project.
  useEffect(() => {
    if (fromUrl !== null || !stored) return;
    const next = new URLSearchParams(params);
    next.set('project', stored);
    setParams(next, { replace: true });
  }, [fromUrl, stored, params, setParams]);

  const setProject = useCallback((p: string) => {
    setStored(p);
    try {
      localStorage.setItem(STORAGE_KEY, p);
    } catch {
      // storage blocked: the choice lasts this visit
    }
    const next = new URLSearchParams(params);
    if (p) next.set('project', p);
    else next.delete('project');
    setParams(next, { replace: true });
  }, [params, setParams]);

  const value = useMemo<ProjectValue>(() => {
    const createIn = project || RULES.defaultProject;
    return {
      project, setProject, createIn,
      allowed: (need, r) => can(session, r?.project ?? createIn, need),
      role: (p) => roleIn(session, p),
      projectsVersion, projectsChanged,
    };
  }, [project, setProject, session, projectsVersion, projectsChanged]);
  return <ProjectContext.Provider value={value}>{children}</ProjectContext.Provider>;
}

export function useProject(): ProjectValue {
  const ctx = useContext(ProjectContext);
  if (!ctx) throw new Error('useProject() needs a <ProjectProvider> above it');
  return ctx;
}
