import { describe, expect, it } from 'vitest';
import { formatBytes, percentOf } from './format';

describe('format', () => {
  it('formats bytes as MB below a gigabyte and GB above', () => {
    expect(formatBytes(512 * 1024 ** 2)).toBe('512 MB');
    expect(formatBytes(4112 * 1024 ** 2)).toBe('4.0 GB');
    expect(formatBytes(3 * 1024 + 100)).toBe('3 KB'); // small backups
    expect(formatBytes(512)).toBe('512 bytes');
  });
  it('clamps percentages and treats an unknown total as 0', () => {
    expect(percentOf(50, 200)).toBe(25);
    expect(percentOf(500, 200)).toBe(100);
    expect(percentOf(50, null)).toBe(0);
    expect(percentOf(50, 0)).toBe(0);
  });
});
