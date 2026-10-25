import { useEffect } from 'react';

/** Calls tick every intervalMs while active (L2.5-002 §4: poll while anything is in progress). */
export function usePolling(active: boolean, intervalMs: number, tick: () => void) {
  useEffect(() => {
    if (!active) return;
    const t = setInterval(tick, intervalMs);
    return () => clearInterval(t);
  }, [active, intervalMs, tick]);
}
