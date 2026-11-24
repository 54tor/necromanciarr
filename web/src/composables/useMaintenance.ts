import { ref, watch, type Ref } from 'vue'
import { api } from '@/api/endpoints'
import type { Job, MaintenanceProposal, Proposal } from '@/api/types'
import { lastFinished, useJobs } from './useJobs'
import { useApi } from './useApi'

type ApplyRoute = (proposalId: number, dryRun: boolean, acceptPending: boolean) => Promise<Job>

/**
 * useMaintenance porte la liste et l'application des propositions dedupe ou repair : même cycle (plan ou scan,
 * puis application une par une en tâche de fond), seuls le détail et la route d'application changent.
 */
export function useMaintenance<Details extends { actionable: boolean }>(
  kind: 'dedupe' | 'repair',
  route: ApplyRoute,
) {
  const { call, pending } = useApi()
  const jobsState = useJobs()
  const items = ref([]) as Ref<MaintenanceProposal<Details>[]>
  const dryRun = ref(false)

  async function load(): Promise<void> {
    const list = await call(() => api.listProposals({ kind }))
    if (!list) return
    items.value = list.map((proposal: Proposal) => ({
      id: proposal.id,
      decision: proposal.decision,
      warnings: proposal.warnings,
      details: proposal.maintenance as unknown as Details,
    }))
  }

  const canApply = (item: MaintenanceProposal<Details>) =>
    item.details.actionable && ['pending', 'accepted', 'failed'].includes(item.decision)

  async function applyOne(item: MaintenanceProposal<Details>): Promise<Job | undefined> {
    const acceptPending = item.decision !== 'accepted'
    return jobsState.start(() => route(item.id, dryRun.value, acceptPending))
  }

  /** applyAccepted applique à la suite les propositions déjà acceptées ; s'arrête au premier échec. */
  async function applyAccepted(): Promise<void> {
    for (const item of items.value.filter(
      (candidate) => candidate.decision === 'accepted' && canApply(candidate),
    )) {
      const job = await applyOne(item)
      if (!job) return
      const finished = await jobsState.waitFor(job)
      if (finished.state === 'failed') return
    }
  }

  watch(lastFinished, () => void load())

  return {
    items,
    dryRun,
    pending,
    busy: jobsState.busy,
    load,
    canApply,
    applyOne,
    applyAccepted,
    start: jobsState.start,
  }
}
