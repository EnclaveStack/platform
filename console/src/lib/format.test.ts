import { describe, expect, it } from 'vitest';
import { formatAgo, formatBytes, percentOf } from './format';

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

describe('formatAgo', () => {
  const now = Date.parse('2026-10-22T12:00:00Z');
  it('says how long ago, coarsening as time passes', () => {
    expect(formatAgo('2026-10-22T11:59:30Z', now)).toBe('just now');
    expect(formatAgo('2026-10-22T11:55:00Z', now)).toBe('5 min ago');
    expect(formatAgo('2026-10-22T09:00:00Z', now)).toBe('3 h ago');
    expect(formatAgo('2026-10-21T11:00:00Z', now)).toBe('1 day ago');
    expect(formatAgo('2026-10-19T12:00:00Z', now)).toBe('3 days ago');
  });
  it('gives the date after a week, and nothing for a bad time', () => {
    expect(formatAgo('2026-10-01T12:00:00Z', now)).toBe(new Date('2026-10-01T12:00:00Z').toLocaleDateString());
    expect(formatAgo('not a time', now)).toBe('');
  });
});
