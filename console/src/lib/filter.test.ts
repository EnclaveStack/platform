import { describe, expect, it } from 'vitest';
import type { Resource } from '../api/types';
import { filterResources } from './filter';

const r = (name: string, id = 'app_' + name): Resource => ({ id, kind: 'app', name, project: 'default', generation: 1, ownerId: null, spec: { image: 'x' },
  status: { phase: 'ready', message: '', observedGeneration: 1, attempts: 0, steps: [], lastTransitionAt: '' }, outputs: {}, createdAt: '', updatedAt: '' });

describe('filterResources', () => {
  const items = [r('auth-service'), r('gateway'), r('postgres-main', 'db_zz9')];
  it('keeps everything for a blank query', () => expect(filterResources(items, '  ')).toHaveLength(3));
  it('matches names case-insensitively', () => expect(filterResources(items, 'AUTH').map((i) => i.name)).toEqual(['auth-service']));
  it('matches IDs', () => expect(filterResources(items, 'zz9').map((i) => i.name)).toEqual(['postgres-main']));
  it('returns nothing when nothing matches', () => expect(filterResources(items, 'nope')).toEqual([]));
});
