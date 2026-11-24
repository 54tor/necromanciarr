import { computed, ref } from 'vue'
import { toast } from 'vue-sonner'
import { api } from '@/api/endpoints'
import { messageForCode } from '@/api/errors'
import type { Job } from '@/api/types'
import { useApi } from './useApi'
import { usePolling } from './usePolling'

const POLL_INTERVAL_MS = 2000

// État partagé par toutes les vues : le serveur n'exécute qu'un job à la fois.
/** Exposé pour que les tests remettent l'état partagé à zéro entre deux cas. */
export const jobs = ref<Job[]>([])
/** Dernier job terminé : les vues le surveillent pour recharger leurs données. */
export const lastFinished = ref<Job | undefined>(undefined)
const waiters = new Map<number, (job: Job) => void>()
const runningJob = computed(() => jobs.value.find((job) => job.state === 'running'))

function announceFinished(previous: Job[], current: Job[]): void {
  const wasRunning = new Set(previous.filter((job) => job.state === 'running').map((job) => job.id))
  for (const job of current) {
    if (job.state === 'running' || !wasRunning.has(job.id)) continue
    lastFinished.value = job
    waiters.get(job.id)?.(job)
    waiters.delete(job.id)
    if (job.state === 'done') toast.success('Tâche terminée.')
    else toast.error(messageForCode(job.error?.code ?? 'failure'))
  }
}

async function refreshJobs(): Promise<void> {
  try {
    const current = await api.listJobs()
    announceFinished(jobs.value, current)
    jobs.value = current
  } catch {
    // Un échec de sondage ne doit pas masquer l'état précédent ; le prochain tour réessaie.
  }
}

/** useJobs suit les jobs du serveur : interrogation toutes les 2 s tant qu'un job tourne, arrêtée au démontage. */
export function useJobs() {
  const { call } = useApi()
  const busy = computed(() => runningJob.value !== undefined)
  usePolling(refreshJobs, POLL_INTERVAL_MS, busy)

  /** start lance une action longue (réponse 202 avec le job) et la suit. */
  async function start(launch: () => Promise<Job>): Promise<Job | undefined> {
    const job = await call(launch)
    if (job) jobs.value = [job, ...jobs.value.filter((known) => known.id !== job.id)]
    return job
  }

  /** waitFor rend le job une fois terminé (utilisé pour enchaîner des applications). */
  function waitFor(job: Job): Promise<Job> {
    return new Promise((resolve) => waiters.set(job.id, resolve))
  }

  return { jobs, runningJob, busy, start, waitFor, refresh: refreshJobs }
}
