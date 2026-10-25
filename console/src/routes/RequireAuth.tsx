import { Splash as SplashScreen } from '@enclavestack/ui';
import { Navigate, Outlet, useLocation } from 'react-router';
import { useSession } from '../auth/session';

export function Splash() {
  return <SplashScreen>Connecting to this machine…</SplashScreen>;
}

/** CON-03: every console page needs a session; remember where the operator was going. */
export function RequireAuth() {
  const { state } = useSession();
  const location = useLocation();
  if (state.status === 'loading') return <Splash />;
  if (state.status === 'signed-out') {
    return <Navigate to={state.setupRequired ? '/setup' : '/login'} replace state={{ from: location.pathname }} />;
  }
  return <Outlet />;
}
