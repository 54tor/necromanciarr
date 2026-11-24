import { ref } from 'vue'
import { ApiError, NETWORK_ERROR_CODE } from './errors'
import type { ApiErrorBody } from './types'

/** Jeton saisi par l'opérateur : en mémoire du module seulement, jamais écrit dans un stockage, une URL ni un cookie. */
export const authToken = ref('')
/** Vrai quand le serveur a répondu 401 : l'application affiche alors la boîte de saisie du jeton. */
export const loginRequired = ref(false)
/** Vrai quand un jeton saisi vient d'être refusé : la boîte de saisie le dit. */
export const loginRejected = ref(false)

const JSON_CONTENT_TYPE = 'application/json'
const CSRF_HEADER_VALUE = 'necromanciarr'

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

/** request appelle l'API ; tout POST porte un corps JSON (au minimum `{}`) et l'en-tête anti-CSRF. */
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
  if (response.status === 401) {
    // Deux requêtes parallèles peuvent échouer : la seconde ne doit pas effacer le constat de la première.
    if (authToken.value !== '') loginRejected.value = true
    authToken.value = ''
    loginRequired.value = true
  }
  throw new ApiError(response.status, failure)
}

/** setToken mémorise le jeton saisi et ferme la demande de connexion. */
export function setToken(token: string): void {
  authToken.value = token
  loginRejected.value = false
  loginRequired.value = false
}
