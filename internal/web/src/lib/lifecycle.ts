// Where a pull request is in its life, as the list's marker and its own
// page's badge both say it. Rune-free so tests can import it.
import { mdiFileDocumentEditOutline, mdiSourceBranchRemove, mdiSourceMerge, mdiSourcePull } from './icons';
import type { Tone } from './format';
import type { Pull } from './types';

export interface Lifecycle {
  state: 'merged' | 'closed' | 'draft' | 'open';
  label: string;
  icon: string;
  tone: Tone;
}

// lifecycle is the first that holds of merged, closed, draft and open: a
// merged pull request is also closed, and a draft may be either.
export function lifecycle(p: Pick<Pull, 'merged' | 'state' | 'draft'>): Lifecycle {
  if (p.merged) return { state: 'merged', label: 'Merged', icon: mdiSourceMerge, tone: 'merged' };
  if (p.state === 'closed') return { state: 'closed', label: 'Closed', icon: mdiSourceBranchRemove, tone: 'danger' };
  if (p.draft) return { state: 'draft', label: 'Draft', icon: mdiFileDocumentEditOutline, tone: 'muted' };
  return { state: 'open', label: 'Open', icon: mdiSourcePull, tone: 'ok' };
}
