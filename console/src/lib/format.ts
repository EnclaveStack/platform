const GiB = 1024 ** 3;
const MiB = 1024 ** 2;
const KiB = 1024;

/** Machine memory and backup sizes: GB with one decimal, whole MB, then KB and bytes for small dumps. */
export function formatBytes(bytes: number): string {
  if (bytes >= GiB) return `${(bytes / GiB).toFixed(1)} GB`;
  if (bytes >= MiB) return `${Math.round(bytes / MiB)} MB`;
  if (bytes >= KiB) return `${Math.round(bytes / KiB)} KB`;
  return `${bytes} bytes`;
}

/** Fraction of total, clamped to 0–100; 0 when the total is unknown. */
export function percentOf(part: number, total: number | null): number {
  if (!total || total <= 0) return 0;
  return Math.min(100, Math.max(0, (part / total) * 100));
}

/** "just now", "5 min ago", "3 h ago", "2 days ago", then the date: when a resource last changed. */
export function formatAgo(iso: string, now = Date.now()): string {
  const then = Date.parse(iso);
  if (Number.isNaN(then)) return '';
  const s = Math.max(0, Math.round((now - then) / 1000));
  if (s < 60) return 'just now';
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86_400) return `${Math.floor(s / 3600)} h ago`;
  if (s < 7 * 86_400) return `${Math.floor(s / 86_400)} day${s < 2 * 86_400 ? '' : 's'} ago`;
  return new Date(then).toLocaleDateString();
}
