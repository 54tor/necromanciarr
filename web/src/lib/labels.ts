import type { CodeGroup } from '@/i18n'
import { codeLabel } from '@/i18n'
import type { ProposalKind } from '@/api/types'

/**
 * labelTable expose un groupe de codes comme une table `code -> libellé` : chaque lecture passe par `codeLabel`,
 * donc suit la langue active. Supprimée avec les vues qui l'utilisent (D3d).
 */
function labelTable(group: CodeGroup, codes: readonly string[]): Readonly<Record<string, string>> {
  const table: Record<string, string> = {}
  for (const code of codes) {
    Object.defineProperty(table, code, { enumerable: true, get: () => codeLabel(group, code) })
  }
  return table
}

export const KIND_LABELS = labelTable('kind', [
  'full',
  'partial_annex',
  'partial_pack',
  'cross_seed',
  'conflict',
  'dedupe',
  'repair',
])
export const DECISION_LABELS = labelTable('decision', [
  'pending',
  'accepted',
  'rejected',
  'applied',
  'failed',
  'superseded',
])
export const STEP_LABELS = labelTable('state', [
  'linked',
  'seeded',
  'seeded_paused_incomplete',
  'forced',
])
export const REPAIR_METHOD_LABELS = labelTable('reason', [
  'inode',
  'size-mtime',
  'fingerprint',
  'torrent-pieces',
])
export const DEDUPE_METHOD_LABELS = labelTable('reason', ['hash', 'full', 'sampled'])
export const LINK_KIND_LABELS = labelTable('kind', ['hardlink', 'reflink', 'symlink'])
export const REPAIR_STATUS_LABELS = labelTable('reason', ['repairable', 'ambiguous', 'lost'])
export const LINK_STATE_LABELS = labelTable('state', ['ok', 'diverged', 'lost', 'unreadable'])
export const CANDIDATE_LABELS = labelTable('state', [
  'matched',
  'fetched',
  'verified',
  'failed',
  'stale',
])
/** Raisons pour lesquelles un torrent vérifié n'est pas proposé (inconnue : affichée telle quelle). */
export const HELD_REASON_LABELS = labelTable('reason', ['missing_pieces_above_cap'])

/** Natures affichées par le filtre de la vue Propositions (hors déduplication et réparation, qui ont leur vue). */
export const REVIEW_KINDS: readonly ProposalKind[] = [
  'full',
  'partial_annex',
  'partial_pack',
  'cross_seed',
  'conflict',
]

/** Ordre d'affichage des états (filtre et effectifs). */
export const LINK_STATES = ['ok', 'diverged', 'lost', 'unreadable'] as const

/** labelOf renvoie le libellé d'une valeur connue, ou la valeur brute d'une valeur inconnue. */
export function labelOf(labels: Readonly<Record<string, string>>, value: string): string {
  return labels[value] ?? value
}
