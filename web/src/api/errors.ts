import { t, te } from '@/i18n'
import type { ApiErrorBody } from './types'

/** Erreur renvoyée par l'API (ou transport) : code stable, statut HTTP, message du serveur conservé pour le débogage. */
export class ApiError extends Error {
  readonly code: string
  readonly status: number

  constructor(status: number, body: ApiErrorBody) {
    super(body.message)
    this.name = 'ApiError'
    this.status = status
    this.code = body.code
  }
}

export const NETWORK_ERROR_CODE = 'network'

/**
 * messageForCode donne le texte d'un code d'erreur de l'API dans la langue active. Un code inconnu donne le message
 * générique suivi du code : le texte du serveur n'est jamais affiché (il peut contenir un chemin ou un secret).
 */
export function messageForCode(code: string): string {
  const key = `errors.${code}`
  return te(key) ? t(key) : t('errors.unknownCode', { message: t('errors.generic'), code })
}

export function messageForError(error: unknown): string {
  return error instanceof ApiError ? messageForCode(error.code) : t('errors.generic')
}
