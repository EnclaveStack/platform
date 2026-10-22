import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';

// In development the console talks to the in-memory mock (VITE_ENCLAVE_API=mock, the default).
// With VITE_ENCLAVE_API=http, /api is proxied to a control plane running locally on :7070 (L3-002).
// In production the Go binary serves this build and /api from the same origin, so no proxy is involved.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: { '/api': 'http://127.0.0.1:7070' },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test-setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
    testTimeout: 15_000,
  },
});
