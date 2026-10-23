import { createContext, useContext, useEffect, type ReactNode } from 'react';

interface Shell {
  /** The machine the console runs on, for the breadcrumb's first step. */
  machineName: string;
  /** Names the page's last breadcrumb step (a resource's name); null clears it. */
  setPageTitle: (title: string | null) => void;
}

const ShellContext = createContext<Shell>({ machineName: '', setPageTitle: () => {} });

export function ShellProvider({ value, children }: { value: Shell; children: ReactNode }) {
  return <ShellContext.Provider value={value}>{children}</ShellContext.Provider>;
}

export function useShell(): Shell {
  return useContext(ShellContext);
}

/** Puts `title` at the end of the breadcrumb while the page shows (CON-63). */
export function usePageTitle(title: string | undefined) {
  const { setPageTitle } = useShell();
  useEffect(() => {
    setPageTitle(title ?? null);
    return () => setPageTitle(null);
  }, [title, setPageTitle]);
}
