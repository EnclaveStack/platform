/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** Which EnclaveApi adapter the build uses (ADR-002). Defaults to `mock` in dev, `http` in production builds. */
  readonly VITE_ENCLAVE_API?: 'mock' | 'http';
}
