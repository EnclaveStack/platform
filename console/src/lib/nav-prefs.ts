import { useCallback, useState } from 'react';

const KEY = 'enclave.nav';

interface NavPrefs {
  /** Sidebar groups the person closed (CON-61). */
  closed: string[];
  /** The sidebar folded to its icons. */
  rail: boolean;
}

function read(): NavPrefs {
  try {
    const v = JSON.parse(localStorage.getItem(KEY) ?? '{}') as Partial<NavPrefs>;
    return { closed: Array.isArray(v.closed) ? v.closed.filter((c): c is string => typeof c === 'string') : [], rail: v.rail === true };
  } catch {
    return { closed: [], rail: false };
  }
}

function write(prefs: NavPrefs) {
  try {
    localStorage.setItem(KEY, JSON.stringify(prefs));
  } catch {
    // A private window or blocked storage: the sidebar still works, it just won't remember.
  }
}

/** Which sidebar groups are closed and whether the sidebar is a rail, remembered in this browser. */
export function useNavPrefs() {
  const [prefs, setPrefs] = useState<NavPrefs>(read);
  const update = useCallback((change: (p: NavPrefs) => NavPrefs) => {
    setPrefs((p) => {
      const next = change(p);
      write(next);
      return next;
    });
  }, []);
  const toggleGroup = useCallback(
    (id: string) => update((p) => ({ ...p, closed: p.closed.includes(id) ? p.closed.filter((c) => c !== id) : [...p.closed, id] })),
    [update],
  );
  const toggleRail = useCallback(() => update((p) => ({ ...p, rail: !p.rail })), [update]);
  return { closed: prefs.closed, rail: prefs.rail, toggleGroup, toggleRail };
}
