import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from '@/App.vue'
import { doctorReport, busyStatus, okReport } from '@/test/fixtures'
import { installFakeApi, resetAppState } from '@/test/fakeApi'
import OnboardingView from './OnboardingView.vue'

describe('OnboardingView', () => {
  beforeEach(resetAppState)
  afterEach(() => vi.unstubAllGlobals())

  it('OnboardingView_showsEveryCheckWithStatusAndHint', async () => {
    installFakeApi({ 'POST /api/doctor/run': doctorReport, 'GET /api/status': busyStatus })

    const wrapper = mount(OnboardingView)
    await flushPromises()

    const gotItems = wrapper.findAll('[data-check]')
    expect(gotItems.map((item) => item.attributes('data-check'))).toEqual([
      'config',
      'qbit',
      'links',
      'provider',
    ])
    const statuses = gotItems.map((item) => item.find('[data-status]').attributes('data-status'))
    expect(statuses).toEqual(['ok', 'fail', 'warn', 'skipped'])
    expect(gotItems[1]?.text()).toContain('Échec')
    expect(gotItems[1]?.text()).toContain('Connexion refusée.')
    expect(gotItems[1]?.text()).toContain('Conseil : Vérifiez l’adresse du client.')
    expect(gotItems[3]?.text()).toContain('Ignoré')
  })

  it('OnboardingView_rerunsOneCheckThroughTheApi', async () => {
    const calls = installFakeApi({
      'POST /api/doctor/run': doctorReport,
      'GET /api/status': busyStatus,
      'POST /api/doctor/checks/{checkId}': {
        worst: 'ok',
        checks: [
          {
            id: 'qbit',
            title: 'Client qBittorrent',
            status: 'ok',
            detail: 'Connecté.',
            durationMs: 5,
          },
        ],
      },
    })
    const wrapper = mount(OnboardingView)
    await flushPromises()

    await wrapper.find('[data-check="qbit"] button').trigger('click')
    await flushPromises()

    expect(
      calls.some((call) => call.method === 'POST' && call.path === '/api/doctor/checks/qbit'),
    ).toBe(true)
    expect(wrapper.find('[data-check="qbit"]').text()).toContain('Connecté.')
    expect(wrapper.find('[data-check="config"]').text()).toContain('Configuration valide.')
  })

  it('OnboardingView_rerunsEverythingFromTheGlobalButton', async () => {
    const calls = installFakeApi({
      'POST /api/doctor/run': doctorReport,
      'GET /api/status': busyStatus,
    })
    const wrapper = mount(OnboardingView)
    await flushPromises()
    const before = calls.filter((call) => call.path === '/api/doctor/run').length

    await wrapper.find('button[data-action="doctor"]').trigger('click')
    await flushPromises()

    expect(calls.filter((call) => call.path === '/api/doctor/run').length).toBe(before + 1)
  })

  it('OnboardingView_redirectsWhenAFailRemains', async () => {
    installFakeApi({ 'POST /api/doctor/run': doctorReport, 'GET /api/status': busyStatus })
    window.location.hash = '#/propositions'

    mount(App)
    await flushPromises()

    expect(window.location.hash).toBe('#/demarrage')
  })

  it('OnboardingView_skipRemembersTheChoiceForTheSessionOnly', async () => {
    const setItem = vi.spyOn(Storage.prototype, 'setItem')
    installFakeApi({
      'POST /api/doctor/run': doctorReport,
      'GET /api/status': busyStatus,
      'GET /api/proposals': [],
    })
    window.location.hash = '#/propositions'
    const wrapper = mount(App)
    await flushPromises()

    await wrapper.find('[data-testid="skip"]').trigger('click')
    await flushPromises()

    expect(window.location.hash).toBe('#/propositions')
    window.location.hash = '#/liens'
    window.dispatchEvent(new HashChangeEvent('hashchange'))
    await flushPromises()
    expect(window.location.hash).toBe('#/liens')
    expect(setItem).not.toHaveBeenCalled()
    setItem.mockRestore()
  })

  it('OnboardingView_redirectsOnFirstLaunchEvenWithoutFail', async () => {
    installFakeApi({
      'POST /api/doctor/run': okReport,
      'GET /api/status': { candidates: {}, proposals: {}, steps: {}, cross_seed_mode: 'disabled' },
    })
    window.location.hash = '#/propositions'

    const wrapper = mount(App)
    await flushPromises()

    expect(window.location.hash).toBe('#/demarrage')
    expect(wrapper.text()).toContain('Première utilisation')
  })

  it('OnboardingView_neverRedirectsAwayFromStages', async () => {
    installFakeApi({
      'POST /api/doctor/run': doctorReport,
      'GET /api/status': busyStatus,
      'GET /api/jobs': [],
    })
    window.location.hash = '#/etapes'

    mount(App)
    await flushPromises()

    expect(window.location.hash).toBe('#/etapes')
  })

  it('OnboardingView_translatesStateKeysAndKeepsUnknownOnes', async () => {
    installFakeApi({
      'POST /api/doctor/run': okReport,
      'GET /api/status': {
        candidates: { verified: 3, odd_state: 1 },
        proposals: { pending: 2 },
        steps: { seeded_paused_incomplete: 1 },
        cross_seed_mode: 'disabled',
      },
    })
    const wrapper = mount(OnboardingView)
    await flushPromises()

    const gotText = wrapper.text()

    expect(gotText).toContain('Vérifiés : 3')
    expect(gotText).toContain('En attente : 2')
    expect(gotText).toContain('Ensemencée, en pause (incomplète) : 1')
    expect(gotText).toContain('odd_state : 1')
  })

  it('OnboardingView_doesNotRedirectWhenEverythingIsFineAndUsed', async () => {
    installFakeApi({
      'POST /api/doctor/run': okReport,
      'GET /api/status': busyStatus,
      'GET /api/proposals': [],
    })
    window.location.hash = '#/propositions'

    mount(App)
    await flushPromises()

    expect(window.location.hash).toBe('#/propositions')
  })
})
