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
