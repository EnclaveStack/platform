import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import { useApi } from '../api/context';
import type { Session, SetupInput } from '../api/types';

type SessionState =
  | { status: 'loading' }
  | { status: 'signed-out'; setupRequired: boolean }
  | { status: 'signed-in'; session: Session };

interface SessionContextValue {
  state: SessionState;
  signIn: (email: string, password: string) => Promise<void>;
  setup: (input: SetupInput) => Promise<void>;
  signOut: () => Promise<void>;
  /** Sets a password from an invite or reset link and signs in (IAM-03). */
  acceptInvite: (code: string, password: string) => Promise<void>;
}

const SessionContext = createContext<SessionContextValue | null>(null);

export function SessionProvider({ children }: { children: ReactNode }) {
  const api = useApi();
  const [state, setState] = useState<SessionState>({ status: 'loading' });

  useEffect(() => {
    let live = true;
    (async () => {
      const session = await api.getSession();
      if (!live) return;
      if (session) setState({ status: 'signed-in', session });
      else setState({ status: 'signed-out', setupRequired: await api.getSetupRequired() });
    })().catch(() => live && setState({ status: 'signed-out', setupRequired: false }));
    return () => {
      live = false;
    };
  }, [api]);

  const signIn = useCallback(async (email: string, password: string) => {
    setState({ status: 'signed-in', session: await api.signIn(email, password) });
  }, [api]);

  const setup = useCallback(async (input: SetupInput) => {
    setState({ status: 'signed-in', session: await api.setup(input) });
  }, [api]);

  const signOut = useCallback(async () => {
    await api.signOut();
    setState({ status: 'signed-out', setupRequired: false });
  }, [api]);

  const acceptInvite = useCallback(async (code: string, password: string) => {
    setState({ status: 'signed-in', session: await api.acceptInvite(code, password) });
  }, [api]);

  const value = useMemo(() => ({ state, signIn, setup, signOut, acceptInvite }), [state, signIn, setup, signOut, acceptInvite]);
  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}

export function useSession(): SessionContextValue {
  const ctx = useContext(SessionContext);
  if (!ctx) throw new Error('useSession() needs a <SessionProvider> above it');
  return ctx;
}
