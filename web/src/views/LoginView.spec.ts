import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useAuth } from '@/composables/useAuth'
import { failure, installFakeApi, resetAppState } from '@/test/fakeApi'
import {
  authenticatedSession,
  keyOptions,
  loggedOutSession,
  strongPassword,
} from '@/test/authFixtures'
import { fakeCredentialJson, stubWebAuthn, unstubWebAuthn } from '@/test/webauthn'
import LoginView from './LoginView.vue'

async function mountLogin(): Promise<VueWrapper> {
  await useAuth().load()
  const wrapper = mount(LoginView)
  await flushPromises()
  return wrapper
}

async function submitPassword(wrapper: VueWrapper, password = strongPassword): Promise<void> {
  await wrapper.get('[data-testid="login-username"]').setValue('alice')
  await wrapper.get('[data-testid="login-password"]').setValue(password)
  await wrapper.get('form[data-testid="password-step"]').trigger('submit')
  await flushPromises()
}

function passwordValue(wrapper: VueWrapper): string {
  return (wrapper.get('[data-testid="login-password"]').element as HTMLInputElement).value
}

describe('LoginView', () => {
  beforeEach(resetAppState)
  afterEach(() => {
    vi.unstubAllGlobals()
    unstubWebAuthn()
    vi.useRealTimers()
  })

  it('déclare les champs pour le gestionnaire de mots de passe', async () => {
    installFakeApi({ 'GET /api/auth/session': loggedOutSession })
    const wrapper = await mountLogin()

    expect(wrapper.get('[data-testid="login-username"]').attributes('autocomplete')).toBe(
      'username',
    )
    const password = wrapper.get('[data-testid="login-password"]')
    expect(password.attributes('type')).toBe('password')
    expect(password.attributes('autocomplete')).toBe('current-password')
  })

  it('connecte par mot de passe et propose les facteurs quand le serveur demande un second facteur', async () => {
    const calls = installFakeApi({
      'GET /api/auth/session': loggedOutSession,
      'POST /api/auth/login': {
        state: 'second_factor_required',
        factors: ['webauthn', 'totp'],
        webauthn: keyOptions,
      },
    })
    const wrapper = await mountLogin()

    await submitPassword(wrapper)

    const loginCall = calls.find((call) => call.path === '/api/auth/login')
    expect(loginCall?.body).toEqual({ username: 'alice', password: strongPassword })
    expect(wrapper.find('[data-testid="factor-key"]').text()).toBe('Utiliser ma clé de sécurité')
    const code = wrapper.get('[data-testid="factor-totp-code"]')
    expect(code.attributes('autocomplete')).toBe('one-time-code')
    expect(code.attributes('inputmode')).toBe('numeric')
    expect(wrapper.find('[data-testid="login-password"]').exists()).toBe(false)
  })

  it('le choix du facteur ne propose que le code TOTP quand aucune clé n’est enregistrée', async () => {
    installFakeApi({
      'GET /api/auth/session': loggedOutSession,
      'POST /api/auth/login': { state: 'second_factor_required', factors: ['totp'] },
    })
    const wrapper = await mountLogin()

    await submitPassword(wrapper)

    expect(wrapper.find('[data-testid="factor-key"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="factor-totp-code"]').exists()).toBe(true)
  })

  it('termine la connexion avec la clé de sécurité', async () => {
    stubWebAuthn()
    let state = 'logged_out'
    const calls = installFakeApi({
      'GET /api/auth/session': () => ({ ...authenticatedSession, state }),
      'POST /api/auth/login': {
        state: 'second_factor_required',
        factors: ['webauthn'],
        webauthn: keyOptions,
      },
      'POST /api/auth/webauthn/login/finish': () => {
        state = 'authenticated'
        return { state: 'authenticated' }
      },
    })
    const wrapper = await mountLogin()
    await submitPassword(wrapper)

    await wrapper.get('[data-testid="factor-key"]').trigger('click')
    await flushPromises()

    const finish = calls.find((call) => call.path === '/api/auth/webauthn/login/finish')
    expect(finish?.body).toEqual({ credential: fakeCredentialJson })
    expect(useAuth().status.value?.state).toBe('authenticated')
  })

  it('retourne à l’étape du mot de passe après un échec du second facteur', async () => {
    installFakeApi({
      'GET /api/auth/session': loggedOutSession,
      'POST /api/auth/login': { state: 'second_factor_required', factors: ['totp'] },
      'POST /api/auth/totp/login': () => failure(401, 'invalid_code'),
    })
    const wrapper = await mountLogin()
    await submitPassword(wrapper)

    await wrapper.get('[data-testid="factor-totp-code"]').setValue('123456')
    await wrapper.get('form[data-testid="totp-step"]').trigger('submit')
    await flushPromises()

    expect(wrapper.find('[data-testid="factor-totp-code"]').exists()).toBe(false)
    expect(passwordValue(wrapper)).toBe('')
    expect(wrapper.get('[data-testid="login-error"]').text()).toBe('Code incorrect.')
  })

  it('refuse un code qui n’a pas six chiffres sans appeler le serveur', async () => {
    const calls = installFakeApi({
      'GET /api/auth/session': loggedOutSession,
      'POST /api/auth/login': { state: 'second_factor_required', factors: ['totp'] },
    })
    const wrapper = await mountLogin()
    await submitPassword(wrapper)

    await wrapper.get('[data-testid="factor-totp-code"]').setValue('12a4')
    await wrapper.get('form[data-testid="totp-step"]').trigger('submit')
    await flushPromises()

    expect(calls.some((call) => call.path === '/api/auth/totp/login')).toBe(false)
    expect(wrapper.get('[data-testid="login-error"]').text()).toBe(
      'Saisissez les six chiffres du code.',
    )
  })

  it('« Se connecter avec une clé d’accès » ne demande aucun mot de passe', async () => {
    const stub = stubWebAuthn()
    const calls = installFakeApi({
      'GET /api/auth/session': loggedOutSession,
      'POST /api/auth/passkey/login/begin': { state: 'logged_out', webauthn: keyOptions },
      'POST /api/auth/passkey/login/finish': { state: 'authenticated' },
    })
    const wrapper = await mountLogin()

    await wrapper.get('[data-testid="login-passkey"]').trigger('click')
    await flushPromises()

    expect(stub.get).toHaveBeenCalledOnce()
    expect(calls.map((call) => call.path)).toContain('/api/auth/passkey/login/finish')
    expect(calls.some((call) => call.path === '/api/auth/login')).toBe(false)
    expect(JSON.stringify(calls.map((call) => call.body))).not.toContain('password')
  })

  it('cache la clé d’accès quand WebAuthn est indisponible', async () => {
    installFakeApi({
      'GET /api/auth/session': { ...loggedOutSession, webauthnAvailable: false },
    })
    const wrapper = await mountLogin()

    expect(wrapper.find('[data-testid="login-passkey"]').exists()).toBe(false)
  })

  it('le 429 affiche le délai d’attente et désactive le bouton jusqu’à son terme', async () => {
    vi.useFakeTimers()
    installFakeApi({
      'GET /api/auth/session': loggedOutSession,
      'POST /api/auth/login': () => failure(429, 'throttled', 'raw', { 'Retry-After': '3' }),
    })
    const wrapper = await mountLogin()

    await submitPassword(wrapper)

    const submit = () => wrapper.get('[data-testid="login-submit"]')
    expect(wrapper.get('[data-testid="throttle-countdown"]').text()).toContain('3 s')
    expect(submit().attributes('disabled')).toBeDefined()
    expect(passwordValue(wrapper)).toBe('')
    await vi.advanceTimersByTimeAsync(1000)
    expect(wrapper.get('[data-testid="throttle-countdown"]').text()).toContain('2 s')
    await vi.advanceTimersByTimeAsync(2000)
    expect(wrapper.find('[data-testid="throttle-countdown"]').exists()).toBe(false)
    expect(submit().attributes('disabled')).toBeUndefined()
  })

  it('montre le même message pour un identifiant inconnu et un mauvais mot de passe', async () => {
    installFakeApi({
      'GET /api/auth/session': loggedOutSession,
      'POST /api/auth/login': () => failure(401, 'invalid_credentials'),
    })
    const wrapper = await mountLogin()

    await submitPassword(wrapper)

    expect(wrapper.get('[data-testid="login-error"]').text()).toBe(
      'Identifiant ou mot de passe incorrect.',
    )
    expect(passwordValue(wrapper)).toBe('')
  })
})
