import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useAuth } from '@/composables/useAuth'
import { failure, installFakeApi, resetAppState, type FakeCall } from '@/test/fakeApi'
import {
  authenticatedSession,
  credentials,
  keyOptions,
  sessions,
  strongPassword,
  totpEnrolment,
} from '@/test/authFixtures'
import { fakeCredentialJson, stubWebAuthn, unstubWebAuthn } from '@/test/webauthn'
import AccountView from './AccountView.vue'

vi.mock('vue-sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

const mounted: VueWrapper[] = []

async function mountAccount(routes: Record<string, unknown> = {}) {
  const calls = installFakeApi({
    'GET /api/auth/session': authenticatedSession,
    'GET /api/auth/webauthn/credentials': credentials,
    'GET /api/auth/sessions': sessions,
    ...routes,
  })
  await useAuth().load()
  const wrapper = mount(AccountView, { attachTo: document.body })
  mounted.push(wrapper)
  await flushPromises()
  return { wrapper, calls }
}

function bodyElement<Element extends HTMLElement>(selector: string): Element {
  const element = document.body.querySelector<Element>(selector)
  if (!element) throw new Error(`${selector} absent`)
  return element
}

function typeInto(selector: string, value: string): void {
  const input = bodyElement<HTMLInputElement>(selector)
  input.value = value
  input.dispatchEvent(new Event('input'))
}

function callsTo(calls: FakeCall[], method: string, path: string): FakeCall[] {
  return calls.filter((call) => call.method === method && call.path === path)
}

describe('AccountView', () => {
  beforeEach(resetAppState)
  afterEach(() => {
    mounted.splice(0).forEach((wrapper) => wrapper.unmount())
    vi.unstubAllGlobals()
    unstubWebAuthn()
    document.body.innerHTML = ''
  })

  it('résume l’état du compte', async () => {
    const { wrapper } = await mountAccount()

    const valeurObtenue = wrapper.get('[data-testid="account-summary"]').text()

    expect(valeurObtenue).toContain('alice')
    expect(valeurObtenue).toContain('Clés de sécurité : 1')
    expect(valeurObtenue).toContain('Code TOTP : non activé')
  })

  it('la page Compte liste les clés et exige le mot de passe pour en supprimer une', async () => {
    const { wrapper, calls } = await mountAccount({
      'DELETE /api/auth/webauthn/credentials/{id}': {},
    })

    const items = wrapper.findAll('[data-testid="key-item"]').map((item) => item.text())
    expect(items[0]).toContain('Clé du bureau')
    expect(items[0]).toContain('Clé de sécurité (second facteur)')
    expect(items[0]).toContain('Jamais utilisée')
    expect(items[1]).toContain('Clé d’accès (connexion sans mot de passe)')

    await wrapper.get('[data-testid="key-remove-key-1"]').trigger('click')
    await flushPromises()
    const confirm = bodyElement<HTMLButtonElement>('[data-testid="confirm-dialog-confirm"]')
    expect(confirm.disabled).toBe(true)
    typeInto('[data-testid="key-remove-password"]', strongPassword)
    await flushPromises()
    bodyElement<HTMLButtonElement>('[data-testid="confirm-dialog-confirm"]').click()
    await flushPromises()

    const removal = callsTo(calls, 'DELETE', '/api/auth/webauthn/credentials/key-1')
    expect(removal).toHaveLength(1)
    expect(removal[0]?.body).toEqual({ password: strongPassword })
  })

  it('renomme une clé', async () => {
    const { wrapper, calls } = await mountAccount({
      'PATCH /api/auth/webauthn/credentials/{id}': {},
    })

    await wrapper.get('[data-testid="key-rename-key-2"]').trigger('click')
    await wrapper.get('[data-testid="key-rename-input"]').setValue('Téléphone pro')
    await wrapper.get('form[data-testid="key-rename-form"]').trigger('submit')
    await flushPromises()

    expect(callsTo(calls, 'PATCH', '/api/auth/webauthn/credentials/key-2')[0]?.body).toEqual({
      name: 'Téléphone pro',
    })
  })

  it('ajoute une clé : mot de passe, appel du navigateur puis nom', async () => {
    const stub = stubWebAuthn()
    const { wrapper, calls } = await mountAccount({
      'POST /api/auth/webauthn/register/begin': { webauthn: keyOptions },
      'POST /api/auth/webauthn/register/finish': {},
    })

    await wrapper.get('[data-testid="key-name"]').setValue('Clé de secours')
    await wrapper.get('[data-testid="key-kind-passkey"]').setValue(true)
    await wrapper.get('[data-testid="key-password"]').setValue(strongPassword)
    await wrapper.get('form[data-testid="key-add-form"]').trigger('submit')
    await flushPromises()

    expect(callsTo(calls, 'POST', '/api/auth/webauthn/register/begin')[0]?.body).toEqual({
      password: strongPassword,
      kind: 'passkey',
    })
    expect(stub.create).toHaveBeenCalledOnce()
    expect(callsTo(calls, 'POST', '/api/auth/webauthn/register/finish')[0]?.body).toEqual({
      name: 'Clé de secours',
      credential: fakeCredentialJson,
    })
    expect((wrapper.get('[data-testid="key-password"]').element as HTMLInputElement).value).toBe('')
  })

  it('affiche le QR code, la clé de saisie manuelle puis exige un code valide pour confirmer le TOTP', async () => {
    let totp = false
    const { wrapper, calls } = await mountAccount({
      'GET /api/auth/session': () => ({
        ...authenticatedSession,
        secondFactors: { keys: 1, passkeys: 1, totp },
      }),
      'POST /api/auth/totp/setup/begin': totpEnrolment,
      'POST /api/auth/totp/setup/finish': (call: FakeCall) => {
        if ((call.body as { code: string }).code !== '246810') return failure(401, 'invalid_code')
        totp = true
        return new Response('{}', { status: 201 })
      },
    })
    await wrapper.get('[data-testid="totp-password"]').setValue(strongPassword)
    await wrapper.get('form[data-testid="totp-begin-form"]').trigger('submit')
    await flushPromises()

    expect(wrapper.get('[data-testid="totp-qr"]').attributes('src')).toBe(
      `data:image/png;base64,${totpEnrolment.qrPngBase64}`,
    )
    expect(wrapper.get('[data-testid="totp-secret"]').text()).toBe(totpEnrolment.secret)
    expect(wrapper.get('[data-testid="totp-link"]').attributes('href')).toBe(
      totpEnrolment.otpauthUri,
    )
    await wrapper.get('[data-testid="totp-code"]').setValue('12')
    await wrapper.get('form[data-testid="totp-confirm-form"]').trigger('submit')
    await flushPromises()
    expect(callsTo(calls, 'POST', '/api/auth/totp/setup/finish')).toHaveLength(0)
    expect(wrapper.get('[data-testid="totp-message"]').text()).toBe(
      'Saisissez les six chiffres du code.',
    )

    await wrapper.get('[data-testid="totp-code"]').setValue('246810')
    await wrapper.get('form[data-testid="totp-confirm-form"]').trigger('submit')
    await flushPromises()

    expect(wrapper.find('[data-testid="totp-secret"]').exists()).toBe(false)
    expect(wrapper.html()).not.toContain(totpEnrolment.secret)
    expect(wrapper.get('[data-testid="account-summary"]').text()).toContain('Code TOTP : activé')
  })

  it('un code TOTP refusé efface le secret et demande de recommencer', async () => {
    const { wrapper } = await mountAccount({
      'POST /api/auth/totp/setup/begin': totpEnrolment,
      'POST /api/auth/totp/setup/finish': () => failure(401, 'invalid_code'),
    })
    await wrapper.get('[data-testid="totp-password"]').setValue(strongPassword)
    await wrapper.get('form[data-testid="totp-begin-form"]').trigger('submit')
    await flushPromises()

    await wrapper.get('[data-testid="totp-code"]').setValue('111111')
    await wrapper.get('form[data-testid="totp-confirm-form"]').trigger('submit')
    await flushPromises()

    expect(wrapper.html()).not.toContain(totpEnrolment.secret)
    expect(wrapper.get('[data-testid="totp-message"]').text()).toBe('Code incorrect.')
  })

  it('signale que les clés de sécurité exigent HTTPS et propose TOTP quand WebAuthn est indisponible', async () => {
    const { wrapper } = await mountAccount({
      'GET /api/auth/session': { ...authenticatedSession, webauthnAvailable: false },
    })

    expect(wrapper.get('[data-testid="keys-https"]').text()).toContain('HTTPS')
    expect(wrapper.get('[data-testid="keys-https"]').text()).toContain('TOTP')
    expect(wrapper.find('[data-testid="key-add-form"]').exists()).toBe(false)
    expect(wrapper.find('form[data-testid="totp-begin-form"]').exists()).toBe(true)
  })

  it('change le mot de passe et dit que les autres sessions sont déconnectées', async () => {
    const { wrapper, calls } = await mountAccount({ 'POST /api/auth/password': {} })

    await wrapper.get('[data-testid="password-current"]').setValue('old-password-12')
    await wrapper.get('[data-testid="password-new"]').setValue(strongPassword)
    await wrapper.get('[data-testid="password-confirmation"]').setValue(strongPassword)
    await wrapper.get('form[data-testid="password-form"]').trigger('submit')
    await flushPromises()

    expect(callsTo(calls, 'POST', '/api/auth/password')[0]?.body).toEqual({
      currentPassword: 'old-password-12',
      newPassword: strongPassword,
    })
    expect(wrapper.get('[data-testid="password-message"]').text()).toContain(
      'autres sessions sont déconnectées',
    )
    for (const testId of ['password-current', 'password-new', 'password-confirmation'])
      expect((wrapper.get(`[data-testid="${testId}"]`).element as HTMLInputElement).value).toBe('')
  })

  it('liste les sessions, marque la courante et déconnecte partout', async () => {
    let state = 'authenticated'
    const { wrapper, calls } = await mountAccount({
      'GET /api/auth/session': () => ({ ...authenticatedSession, state }),
      'POST /api/auth/logout-all': () => {
        state = 'logged_out'
        return { state: 'logged_out' }
      },
    })

    const items = wrapper.findAll('[data-testid="session-item"]').map((item) => item.text())
    expect(items).toHaveLength(2)
    expect(items[0]).toContain('Cette session')
    expect(items[0]).toContain('192.0.2.10')
    expect(items[1]).not.toContain('Cette session')

    await wrapper.get('[data-testid="logout-all"]').trigger('click')
    await flushPromises()

    expect(callsTo(calls, 'POST', '/api/auth/logout-all')).toHaveLength(1)
    expect(useAuth().status.value?.state).toBe('logged_out')
  })
})
