import { computed, onScopeDispose, ref } from 'vue'
import { toast } from 'vue-sonner'
import { api } from '@/api/endpoints'
import { messageForCode } from '@/api/errors'
import type { Job } from '@/api/types'
import { useApi } from './useApi'
import { usePolling } from './usePolling'

const POLL_INTERVAL_MS = 2000
const WAIT_TIMEOUT_MS = 30 * 60 * 1000
const UNREACHABLE_AFTER_FAILED_POLLS = 3

// État partagé par toutes les vues : le serveur n'exécute qu'un job à la fois.
/** Exposé pour que les tests remettent l'état partagé à zéro entre deux cas. */
export const jobs = ref<Job[]>([])
/** Dernier job terminé : les vues le surveillent pour recharger leurs données. */
export const lastFinished = ref<Job | undefined>(undefined)
const waiters = new Map<number, (job: Job | undefined) => void>()
/** Vrai pendant l'envoi d'un lancement (avant la réponse 202) : bloque les doubles clics. */
const starting = ref(false)
const failedPolls = ref(0)
let pollInFlight = false
const runningJob = computed(() => jobs.value.find((job) => job.state === 'running'))

function announceFinished(previous: Job[], current: Job[]): void {
  const wasRunning = new Set(previous.filter((job) => job.state === 'running').map((job) => job.id))
  for (const job of current) {
    if (job.state === 'running' || !wasRunning.has(job.id)) continue
    lastFinished.value = job
    waiters.get(job.id)?.(job)
    if (job.state === 'done') toast.success('Tâche terminée.')
    else toast.error(messageForCode(job.error?.code ?? 'failure'))
  }
}

async function refreshJobs(): Promise<void> {
  if (pollInFlight) return
  pollInFlight = true
  try {
    const current = await api.listJobs()
    announceFinished(jobs.value, current)
    jobs.value = current
    failedPolls.value = 0
  } catch {
    failedPolls.value += 1
  } finally {
    pollInFlight = false
  }
}

/** useJobs suit les jobs du serveur : interrogation toutes les 2 s tant qu'un job tourne, arrêtée au démontage. */
export function useJobs() {
  const { call } = useApi()
  const busy = computed(() => runningJob.value !== undefined || starting.value)
  // Après plusieurs sondages en échec, l'état « en cours » n'est plus fiable : on le dit.
  const unreachable = computed(() => failedPolls.value >= UNREACHABLE_AFTER_FAILED_POLLS)
  const ownWaiters = new Set<number>()
  usePolling(
    refreshJobs,
    POLL_INTERVAL_MS,
    computed(() => runningJob.value !== undefined),
  )

  /** start lance une action longue (réponse 202 avec le job) et la suit. */
  async function start(launch: () => Promise<Job>): Promise<Job | undefined> {
    starting.value = true
    try {
      const job = await call(launch)
      if (job) jobs.value = [job, ...jobs.value.filter((known) => known.id !== job.id)]
      return job
    } finally {
      starting.value = false
    }
  }

  /** waitFor rend le job terminé, ou undefined après le délai ou au démontage de la vue (la chaîne s'arrête). */
  function waitFor(job: Job): Promise<Job | undefined> {
    return new Promise((resolve) => {
      const timer = setTimeout(() => settle(undefined), WAIT_TIMEOUT_MS)
      function settle(result: Job | undefined): void {
        clearTimeout(timer)
        waiters.delete(job.id)
        ownWaiters.delete(job.id)
        resolve(result)
      }
      waiters.set(job.id, settle)
      ownWaiters.add(job.id)
    })
  }

  onScopeDispose(() => [...ownWaiters].forEach((jobId) => waiters.get(jobId)?.(undefined)))

  return { jobs, runningJob, busy, unreachable, start, waitFor, refresh: refreshJobs }
}
