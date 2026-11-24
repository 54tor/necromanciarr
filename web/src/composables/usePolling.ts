import { onScopeDispose, watch, type Ref } from 'vue'

/** usePolling exécute `task` toutes les `intervalMs` tant que `active` est vrai, et s'arrête au démontage. */
export function usePolling(
  task: () => void | Promise<void>,
  intervalMs: number,
  active: Ref<boolean>,
): void {
  let timer: ReturnType<typeof setInterval> | undefined
  const stop = () => {
    clearInterval(timer)
    timer = undefined
  }
  watch(
    active,
    (isActive) => {
      stop()
      if (isActive) timer = setInterval(task, intervalMs)
    },
    { immediate: true },
  )
  onScopeDispose(stop)
}
