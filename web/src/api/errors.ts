import { t, te } from '@/i18n'
import type { ApiErrorBody } from './types'

/** Erreur renvoyée par l'API (ou transport) : code stable, statut HTTP, message du serveur conservé pour le débogage. */
export class ApiError extends Error {
  readonly code: string
  readonly status: number
  readonly detail?: string
  /** Délai d'attente annoncé par l'en-tête Retry-After (limiteur de connexion), en secondes. */
  readonly retryAfterSeconds?: number

  constructor(status: number, body: ApiErrorBody, retryAfterSeconds?: number) {
    super(body.message)
    this.name = 'ApiError'
    this.status = status
    this.code = body.code
    this.detail = body.detail
    this.retryAfterSeconds = retryAfterSeconds
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

/** detailForError rend la raison d'un refus d'écriture de configuration, affichée en texte brut à côté du message. */
export function detailForError(error: unknown): string | undefined {
  return error instanceof ApiError ? error.detail : undefined
}
