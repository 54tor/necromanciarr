import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { failure, installFakeApi, resetAppState } from '@/test/fakeApi'
import { authToken, dropToken, request, sessionLost, setToken, tokenRejected } from './client'
import { ApiError } from './errors'

describe('Client', () => {
  beforeEach(resetAppState)
  afterEach(() => vi.unstubAllGlobals())

  it('Client_sendsBearerTokenAndNeverStoresItInLocalStorage', async () => {
    const setItem = vi.spyOn(Storage.prototype, 'setItem')
    const calls = installFakeApi({ 'GET /api/status': {}, 'POST /api/proposals/{id}/reject': {} })
    setToken('secret-token-value')

    await request('GET', '/api/status')
    await request('POST', '/api/proposals/3/reject', {})

    expect(calls[0]?.headers.Authorization).toBe('Bearer secret-token-value')
    expect(calls[1]?.headers.Authorization).toBe('Bearer secret-token-value')
    expect(setItem).not.toHaveBeenCalled()
    expect(document.cookie).not.toContain('secret-token-value')
    expect(JSON.stringify({ ...localStorage, ...sessionStorage })).not.toContain(
      'secret-token-value',
    )
    expect(calls.map((call) => call.path).join()).not.toContain('secret-token-value')
    setItem.mockRestore()
  })

  it('Client_sendsJsonAndCsrfHeadersOnPostOnly', async () => {
    const calls = installFakeApi({ 'GET /api/status': {}, 'POST /api/dedupe/plan': {} })

    await request('GET', '/api/status')
    await request('POST', '/api/dedupe/plan')

    expect(calls[0]?.headers).toEqual({ Accept: 'application/json' })
    expect(calls[1]?.headers['Content-Type']).toBe('application/json')
    expect(calls[1]?.headers['X-Requested-With']).toBe('necromanciarr')
    expect(calls[1]?.body).toEqual({})
  })

  it('Client_forgetsARejectedBootstrapTokenAndSaysSo', async () => {
    installFakeApi({ 'GET /api/status': () => failure(401, 'unauthorized') })
    setToken('wrong')

    await Promise.all([
      request('GET', '/api/status').catch(() => undefined),
      request('GET', '/api/status').catch(() => undefined),
    ])

    expect(authToken.value).toBe('')
    expect(tokenRejected.value).toBe(true)
    expect(sessionLost.value).toBe(0)
  })

  it.each(['session_required', 'bearer_disabled'])(
    'Client_reportsASignedOutSessionOnA401With%s',
    async (code) => {
      installFakeApi({ 'GET /api/status': () => failure(401, code) })
      setToken('stale-bootstrap-token')
      const before = sessionLost.value

      await request('GET', '/api/status').catch(() => undefined)

      expect(sessionLost.value).toBe(before + 1)
      expect(authToken.value).toBe('')
    },
  )

  it('Client_keepsTheSessionOnA401FormError', async () => {
    installFakeApi({ 'POST /api/auth/password': () => failure(401, 'invalid_credentials') })
    const before = sessionLost.value

    const gotError = await request('POST', '/api/auth/password', {}).catch(
      (error: unknown) => error,
    )

    expect((gotError as ApiError).code).toBe('invalid_credentials')
    expect(sessionLost.value).toBe(before)
  })

  it('Client_readsTheRetryAfterDelayOfAThrottledCall', async () => {
    installFakeApi({
      'POST /api/auth/login': () => failure(429, 'throttled', 'raw', { 'Retry-After': '7' }),
    })

    const gotError = await request('POST', '/api/auth/login', {}).catch((error: unknown) => error)

    expect((gotError as ApiError).retryAfterSeconds).toBe(7)
  })

  it.each([
    ['999999999', 3600],
    ['-5', undefined],
    ['soon', undefined],
  ])('Client_boundsTheRetryAfterDelay %s', async (header, valeurAttendue) => {
    installFakeApi({
      'POST /api/auth/login': () => failure(429, 'throttled', 'raw', { 'Retry-After': header }),
    })

    const gotError = await request('POST', '/api/auth/login', {}).catch((error: unknown) => error)

    const valeurObtenue = (gotError as ApiError).retryAfterSeconds
    expect(valeurObtenue).toBe(valeurAttendue)
  })

  it('Client_sendsNoBearerOnceTheTokenIsDropped', async () => {
    const calls = installFakeApi({ 'GET /api/status': {} })
    setToken('bootstrap')
    dropToken()

    await request('GET', '/api/status')

    expect(calls[0]?.headers.Authorization).toBeUndefined()
  })

  it('Client_turnsATransportFailureIntoANetworkError', async () => {
    vi.stubGlobal('fetch', () => Promise.reject(new TypeError('offline')))

    const gotError = await request('GET', '/api/status').catch((error: unknown) => error)

    expect((gotError as ApiError).code).toBe('network')
  })

  it('Client_keepsTheStatusWhenTheErrorBodyIsNotJson', async () => {
    vi.stubGlobal(
      'fetch',
      async () => new Response('<html>', { status: 502, statusText: 'Bad Gateway' }),
    )

    const gotError = await request('GET', '/api/status').catch((error: unknown) => error)

    expect((gotError as ApiError).code).toBe('failure')
    expect((gotError as ApiError).status).toBe(502)
  })
})
