import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { failure, installFakeApi, resetAppState } from '@/test/fakeApi'
import { authToken, loginRejected, loginRequired, request, setToken } from './client'
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

  it('Client_asksForTheTokenOnlyAfterA401', async () => {
    installFakeApi({ 'GET /api/status': () => failure(401, 'unauthorized') })

    const gotError = await request('GET', '/api/status').catch((error: unknown) => error)

    expect(gotError).toBeInstanceOf(ApiError)
    expect((gotError as ApiError).code).toBe('unauthorized')
    expect(loginRequired.value).toBe(true)
    setToken('x')
    expect(loginRequired.value).toBe(false)
    expect(authToken.value).toBe('x')
  })

  it('Client_forgetsARejectedTokenAndSaysSo', async () => {
    installFakeApi({ 'GET /api/status': () => failure(401, 'unauthorized') })
    setToken('wrong')

    await Promise.all([
      request('GET', '/api/status').catch(() => undefined),
      request('GET', '/api/status').catch(() => undefined),
    ])

    expect(authToken.value).toBe('')
    expect(loginRejected.value).toBe(true)
    expect(loginRequired.value).toBe(true)
  })

  it('Client_doesNotAskForATokenWhenTheServerAnswers', async () => {
    installFakeApi({ 'GET /api/status': { ok: true } })

    await request('GET', '/api/status')

    expect(loginRequired.value).toBe(false)
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
