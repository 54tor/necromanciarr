import type { Decision, Proposal, ProposalKind } from '@/api/types'

/** Décisions depuis lesquelles une proposition peut encore être acceptée (`failed` se réaccepte). */
const OPEN_DECISIONS: readonly Decision[] = ['pending', 'failed']

/** Rang d'affichage des décisions : ce qui attend une action d'abord. */
const DECISION_RANKS: Readonly<Record<Decision, number>> = {
  pending: 0,
  failed: 1,
  accepted: 2,
  applied: 3,
  rejected: 4,
  superseded: 5,
}

/** Teinte de la pastille de décision, commune aux vues Propositions et Déduplication. */
export const DECISION_TONES = {
  pending: 'info',
  accepted: 'info',
  applied: 'ok',
  failed: 'fail',
  rejected: 'muted',
  superseded: 'muted',
} as const satisfies Record<Decision, 'ok' | 'warn' | 'fail' | 'info' | 'muted'>

const GROUP_ACCEPTED_KINDS: readonly Proposal['kind'][] = ['cross_seed', 'full']

export function decisionRank(decision: Decision): number {
  return DECISION_RANKS[decision]
}

const isOpen = (proposal: Proposal) =>
  proposal.current && OPEN_DECISIONS.includes(proposal.decision)

/** Un conflit se tranche par « choisir », jamais par « accepter ». */
export const canAccept = (proposal: Proposal) => isOpen(proposal) && proposal.kind !== 'conflict'

/**
 * Sans annonce (`no_announce`), Link refusera la proposition ; accepter reste permis (parité avec la CLI). Calculé par
 * le serveur à chaque lecture : ajouter une entrée `trackers` lève l'état sans revérifier.
 */
export const hasNoAnnounce = (proposal: Proposal) => proposal.details.announce_source === 'none'

/** Le serveur ne rejette que depuis `pending`. */
export const canReject = (proposal: Proposal) => proposal.current && proposal.decision === 'pending'

export const canChoose = (proposal: Proposal) => isOpen(proposal) && proposal.kind === 'conflict'

/** Le serveur n'accepte un groupe que depuis une principale `full` en attente ou déjà acceptée. */
export const canAcceptGroup = (proposal: Proposal) => isGroupPrimary(proposal)

/** Seule case à cocher permise : rejeter une proposition en attente et courante (jamais accepter en masse). */
export const isSelectable = (proposal: Proposal) => canReject(proposal)

/**
 * Forcer ne vaut que pour un partiel à annexes manquantes déjà ensemencé en pause (docs/reference/03-seed-safety.md, 5) :
 * un pack partiel n'est jamais forçable.
 */
export const canForce = (proposal: Proposal) =>
  proposal.current &&
  proposal.kind === 'partial_annex' &&
  proposal.decision === 'applied' &&
  proposal.step === 'seeded_paused_incomplete'

const isGroupPrimary = (proposal: Proposal) =>
  proposal.current &&
  proposal.primary &&
  proposal.kind === 'full' &&
  (proposal.decision === 'pending' || proposal.decision === 'accepted')

/** Un torrent déjà accepté ou appliqué occupe son candidat (les conflits ne sont pas des torrents). */
const isTakenTorrent = (proposal: Proposal) =>
  (proposal.decision === 'accepted' || proposal.decision === 'applied') &&
  proposal.kind !== 'conflict'

/**
 * Propositions que « accepter le groupe » accepte (miroir de `groupTargets` côté serveur) : la principale, puis, en
 * cross-seed natif seulement, une proposition en attente par autre candidat, `cross_seed` avant `full`.
 */
export function groupTargets(members: Proposal[], isNative: boolean): Proposal[] {
  const primary = members.find(isGroupPrimary)
  if (!primary) return []
  if (!isNative) return [primary]
  const takenCandidates = new Set<number>([primary.candidate_id])
  for (const member of members) if (isTakenTorrent(member)) takenCandidates.add(member.candidate_id)
  const targets = [primary]
  for (const kind of GROUP_ACCEPTED_KINDS) {
    for (const member of members) {
      const isTarget =
        member.kind === kind &&
        member.decision === 'pending' &&
        !takenCandidates.has(member.candidate_id)
      if (!isTarget) continue
      targets.push(member)
      takenCandidates.add(member.candidate_id)
    }
  }
  return targets
}

/** La colonne et le filtre Source n'existent que si le serveur envoie le champ (ajouté par la tâche B14). */
export function hasSourceField(proposals: Proposal[]): boolean {
  return proposals.some((proposal) => proposal.source !== undefined)
}

/** Natures offertes par le filtre de la vue Propositions. */
export const KIND_CODES: readonly ProposalKind[] = [
  'full',
  'partial_annex',
  'partial_pack',
  'cross_seed',
  'conflict',
  'dedupe',
  'repair',
]

/** Décisions offertes par le filtre de la vue Propositions. */
export const DECISION_CODES: readonly Decision[] = [
  'pending',
  'accepted',
  'rejected',
  'applied',
  'failed',
  'superseded',
]

/** Natures affichées par la vue Propositions (hors déduplication et réparation, qui ont leur vue). */
export const REVIEW_KINDS: readonly ProposalKind[] = [
  'full',
  'partial_annex',
  'partial_pack',
  'cross_seed',
  'conflict',
]
