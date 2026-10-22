import { useCallback, useEffect, useState } from 'react';

export type ThemeChoice = 'system' | 'light' | 'dark';
const KEY = 'enclave.theme';
const ORDER: ThemeChoice[] = ['system', 'light', 'dark'];

function read(): ThemeChoice {
  try {
    const v = localStorage.getItem(KEY);
    return v === 'light' || v === 'dark' ? v : 'system';
  } catch {
    return 'system';
  }
}

export function applyTheme(choice: ThemeChoice) {
  const root = document.documentElement;
  if (choice === 'system') root.removeAttribute('data-theme');
  else root.setAttribute('data-theme', choice);
}

/** Light / dark / follow the OS (CON-10). The choice is a per-browser convenience, so localStorage is enough. */
export function useTheme(): [ThemeChoice, () => void] {
  const [choice, setChoice] = useState<ThemeChoice>(read);
  useEffect(() => applyTheme(choice), [choice]);
  const cycle = useCallback(() => {
    setChoice((c) => {
      const next = ORDER[(ORDER.indexOf(c) + 1) % ORDER.length] ?? 'system';
      try {
        localStorage.setItem(KEY, next);
      } catch {
        // Storage blocked: the choice lasts until reload.
      }
      return next;
    });
  }, []);
  return [choice, cycle];
}
