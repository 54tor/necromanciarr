import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setToken } from '@/api/client'
import { useAuth } from '@/composables/useAuth'
import { failure, installFakeApi, resetAppState, type FakeCall } from '@/test/fakeApi'
import { loggedOutSession, noAccountSession, strongPassword } from '@/test/authFixtures'
import AccountSetupView from './AccountSetupView.vue'

const TOO_LONG_IN_BYTES = 'é'.repeat(65)

async function mountSetup(routes: Record<string, unknown> = {}) {
  const calls = installFakeApi({ 'GET /api/auth/session': noAccountSession, ...routes })
  await useAuth().load()
  setToken('bootstrap-token-value')
  const wrapper = mount(AccountSetupView)
  await flushPromises()
  return { wrapper, calls }
}

async function submit(
  wrapper: VueWrapper,
  password: string,
  confirmation = password,
  username = 'alice',
): Promise<void> {
  await wrapper.get('[data-testid="setup-username"]').setValue(username)
  await wrapper.get('[data-testid="setup-password"]').setValue(password)
  await wrapper.get('[data-testid="setup-confirmation"]').setValue(confirmation)
  await wrapper.get('form').trigger('submit')
  await flushPromises()
}

function setupCalls(calls: FakeCall[]): FakeCall[] {
  return calls.filter((call) => call.path === '/api/auth/setup')
}

function fieldValues(wrapper: VueWrapper): string[] {
  return ['setup-password', 'setup-confirmation'].map(
    (testId) => (wrapper.get(`[data-testid="${testId}"]`).element as HTMLInputElement).value,
  )
}

describe('AccountSetupView', () => {
  beforeEach(resetAppState)
  afterEach(() => vi.unstubAllGlobals())

  it('déclare de nouveaux mots de passe et dit ce que le compte protège', async () => {
    const { wrapper } = await mountSetup()

    expect(wrapper.get('[data-testid="setup-username"]').attributes('autocomplete')).toBe(
      'username',
    )
    for (const testId of ['setup-password', 'setup-confirmation']) {
      expect(wrapper.get(`[data-testid="${testId}"]`).attributes('autocomplete')).toBe(
        'new-password',
      )
      expect(wrapper.get(`[data-testid="${testId}"]`).attributes('type')).toBe('password')
    }
    expect(wrapper.text()).toContain('le jeton d’amorçage ne fonctionne plus')
  })

  it('refuse un mot de passe de moins de 12 caractères ou de plus de 128 octets avant tout appel', async () => {
    const { wrapper, calls } = await mountSetup()

    await submit(wrapper, 'court-11car')
    expect(wrapper.get('[data-testid="setup-error"]').text()).toBe(
      'Le mot de passe doit compter 12 caractères au moins.',
    )
    await submit(wrapper, TOO_LONG_IN_BYTES)
    expect(wrapper.get('[data-testid="setup-error"]').text()).toBe(
      'Le mot de passe ne doit pas dépasser 128 octets.',
    )

    expect(setupCalls(calls)).toHaveLength(0)
    expect(fieldValues(wrapper)).toEqual(['', ''])
  })

  it('refuse une confirmation différente', async () => {
    const { wrapper, calls } = await mountSetup()

    await submit(wrapper, strongPassword, `${strongPassword}x`)

    expect(wrapper.get('[data-testid="setup-error"]').text()).toBe(
      'La confirmation ne correspond pas au mot de passe.',
    )
    expect(setupCalls(calls)).toHaveLength(0)
  })

  it('refuse un identifiant vide ou de plus de 64 caractères', async () => {
    const { wrapper, calls } = await mountSetup()

    await submit(wrapper, strongPassword, strongPassword, '   ')
    await submit(wrapper, strongPassword, strongPassword, 'a'.repeat(65))

    expect(wrapper.get('[data-testid="setup-error"]').text()).toBe(
      'L’identifiant doit compter de 1 à 64 caractères.',
    )
    expect(setupCalls(calls)).toHaveLength(0)
  })

  it('crée le compte avec le jeton d’amorçage et efface les mots de passe', async () => {
    let created = false
    const { wrapper, calls } = await mountSetup({
      'GET /api/auth/session': () =>
        created ? { ...noAccountSession, state: 'authenticated' } : noAccountSession,
      'POST /api/auth/setup': () => {
        created = true
        return new Response(JSON.stringify({ state: 'authenticated' }), { status: 201 })
      },
    })

    await submit(wrapper, strongPassword)

    expect(setupCalls(calls)[0]?.body).toEqual({ username: 'alice', password: strongPassword })
    expect(setupCalls(calls)[0]?.headers.Authorization).toBe('Bearer bootstrap-token-value')
    expect(useAuth().status.value?.state).toBe('authenticated')
    expect(useAuth().accountJustCreated.value).toBe(true)
    expect(fieldValues(wrapper)).toEqual(['', ''])
  })

  it('revient à la connexion quand un compte existe déjà', async () => {
    let exists = false
    await mountSetup({
      'GET /api/auth/session': () => (exists ? loggedOutSession : noAccountSession),
      'POST /api/auth/setup': () => {
        exists = true
        return failure(409, 'account_exists')
      },
    }).then(({ wrapper }) => submit(wrapper, strongPassword))

    expect(useAuth().status.value?.state).toBe('logged_out')
  })
})
