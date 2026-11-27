import { setLanguage } from '@/i18n'
import { vi } from 'vitest'
import { authToken, tokenRejected } from '@/api/client'
import { resetAuthState } from '@/composables/useAuth'
import { onboardingState } from '@/composables/useOnboarding'
import { jobs } from '@/composables/useJobs'
import { resetSetupState } from '@/composables/useSetup'
import { onboardingSkipped } from '@/state/session'
import { authenticatedSession } from './authFixtures'

export interface FakeCall {
  method: string
  path: string
  query: URLSearchParams
  body: unknown
  headers: Record<string, string>
}

type Handler = (call: FakeCall) => unknown
type Routes = Record<string, Handler | object>

/** failure fabrique la réponse d'erreur de l'API : {"error": {"code", "message"}}, avec ses en-têtes éventuels. */
export function failure(
  status: number,
  code: string,
  message = 'raw server text',
  headers: Record<string, string> = {},
): Response {
  return new Response(JSON.stringify({ error: { code, message } }), { status, headers })
}

function matches(pattern: string, method: string, path: string): boolean {
  const [patternMethod = '', patternPath = ''] = pattern.split(' ')
  const expression = new RegExp(`^${patternPath.replace(/\{[^}]+\}/g, '[^/]+')}$`)
  return patternMethod === method && expression.test(path)
}

/**
 * installFakeApi remplace fetch : chaque route « METHODE /chemin/{id} » rend un JSON ou une Response ; les appels sont
 * journalisés. Sans route `GET /api/auth/session`, le serveur factice répond une session ouverte.
 */
export function installFakeApi(givenRoutes: Routes): FakeCall[] {
  const routes: Routes = { 'GET /api/auth/session': authenticatedSession, ...givenRoutes }
  const calls: FakeCall[] = []
  vi.stubGlobal('fetch', async (input: string, init: RequestInit = {}) => {
    const url = new URL(input, 'http://localhost')
    const call: FakeCall = {
      method: init.method ?? 'GET',
      path: url.pathname,
      query: url.searchParams,
      body: init.body ? JSON.parse(String(init.body)) : undefined,
      headers: (init.headers ?? {}) as Record<string, string>,
    }
    calls.push(call)
    const pattern = Object.keys(routes).find((candidate) =>
      matches(candidate, call.method, call.path),
    )
    if (!pattern) return failure(404, 'not_found')
    const route = routes[pattern]
    const payload = typeof route === 'function' ? (route as Handler)(call) : route
    return payload instanceof Response
      ? payload
      : new Response(JSON.stringify(payload), { status: 200 })
  })
  return calls
}

/** resetAppState remet à zéro l'état partagé des modules (jeton, compte, jobs, diagnostic, « passer », hash, langue). */
export function resetAppState(): void {
  authToken.value = ''
  tokenRejected.value = false
  resetAuthState()
  onboardingSkipped.value = false
  onboardingState.checks.value = []
  onboardingState.status.value = undefined
  onboardingState.loaded.value = false
  jobs.value = []
  resetSetupState()
  setLanguage('fr')
  window.location.hash = ''
}
