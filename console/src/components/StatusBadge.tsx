import { Dot, Status, type StatusTone } from '@enclavestack/ui';
import type { Phase } from '../api/types';

const LABEL: Record<Phase, string> = {
  pending: 'Pending',
  provisioning: 'Provisioning',
  ready: 'Ready',
  updating: 'Updating',
  deleting: 'Deleting',
  failed: 'Failed',
};

const TONE: Record<Phase, StatusTone> = {
  pending: 'busy',
  provisioning: 'busy',
  updating: 'busy',
  deleting: 'busy',
  ready: 'ok',
  failed: 'bad',
};

export function phaseLabel(p: Phase): string {
  return LABEL[p];
}

export function inProgress(p: Phase): boolean {
  return TONE[p] === 'busy';
}

/** Phase is always a word beside the dot, never colour alone (L2.5-001 §3, L2.5-002 §4). */
export function StatusBadge({ phase }: { phase: Phase }) {
  return <Status tone={TONE[phase]}>{LABEL[phase]}</Status>;
}

export function PhaseDot({ phase }: { phase: Phase }) {
  return <Dot tone={TONE[phase]} label={LABEL[phase]} />;
}
