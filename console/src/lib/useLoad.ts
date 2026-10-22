import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';

export interface Loaded<T> {
  data: T | undefined;
  error: Error | undefined;
  loading: boolean;
  reload: () => void;
}

interface Result<T> {
  key: string;
  tick: number;
  data?: T;
  error?: Error;
}

/**
 * Runs `load` whenever `key` changes and on reload(). A superseded run's result is dropped, so a fast
 * click through the list never shows the previous item's data. During a reload of the same key the
 * previous data stays visible.
 */
export function useLoad<T>(key: string, load: () => Promise<T>): Loaded<T> {
  const [result, setResult] = useState<Result<T> | null>(null);
  const [tick, setTick] = useState(0);
  const loadRef = useRef(load);
  useLayoutEffect(() => {
    loadRef.current = load;
  });

  useEffect(() => {
    let live = true;
    loadRef.current().then(
      (data) => live && setResult({ key, tick, data }),
      (error: unknown) => live && setResult({ key, tick, error: error instanceof Error ? error : new Error(String(error)) }),
    );
    return () => {
      live = false;
    };
  }, [key, tick]);

  const reload = useCallback(() => setTick((t) => t + 1), []);
  const sameKey = result?.key === key;
  const settled = sameKey && result.tick === tick;
  return { data: sameKey ? result.data : undefined, error: settled ? result.error : undefined, loading: !settled, reload };
}
