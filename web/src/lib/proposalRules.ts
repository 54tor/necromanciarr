import type { Decision, Proposal } from '@/api/types'

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

const GROUP_ACCEPTED_KINDS: readonly Proposal['kind'][] = ['full', 'cross_seed']

export function decisionRank(decision: Decision): number {
  return DECISION_RANKS[decision]
}

const isOpen = (proposal: Proposal) =>
  proposal.current && OPEN_DECISIONS.includes(proposal.decision)

/** Un conflit se tranche par « choisir », jamais par « accepter ». */
export const canAccept = (proposal: Proposal) => isOpen(proposal) && proposal.kind !== 'conflict'

/** Le serveur ne rejette que depuis `pending`. */
export const canReject = (proposal: Proposal) => proposal.current && proposal.decision === 'pending'

export const canChoose = (proposal: Proposal) => isOpen(proposal) && proposal.kind === 'conflict'

export const canAcceptGroup = (proposal: Proposal) => isOpen(proposal) && proposal.primary

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

/** Une proposition de torrent du groupe que « accepter le groupe » peut accepter, d'après la liste chargée. */
export function groupMembers(proposals: Proposal[], groupId: number): Proposal[] {
  return proposals
    .filter(
      (member) =>
        member.group_id === groupId && GROUP_ACCEPTED_KINDS.includes(member.kind) && isOpen(member),
    )
    .sort((first, second) => Number(second.primary) - Number(first.primary))
}

/** La colonne et le filtre Source n'existent que si le serveur envoie le champ (ajouté par la tâche B14). */
export function hasSourceField(proposals: Proposal[]): boolean {
  return proposals.some((proposal) => proposal.source !== undefined)
}
