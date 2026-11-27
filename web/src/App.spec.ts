import { flushPromises, mount as mountComponent, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from './App.vue'
import { authToken } from './api/client'
import { failure, installFakeApi, resetAppState } from './test/fakeApi'
import { busyStatus, okReport } from './test/fixtures'
import { loggedOutSession, noAccountSession, strongPassword } from './test/authFixtures'
import { stubViewport } from './test/viewport'

const mounted: VueWrapper[] = []

function typeInto(selector: string, value: string): void {
  const input = document.body.querySelector<HTMLInputElement>(selector)
  if (!input) throw new Error(`${selector} absent`)
  input.value = value
  input.dispatchEvent(new Event('input'))
}
const mount: typeof mountComponent = (component, options) => {
  const wrapper = mountComponent(component, options)
  mounted.push(wrapper as VueWrapper)
  return wrapper
}

describe('App', () => {
  beforeEach(() => {
    resetAppState()
    stubViewport(1280)
  })
  afterEach(() => {
    mounted.splice(0).forEach((wrapper) => wrapper.unmount())
    vi.unstubAllGlobals()
  })

  it('renders the product name and the ten tabs', async () => {
    installFakeApi({
      'POST /api/doctor/run': okReport,
      'GET /api/status': busyStatus,
      'GET /api/proposals': [],
    })
    const wrapper = mount(App)
    await flushPromises()

    const gotTabs = wrapper.findAll('nav [data-testid="nav-label"]').map((label) => label.text())

    expect(wrapper.get('aside img').attributes('alt')).toContain('Necromanciarr')
    expect(gotTabs).toEqual([
      'Assistant',
      'Démarrage',
      'Propositions',
      'Déduplication',
      'Liens',
      'Étapes',
      'Sources',
      'Volumes',
      'Configuration',
      'Compte',
    ])
  })

  it('App_switchesToTheServerLanguageAtTheFirstStatus', async () => {
    installFakeApi({
      'POST /api/doctor/run': okReport,
      'GET /api/status': { ...busyStatus, language: 'en' },
      'GET /api/proposals': [],
    })
    const wrapper = mount(App)
    await flushPromises()

    const gotTabs = wrapper.findAll('nav [data-testid="nav-label"]').map((label) => label.text())

    expect(gotTabs).toEqual([
      'Assistant',
      'Getting started',
      'Proposals',
      'Deduplication',
      'Links',
      'Stages',
      'Sources',
      'Volumes',
      'Configuration',
      'Account',
    ])
  })

  it('App_showsPendingProposalsFailedChecksAndBrokenVolumesAsBadges', async () => {
    installFakeApi({
      'POST /api/doctor/run': { checks: [{ ...okReport.checks[0], status: 'fail' }] },
      'GET /api/status': { ...busyStatus, proposals: { pending: 4 }, volumes_broken: 2 },
      'GET /api/proposals': [],
    })
    window.location.hash = '#/etapes'
    const wrapper = mount(App)
    await flushPromises()

    const gotBadges = wrapper.findAll('[data-testid="nav-badge"]').map((badge) => badge.text())

    expect(gotBadges).toEqual(['1', '4', '2'])
  })

  it('marks the current tab with aria-current', async () => {
    installFakeApi({
      'POST /api/doctor/run': okReport,
      'GET /api/status': busyStatus,
      'GET /api/jobs': [],
    })
    window.location.hash = '#/etapes'
    const wrapper = mount(App)
    await flushPromises()

    const gotCurrent = wrapper
      .findAll('nav a[aria-current="page"] [data-testid="nav-label"]')
      .map((label) => label.text())

    expect(gotCurrent).toEqual(['Étapes'])
  })

  it('affiche la saisie du jeton puis la création du compte quand aucun compte n’existe', async () => {
    let state = 'no_account'
    const calls = installFakeApi({
      'GET /api/auth/session': () => ({ ...noAccountSession, state }),
      'POST /api/auth/setup': () => {
        state = 'authenticated'
        return new Response(JSON.stringify({ state: 'authenticated' }), { status: 201 })
      },
      'POST /api/doctor/run': okReport,
      'GET /api/status': busyStatus,
      'GET /api/proposals': [],
    })
    window.location.hash = '#/propositions'
    const wrapper = mount(App, { attachTo: document.body })
    await flushPromises()

    expect(wrapper.find('aside').exists()).toBe(false)
    expect(calls.some((call) => call.path === '/api/status')).toBe(false)
    typeInto('#auth-token', 'typed-by-the-operator')
    document.body.querySelector<HTMLButtonElement>('[data-testid="token-submit"]')?.click()
    await flushPromises()
    expect(document.body.querySelector('#auth-token')).toBeNull()
    await wrapper.get('[data-testid="setup-username"]').setValue('alice')
    await wrapper.get('[data-testid="setup-password"]').setValue(strongPassword)
    await wrapper.get('[data-testid="setup-confirmation"]').setValue(strongPassword)
    await wrapper.get('form[data-testid="setup-form"]').trigger('submit')
    await flushPromises()

    const setupCall = calls.find((call) => call.path === '/api/auth/setup')
    expect(setupCall?.headers.Authorization).toBe('Bearer typed-by-the-operator')
    expect(authToken.value).toBe('')
    expect(wrapper.find('aside').exists()).toBe(true)
    expect(wrapper.get('[data-testid="protect-account"]').text()).toContain('clé de sécurité')
    const appCalls = calls.slice(calls.indexOf(setupCall!) + 1)
    expect(appCalls.every((call) => call.headers.Authorization === undefined)).toBe(true)
  })

  it('App_tellsWhenTheTypedTokenIsRefused', async () => {
    installFakeApi({
      'GET /api/auth/session': noAccountSession,
      'POST /api/auth/setup': () => failure(401, 'unauthorized'),
    })
    const wrapper = mount(App, { attachTo: document.body })
    await flushPromises()
    typeInto('#auth-token', 'wrong')
    document.body.querySelector<HTMLButtonElement>('[data-testid="token-submit"]')?.click()
    await flushPromises()

    await wrapper.get('[data-testid="setup-username"]').setValue('alice')
    await wrapper.get('[data-testid="setup-password"]').setValue(strongPassword)
    await wrapper.get('[data-testid="setup-confirmation"]').setValue(strongPassword)
    await wrapper.get('form[data-testid="setup-form"]').trigger('submit')
    await flushPromises()

    expect(document.body.querySelector('[data-testid="token-rejected"]')?.textContent).toContain(
      'Jeton refusé',
    )
    expect(authToken.value).toBe('')
  })

  it('affiche la connexion quand le compte existe et que la session est absente', async () => {
    const calls = installFakeApi({ 'GET /api/auth/session': loggedOutSession })
    const wrapper = mount(App, { attachTo: document.body })
    await flushPromises()

    expect(wrapper.find('[data-testid="login-username"]').exists()).toBe(true)
    expect(document.body.querySelector('#auth-token')).toBeNull()
    expect(calls.map((call) => call.path)).toEqual(['/api/auth/session'])
  })

  it.each(['session_required', 'bearer_disabled'])(
    'un 401 %s renvoie à la connexion sans demander de jeton',
    async (code) => {
      let state = 'authenticated'
      installFakeApi({
        'GET /api/auth/session': () => ({ ...loggedOutSession, state }),
        'POST /api/doctor/run': () => {
          state = 'logged_out'
          return failure(401, code)
        },
        'GET /api/status': () => failure(401, code),
        'GET /api/proposals': () => failure(401, code),
      })
      const wrapper = mount(App, { attachTo: document.body })
      await flushPromises()

      expect(wrapper.find('[data-testid="login-username"]').exists()).toBe(true)
      expect(wrapper.find('aside').exists()).toBe(false)
      expect(document.body.querySelector('#auth-token')).toBeNull()
    },
  )

  it('App_doesNotShowTheLoginDialogWhenTheServerAnswers', async () => {
    installFakeApi({
      'POST /api/doctor/run': okReport,
      'GET /api/status': busyStatus,
      'GET /api/proposals': [],
    })
    mount(App, { attachTo: document.body })
    await flushPromises()

    expect(document.body.querySelector('#auth-token')).toBeNull()
  })
})
