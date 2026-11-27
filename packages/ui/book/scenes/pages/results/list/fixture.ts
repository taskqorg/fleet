import { NOW, min, PROJECTS } from '../../../../fixtures.ts';

export type Res = { id: string; name: string; type: string; size: number; project: string; task: number; at: number; copy: 'stored' | 'placed' | 'pending' | 'unavailable' };
const TYPES = ['diff', 'image', 'report', 'video', 'glb'];
export const RESULTS: Res[] = Array.from({ length: 2000 }, (_, i) => ({ id: `r${i}`, name: `${['index.html', 'hero.png', 'review.md', 'walk.mp4', 'prop.glb'][i % 5]}`, type: TYPES[i % 5], size: ((i + 1) * 7919 % 4000 + 1) * 1e5,
  project: PROJECTS[i % 4], task: 9989 - i, at: NOW - i * 7 * min, copy: (['stored', 'placed', 'placed', 'pending', 'unavailable'] as const)[i * 3 % 5] }));
