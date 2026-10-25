import { Icons } from '@enclavestack/ui';
import type { Kind } from '../api/types';

const ICON: Record<Kind, typeof Icons.Box> = {
  app: Icons.Box,
  stack: Icons.Boxes,
  database: Icons.Database,
  cache: Icons.Zap,
  bucket: Icons.Archive,
  secret: Icons.KeyRound,
  domain: Icons.Globe,
  job: Icons.Clock,
  backup: Icons.History,
  network: Icons.Network,
  firewall: Icons.Shield,
  loadbalancer: Icons.Split,
  zone: Icons.Earth,
  record: Icons.Signpost,
  vm: Icons.Server,
  volume: Icons.HardDrive,
  snapshot: Icons.Camera,
  queue: Icons.ListOrdered,
  topic: Icons.Radio,
  search: Icons.TextSearch,
  transcoder: Icons.Clapperboard,
  cdn: Icons.Rocket,
  email: Icons.Mail,
  dashboard: Icons.LayoutDashboard,
  alert: Icons.BellRing,
  channel: Icons.Megaphone,
  logexport: Icons.ScrollText,
  secretstore: Icons.Vault,
  tunnel: Icons.Cable,
};

/** The icon that stands for a resource kind in the sidebar, list cards and empty states. */
export function SectionIcon({ kind }: { kind: Kind }) {
  const Icon = ICON[kind];
  return <Icon />;
}
