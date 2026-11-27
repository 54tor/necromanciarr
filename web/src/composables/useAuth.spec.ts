import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { authToken, sessionLost, setToken } from '@/api/client'
import { failure, installFakeApi, resetAppState } from '@/test/fakeApi'
import {
  authenticatedSession,
  keyOptions,
  noAccountSession,
  strongPassword,
} from '@/test/authFixtures'
import { stubWebAuthn, unstubWebAuthn } from '@/test/webauthn'
import { useAuth } from './useAuth'

const BOOTSTRAP_TOKEN = 'bootstrap-token-value'

describe('useAuth', () => {
  beforeEach(resetAppState)
  afterEach(() => {
    vi.unstubAllGlobals()
    unstubWebAuthn()
    vi.restoreAllMocks()
  })

  it('le jeton d’amorçage n’est plus envoyé une fois le compte créé', async () => {
    let accountExists = false
    const calls = installFakeApi({
      'GET /api/auth/session': () => (accountExists ? authenticatedSession : noAccountSession),
      'POST /api/auth/setup': () => {
        accountExists = true
        return new Response(JSON.stringify({ state: 'authenticated' }), { status: 201 })
      },
      'GET /api/auth/webauthn/credentials': [],
    })
    const auth = useAuth()
    await auth.load()
    setToken(BOOTSTRAP_TOKEN)

    const valeurObtenue = await auth.setup('alice', strongPassword, strongPassword)
    await auth.loadCredentials()

    expect(valeurObtenue).toEqual({ ok: true })
    expect(auth.status.value?.state).toBe('authenticated')
    expect(authToken.value).toBe('')
    const setupCall = calls.find((call) => call.path === '/api/auth/setup')
    expect(setupCall?.headers.Authorization).toBe(`Bearer ${BOOTSTRAP_TOKEN}`)
    const callsAfterSetup = calls.slice(calls.indexOf(setupCall!) + 1)
    expect(callsAfterSetup.length).toBeGreaterThan(0)
    expect(callsAfterSetup.every((call) => call.headers.Authorization === undefined)).toBe(true)
  })

  it('n’enregistre ni mot de passe, ni jeton, ni code dans le stockage du navigateur', async () => {
    const setItem = vi.spyOn(Storage.prototype, 'setItem')
    const cookieWrites = vi.spyOn(Document.prototype, 'cookie', 'set')
    stubWebAuthn()
    let state = 'no_account'
    installFakeApi({
      'GET /api/auth/session': () => ({ ...authenticatedSession, state }),
      'POST /api/auth/setup': () => {
        state = 'logged_out'
        return { state: 'authenticated' }
      },
      'POST /api/auth/login': { state: 'second_factor_required', factors: ['totp'] },
      'POST /api/auth/totp/login': () => {
        state = 'authenticated'
        return { state: 'authenticated' }
      },
      'POST /api/auth/totp/setup/begin': {
        otpauthUri: 'otpauth://x',
        secret: 'SECRETBASE32',
        qrPngBase64: null,
      },
      'POST /api/auth/totp/setup/finish': {},
      'POST /api/auth/passkey/login/begin': { state: 'logged_out', webauthn: keyOptions },
      'POST /api/auth/passkey/login/finish': { state: 'authenticated' },
    })
    const auth = useAuth()
    setToken(BOOTSTRAP_TOKEN)

    await auth.setup('alice', strongPassword, strongPassword)
    await auth.login('alice', strongPassword)
    await auth.finishWithTotp('123456')
    await auth.beginTotp(strongPassword)
    await auth.confirmTotp('654321')
    await auth.loginWithPasskey()

    const storedText = JSON.stringify({ ...localStorage, ...sessionStorage }) + document.cookie
    for (const secret of [BOOTSTRAP_TOKEN, strongPassword, '123456', '654321', 'SECRETBASE32'])
      expect(storedText).not.toContain(secret)
    expect(setItem).not.toHaveBeenCalled()
    expect(cookieWrites).not.toHaveBeenCalled()
    expect(auth.totpEnrolment.value).toBeUndefined()
  })

  it('un 401 avec le code invalid_credentials sur une route de session n’est pas une déconnexion', async () => {
    const calls = installFakeApi({
      'GET /api/auth/session': authenticatedSession,
      'POST /api/auth/password': () => failure(401, 'invalid_credentials'),
      'GET /api/auth/sessions': () => failure(401, 'session_required'),
    })
    const auth = useAuth()
    await auth.load()
    const signalsBefore = sessionLost.value

    const valeurObtenue = await auth.changePassword(
      'wrong-password-0',
      strongPassword,
      strongPassword,
    )
    await Promise.resolve()

    expect(valeurObtenue).toEqual({ ok: false, message: 'Identifiant ou mot de passe incorrect.' })
    expect(sessionLost.value).toBe(signalsBefore)
    expect(calls.filter((call) => call.path === '/api/auth/session')).toHaveLength(1)

    await auth.loadSessions()

    expect(sessionLost.value).toBe(signalsBefore + 1)
  })

  it('un 401 session_required relit l’état de la session', async () => {
    let state = 'authenticated'
    installFakeApi({
      'GET /api/auth/session': () => ({ ...authenticatedSession, state }),
      'GET /api/auth/sessions': () => {
        state = 'logged_out'
        return failure(401, 'session_required')
      },
    })
    const auth = useAuth()
    await auth.load()

    await auth.loadSessions()
    await vi.waitFor(() => expect(auth.status.value?.state).toBe('logged_out'))
  })

  it('un code inconnu donne le message générique', async () => {
    installFakeApi({ 'POST /api/auth/logout-all': () => failure(500, 'brand_new_code') })

    const valeurObtenue = await useAuth().logoutAll()

    expect(valeurObtenue).toEqual({ ok: false, message: 'Échec de l’opération.' })
  })
})
