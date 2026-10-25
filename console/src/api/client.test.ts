import { describe, expect, it } from 'vitest';
import { resolveApiMode } from './client';

describe('resolveApiMode (ADR-002)', () => {
  it('honours an explicit setting', () => {
    expect(resolveApiMode({ VITE_ENCLAVE_API: 'mock', PROD: true })).toBe('mock');
    expect(resolveApiMode({ VITE_ENCLAVE_API: 'http', PROD: false })).toBe('http');
  });
  it('defaults to the real backend in production builds and the mock in development', () => {
    expect(resolveApiMode({ PROD: true })).toBe('http');
    expect(resolveApiMode({ PROD: false })).toBe('mock');
  });
  it('ignores an unknown value', () => {
    expect(resolveApiMode({ VITE_ENCLAVE_API: 'staging', PROD: true })).toBe('http');
  });
});
