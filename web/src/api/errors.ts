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

const GENERIC_MESSAGE = 'Une erreur est survenue. Consultez le journal du serveur.'

// Table stable code -> message français. Un code inconnu ne montre jamais le texte du serveur.
const MESSAGES: Record<string, string> = {
  usage: 'Requête invalide.',
  confirmation_required: 'Confirmation requise pour cette action.',
  unauthorized: 'Jeton refusé ou absent : saisissez le jeton d’accès.',
  forbidden: 'Accès refusé par le serveur (origine ou hôte non autorisé).',
  not_found: 'Élément introuvable : il a peut-être été remplacé, actualisez la liste.',
  method_not_allowed: 'Méthode non autorisée.',
  stale_proposal: 'Proposition périmée : relancez la vérification.',
  not_configured: 'Composant non configuré : voir l’onglet Démarrage.',
  stage_running: 'Une étape est déjà en cours.',
  inspection_busy:
    'Un examen des liens est encore en cours (montage lent ou figé) : réessayez plus tard.',
  refused: 'Action refusée : elle n’est pas permise pour cette proposition.',
  shutting_down: 'Le serveur est en cours d’arrêt.',
  failure: 'Échec côté serveur. Consultez son journal.',
  [NETWORK_ERROR_CODE]: 'Serveur injoignable.',
}

/** messageForCode donne le texte français d'un code d'erreur de l'API ; un code inconnu donne un message générique. */
export function messageForCode(code: string): string {
  return MESSAGES[code] ?? `${GENERIC_MESSAGE} (code : ${code})`
}

export function messageForError(error: unknown): string {
  return error instanceof ApiError ? messageForCode(error.code) : GENERIC_MESSAGE
}
