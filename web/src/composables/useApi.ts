import { ref } from 'vue'
import { toast } from 'vue-sonner'
import { ApiError, detailForError, messageForError } from '@/api/errors'

/**
 * useApi enveloppe un appel : une erreur de l'API devient un toast français (table de api/errors.ts) et l'appel rend
 * `undefined`. `pending` sert à désactiver les boutons pendant la requête.
 */
function showError(error: unknown): void {
  const detail = detailForError(error)
  if (detail) {
    toast.error(messageForError(error), { description: detail })
    return
  }
  toast.error(messageForError(error))
}

const SIGNED_OUT_CODES = ['unauthorized', 'session_required', 'bearer_disabled']

export function useApi() {
  const pending = ref(false)

  async function call<Result>(task: () => Promise<Result>): Promise<Result | undefined> {
    pending.value = true
    try {
      return await task()
    } catch (error) {
      // Ces 401 ramènent au jeton ou à la connexion : un toast en plus serait redondant.
      if (!(error instanceof ApiError && SIGNED_OUT_CODES.includes(error.code))) showError(error)
      return undefined
    } finally {
      pending.value = false
    }
  }

  return { call, pending }
}
