import { computed, ref } from 'vue'
import { api } from '@/api/endpoints'
import type { DoctorCheck, DoctorReport, DoctorStatus, Status } from '@/api/types'
import { setLanguage } from '@/i18n'
import { onboardingSkipped } from '@/state/session'
import { useApi } from './useApi'

// État partagé entre l'accueil et le garde de redirection de l'application.
const checks = ref<DoctorCheck[]>([])
const status = ref<Status | undefined>(undefined)
const loaded = ref(false)
/** Exposé pour que les tests remettent l'état partagé à zéro entre deux cas. */
export const onboardingState = { checks, status, loaded }

function worstOf(list: DoctorCheck[]): DoctorStatus {
  if (list.some((check) => check.status === 'fail')) return 'fail'
  if (list.some((check) => check.status === 'warn')) return 'warn'
  return 'ok'
}

function isFirstLaunch(current: Status | undefined): boolean {
  if (!current) return false
  const total = (counts: Record<string, number | undefined>) =>
    Object.values(counts).reduce<number>((sum, count) => sum + (count ?? 0), 0)
  return total(current.candidates) === 0 && total(current.proposals) === 0
}

/** applyStatus mémorise le statut et bascule l'interface dans la langue du serveur (première réponse authentifiée). */
function applyStatus(current: Status): void {
  status.value = current
  setLanguage(current.language)
}

export function useOnboarding() {
  const { call, pending } = useApi()
  const worst = computed(() => worstOf(checks.value))
  const firstLaunch = computed(() => isFirstLaunch(status.value))
  const needsOnboarding = computed(
    () => loaded.value && !onboardingSkipped.value && (worst.value === 'fail' || firstLaunch.value),
  )

  async function loadAll(): Promise<void> {
    const [report, currentStatus] = await Promise.all([
      call(() => api.doctor()),
      call(() => api.status()),
    ])
    if (report) checks.value = report.checks
    if (currentStatus) applyStatus(currentStatus)
    loaded.value = Boolean(report)
  }

  async function rerunCheck(checkId: string): Promise<void> {
    const report: DoctorReport | undefined = await call(() => api.doctorCheck(checkId))
    if (!report) return
    const fresh = new Map(report.checks.map((check) => [check.id, check]))
    checks.value = checks.value.map((check) => fresh.get(check.id) ?? check)
  }

  async function refreshStatus(): Promise<void> {
    const currentStatus = await call(() => api.status())
    if (currentStatus) applyStatus(currentStatus)
  }

  function skip(): void {
    onboardingSkipped.value = true
  }

  return {
    checks,
    status,
    worst,
    firstLaunch,
    needsOnboarding,
    pending,
    loadAll,
    rerunCheck,
    refreshStatus,
    skip,
  }
}
