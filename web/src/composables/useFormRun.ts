import { ref } from 'vue'
import type { AuthOutcome } from '@/composables/useAuth'
import { t } from '@/i18n'

/** useFormRun porte l'état d'un formulaire du compte : appel en cours, message d'erreur ou de réussite. */
export function useFormRun() {
  const message = ref('')
  const succeeded = ref(false)
  const busy = ref(false)

  async function run(task: () => Promise<AuthOutcome>, successKey = ''): Promise<boolean> {
    busy.value = true
    message.value = ''
    const outcome = await task()
    busy.value = false
    succeeded.value = outcome.ok
    if (outcome.ok) message.value = successKey ? t(successKey) : ''
    else message.value = outcome.message
    return outcome.ok
  }

  function clear(): void {
    message.value = ''
  }

  return { message, succeeded, busy, run, clear }
}
