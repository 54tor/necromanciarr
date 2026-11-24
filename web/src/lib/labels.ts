import type {
  CandidateState,
  Decision,
  DoctorStatus,
  LinkState,
  ProposalKind,
  ProposalStep,
} from '@/api/types'

export const KIND_LABELS: Record<ProposalKind, string> = {
  full: 'Torrent complet',
  partial_annex: 'Partiel, annexes manquantes',
  partial_pack: 'Pack partiel',
  cross_seed: 'Cross-seed',
  conflict: 'Conflit entre torrents',
  dedupe: 'Déduplication',
  repair: 'Réparation',
}

/** Natures affichées par le filtre de la vue Propositions (hors déduplication et réparation, qui ont leur vue). */
export const REVIEW_KINDS: readonly ProposalKind[] = [
  'full',
  'partial_annex',
  'partial_pack',
  'cross_seed',
  'conflict',
]

export const DECISION_LABELS: Record<Decision, string> = {
  pending: 'En attente',
  accepted: 'Acceptée',
  rejected: 'Rejetée',
  applied: 'Appliquée',
  failed: 'Échec',
  superseded: 'Remplacée',
}

export const STEP_LABELS: Record<ProposalStep, string> = {
  linked: 'Liée',
  seeded: 'Ensemencée',
  seeded_paused_incomplete: 'Ensemencée, en pause (incomplète)',
  forced: 'Forcée',
}

export const DOCTOR_LABELS: Record<DoctorStatus, string> = {
  ok: 'OK',
  warn: 'Avertissement',
  fail: 'Échec',
  skipped: 'Ignoré',
}

export const REPAIR_METHOD_LABELS: Record<string, string> = {
  inode: 'même inode',
  'size-mtime': 'taille et date de modification',
  fingerprint: 'empreinte du contenu',
  'torrent-pieces': 'pièces du torrent',
}

export const DEDUPE_METHOD_LABELS: Record<string, string> = {
  hash: 'empreinte connue',
  full: 'lecture complète',
  sampled: 'échantillonnage',
}

export const LINK_KIND_LABELS: Record<string, string> = {
  hardlink: 'lien physique',
  reflink: 'reflink',
  symlink: 'lien symbolique',
}

export const REPAIR_STATUS_LABELS: Record<string, string> = {
  repairable: 'Réparable',
  ambiguous: 'Ambigu',
  lost: 'Introuvable',
}

export const LINK_STATE_LABELS: Record<LinkState, string> = {
  ok: 'Conforme',
  diverged: 'Divergent',
  lost: 'Perdu',
  unreadable: 'Illisible',
}

/** Ordre d'affichage des états (filtre et effectifs). */
export const LINK_STATES: readonly LinkState[] = ['ok', 'diverged', 'lost', 'unreadable']

/** labelOf renvoie le libellé français d'une valeur connue, ou la valeur brute d'une valeur inconnue. */
export function labelOf(labels: Readonly<Record<string, string>>, value: string): string {
  return labels[value] ?? value
}

export const CANDIDATE_LABELS: Record<CandidateState, string> = {
  matched: 'Associés',
  fetched: 'Téléchargés',
  verified: 'Vérifiés',
  failed: 'En échec',
  stale: 'Obsolètes',
}
