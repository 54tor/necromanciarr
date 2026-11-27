import { ref } from 'vue'
import { ApiError, NETWORK_ERROR_CODE } from './errors'
import type { ApiErrorBody } from './types'

/**
 * Jeton d'amorçage saisi au premier lancement : en mémoire du module seulement, jamais écrit dans un stockage, une URL
 * ni un cookie, et effacé dès que le compte existe (le serveur le refuse alors).
 */
export const authToken = ref('')
/** Vrai quand le jeton saisi vient d'être refusé : la boîte de saisie le dit. */
export const tokenRejected = ref(false)
/** Compteur incrémenté à chaque 401 qui signifie « déconnecté » : l'application relit alors l'état de la session. */
export const sessionLost = ref(0)

const JSON_CONTENT_TYPE = 'application/json'
const CSRF_HEADER_VALUE = 'necromanciarr'
const BOOTSTRAP_TOKEN_REFUSED = 'unauthorized'
const SIGNED_OUT_CODES = ['session_required', 'bearer_disabled']

function headersFor(method: string): Record<string, string> {
  const headers: Record<string, string> = { Accept: JSON_CONTENT_TYPE }
  if (authToken.value) headers.Authorization = `Bearer ${authToken.value}`
  if (method !== 'GET') {
    headers['Content-Type'] = JSON_CONTENT_TYPE
    headers['X-Requested-With'] = CSRF_HEADER_VALUE
  }
  return headers
}

async function readError(response: Response): Promise<ApiErrorBody> {
  try {
    const payload = (await response.json()) as { error?: ApiErrorBody }
    if (payload.error?.code) return payload.error
  } catch {
    // Corps non JSON : on retombe sur le statut HTTP.
  }
  return { code: 'failure', message: response.statusText }
}

function retryAfterOf(response: Response): number | undefined {
  const seconds = Number(response.headers.get('Retry-After'))
  return Number.isFinite(seconds) && seconds > 0 ? Math.ceil(seconds) : undefined
}

/** Seul le code distingue un 401 « déconnecté » d'un 401 de formulaire (mot de passe ressaisi faux, code refusé). */
function noteUnauthorized(code: string): void {
  if (code === BOOTSTRAP_TOKEN_REFUSED) {
    // Deux requêtes parallèles peuvent échouer : la seconde ne doit pas effacer le constat de la première.
    if (authToken.value !== '') tokenRejected.value = true
    authToken.value = ''
    return
  }
  if (!SIGNED_OUT_CODES.includes(code)) return
  authToken.value = ''
  sessionLost.value += 1
}

/** request appelle l'API ; tout appel autre que GET porte un corps JSON (au minimum `{}`) et l'en-tête anti-CSRF. */
export async function request<Result>(
  method: string,
  path: string,
  body?: unknown,
): Promise<Result> {
  const init: RequestInit = { method, headers: headersFor(method) }
  if (method !== 'GET') init.body = JSON.stringify(body ?? {})
  let response: Response
  try {
    response = await fetch(path, init)
  } catch {
    throw new ApiError(0, { code: NETWORK_ERROR_CODE, message: 'network error' })
  }
  if (response.ok) return (await response.json()) as Result
  const failure = await readError(response)
  if (response.status === 401) noteUnauthorized(failure.code)
  throw new ApiError(response.status, failure, retryAfterOf(response))
}

/** setToken mémorise le jeton d'amorçage saisi. */
export function setToken(token: string): void {
  authToken.value = token
  tokenRejected.value = false
}

/** dropToken oublie le jeton d'amorçage : une fois le compte créé, seul le cookie de session ouvre l'API. */
export function dropToken(): void {
  authToken.value = ''
}
