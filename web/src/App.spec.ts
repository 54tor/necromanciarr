import { flushPromises, mount as mountComponent, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from './App.vue'
import { authToken, loginRequired } from './api/client'
import { failure, installFakeApi, resetAppState } from './test/fakeApi'
import { busyStatus, okReport } from './test/fixtures'
import { stubViewport } from './test/viewport'

const mounted: VueWrapper[] = []
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

  it('renders the product name and the six tabs', async () => {
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
      'Démarrage',
      'Propositions',
      'Déduplication',
      'Liens',
      'Étapes',
      'Sources',
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
      'Getting started',
      'Proposals',
      'Deduplication',
      'Links',
      'Stages',
      'Sources',
    ])
  })

  it('App_showsPendingProposalsAndFailedChecksAsBadges', async () => {
    installFakeApi({
      'POST /api/doctor/run': { checks: [{ ...okReport.checks[0], status: 'fail' }] },
      'GET /api/status': { ...busyStatus, proposals: { pending: 4 } },
      'GET /api/proposals': [],
    })
    window.location.hash = '#/etapes'
    const wrapper = mount(App)
    await flushPromises()

    const gotBadges = wrapper.findAll('[data-testid="nav-badge"]').map((badge) => badge.text())

    expect(gotBadges).toEqual(['1', '4'])
  })

  it('App_leavesAPageWithoutViewForTheDefaultOne', async () => {
    installFakeApi({
      'POST /api/doctor/run': okReport,
      'GET /api/status': busyStatus,
      'GET /api/proposals': [],
    })
    window.location.hash = '#/volumes'
    mount(App)
    await flushPromises()

    expect(window.location.hash).toBe('#/propositions')
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

  it('App_asksForTheTokenOnA401AndReloadsAfterwards', async () => {
    const calls = installFakeApi({
      'POST /api/doctor/run': (call) =>
        call.headers.Authorization ? okReport : failure(401, 'unauthorized'),
      'GET /api/status': (call) =>
        call.headers.Authorization ? busyStatus : failure(401, 'unauthorized'),
      'GET /api/proposals': [],
    })
    window.location.hash = '#/propositions'
    mount(App, { attachTo: document.body })
    await flushPromises()
    expect(loginRequired.value).toBe(true)
    const input = document.body.querySelector<HTMLInputElement>('#auth-token')
    expect(input).not.toBeNull()

    const token = 'typed-by-the-operator'
    input!.value = token
    input!.dispatchEvent(new Event('input'))
    document.body.querySelector<HTMLButtonElement>('[data-testid="login-submit"]')?.click()
    await flushPromises()

    expect(authToken.value).toBe(token)
    expect(loginRequired.value).toBe(false)
    expect(calls.at(-1)?.headers.Authorization).toBe(`Bearer ${token}`)
  })

  it('App_tellsWhenTheTypedTokenIsRefused', async () => {
    installFakeApi({
      'POST /api/doctor/run': () => failure(401, 'unauthorized'),
      'GET /api/status': () => failure(401, 'unauthorized'),
    })
    mount(App, { attachTo: document.body })
    await flushPromises()

    const input = document.body.querySelector<HTMLInputElement>('#auth-token')
    if (input) {
      input.value = 'wrong'
      input.dispatchEvent(new Event('input'))
    }
    document.body.querySelector<HTMLButtonElement>('[data-testid="login-submit"]')?.click()
    await flushPromises()

    expect(document.body.querySelector('[data-testid="login-rejected"]')?.textContent).toContain(
      'Jeton refusé',
    )
    expect(authToken.value).toBe('')
  })

  it('App_reloadsTheCurrentViewAfterTheTokenIsAccepted', async () => {
    const calls = installFakeApi({
      'POST /api/doctor/run': (call) =>
        call.headers.Authorization ? okReport : failure(401, 'unauthorized'),
      'GET /api/status': (call) =>
        call.headers.Authorization ? busyStatus : failure(401, 'unauthorized'),
      'GET /api/proposals': (call) =>
        call.headers.Authorization ? [] : failure(401, 'unauthorized'),
    })
    window.location.hash = '#/propositions'
    mount(App, { attachTo: document.body })
    await flushPromises()
    const before = calls.filter(
      (call) => call.path === '/api/proposals' && call.headers.Authorization,
    ).length

    const input = document.body.querySelector<HTMLInputElement>('#auth-token')
    if (input) {
      input.value = 'ok-token'
      input.dispatchEvent(new Event('input'))
    }
    document.body.querySelector<HTMLButtonElement>('[data-testid="login-submit"]')?.click()
    await flushPromises()

    const after = calls.filter(
      (call) => call.path === '/api/proposals' && call.headers.Authorization,
    ).length
    expect(before).toBe(0)
    expect(after).toBe(1)
  })

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
