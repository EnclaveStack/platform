import { createContext, useContext, type ReactNode } from 'react';
import type { EnclaveApi } from './client';

const ApiContext = createContext<EnclaveApi | null>(null);

export function ApiProvider({ api, children }: { api: EnclaveApi; children: ReactNode }) {
  return <ApiContext.Provider value={api}>{children}</ApiContext.Provider>;
}

export function useApi(): EnclaveApi {
  const api = useContext(ApiContext);
  if (!api) throw new Error('useApi() needs an <ApiProvider> above it');
  return api;
}
