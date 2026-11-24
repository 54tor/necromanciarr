import { messageForCode } from '@/api/errors'
import type { Job, StageName, StageResult } from '@/api/types'
import { t } from '@/i18n'

export interface SummaryLine {
  label: string
  value: number
}

/** Compteurs du résultat propres à chaque étape, dans l'ordre d'affichage. */
const STAGE_COUNTERS: Readonly<Record<StageName, readonly (keyof StageResult)[]>> = {
  scan: ['processed', 'skipped', 'failed'],
  match: ['candidates_created', 'candidates_updated', 'candidates_stale', 'unidentified_files'],
  verify: ['verified', 'partial_annex', 'partial_pack', 'conflicts', 'proposals_created'],
  link: ['linked', 'failed'],
  seed: ['seeded', 'seeded_paused_incomplete', 'failed'],
}

/** stageResultOf rend le résultat d'étape d'un job `run` terminé, sinon undefined. */
export function stageResultOf(job: Job): StageResult | undefined {
  const result = job.result as StageResult | undefined
  return job.action === 'run' && result?.stage ? result : undefined
}

/** jobActionLabel nomme l'action d'un job : l'étape lancée si le résultat la donne, sinon « Étape » pour `run`. */
export function jobActionLabel(job: Job): string {
  const result = stageResultOf(job)
  if (result) return t(`stages.names.${result.stage}`)
  return job.action === 'run' ? t('stages.names.run') : job.action
}

export function counterLines(result: StageResult): SummaryLine[] {
  return (STAGE_COUNTERS[result.stage] ?? []).map((counter) => ({
    label: t(`stages.counters.${counter}`),
    value: Number(result[counter]),
  }))
}

/** reasonLines rend les motifs avec le libellé du serveur ; un motif sans libellé garde son code. */
export function reasonLines(result: StageResult): SummaryLine[] {
  return Object.entries(result.reasons ?? {}).map(([code, count]) => ({
    label: result.reason_labels?.[code] ?? code,
    value: count,
  }))
}

/** summaryText résume un job en une ligne : compteurs non nuls et motifs, ou l'erreur d'un job en échec. */
export function summaryText(job: Job): string {
  if (job.state === 'failed') return messageForCode(job.error?.code ?? 'failure')
  const result = stageResultOf(job)
  if (!result) return '—'
  const lines = [...counterLines(result), ...reasonLines(result)].filter((line) => line.value > 0)
  if (lines.length === 0) return t('stages.noCounters')
  return lines
    .map((line) => t('common.labelValue', { label: line.label, value: line.value }))
    .join(', ')
}
