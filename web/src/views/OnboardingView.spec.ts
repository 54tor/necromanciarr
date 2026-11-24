import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from '@/App.vue'
import type { DoctorReport } from '@/api/types'
import { doctorReport, busyStatus, okReport } from '@/test/fixtures'
import { stubViewport } from '@/test/viewport'
import { installFakeApi, resetAppState } from '@/test/fakeApi'
import OnboardingView from './OnboardingView.vue'

describe('OnboardingView', () => {
  beforeEach(() => {
    resetAppState()
    stubViewport(1280)
  })
  afterEach(() => vi.unstubAllGlobals())

  it('OnboardingView_showsEveryCheckWithStatusAndHint', async () => {
    installFakeApi({ 'POST /api/doctor/run': doctorReport, 'GET /api/status': busyStatus })

    const wrapper = mount(OnboardingView)
    await flushPromises()

    const gotItems = wrapper.findAll('tbody tr')
    expect(gotItems.map((item) => item.attributes('data-row-key'))).toEqual([
      'config',
      'qbit',
      'links',
      'provider',
    ])
    const tones = gotItems.map((item) => item.find('[data-tone]').attributes('data-tone'))
    expect(tones).toEqual(['ok', 'fail', 'warn', 'muted'])
    expect(gotItems[1]?.text()).toContain('Échec')
    expect(gotItems[1]?.text()).toContain('Connexion refusée.')
    expect(gotItems[1]?.text()).toContain('Vérifiez l’adresse du client.')
    expect(gotItems[1]?.text()).toContain('12 ms')
    expect(gotItems[3]?.text()).toContain('Ignoré')
  })

  it('OnboardingView_groupsChecksByFamily', async () => {
    const report: DoctorReport = {
      worst: 'ok',
      checks: [
        'qbittorrent',
        'config-dir',
        'arr:radarr:0',
        'fs-providers',
        'library-roots',
        'zzz',
      ].map((id) => ({ ...okReport.checks[0]!, id, title: `Titre ${id}` })),
    }
    installFakeApi({ 'POST /api/doctor/run': report, 'GET /api/status': busyStatus })

    const wrapper = mount(OnboardingView)
    await flushPromises()

    const gotFamilies = wrapper.findAll('table').map((table) => ({
      caption: table.get('caption').text(),
      rows: table.findAll('tbody tr').map((row) => row.attributes('data-row-key')),
    }))
    expect(gotFamilies).toEqual([
      { caption: 'Configuration', rows: ['config-dir'] },
      { caption: 'Services', rows: ['qbittorrent', 'arr:radarr:0'] },
      { caption: 'Stockage', rows: ['library-roots'] },
      { caption: 'Volumes', rows: ['fs-providers'] },
      { caption: 'Autres', rows: ['zzz'] },
    ])
  })

  it('OnboardingView_rerunsOneCheckFromItsRow', async () => {
    const calls = installFakeApi({
      'POST /api/doctor/run': doctorReport,
      'GET /api/status': busyStatus,
      'POST /api/doctor/checks/{checkId}': {
        worst: 'ok',
        checks: [
          {
            id: 'qbit',
            code: 'client_ok',
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

    const rowButton = wrapper.get('[data-row-key="qbit"] button[data-action="doctor run"]')
    expect(rowButton.attributes('data-scope')).toBe('row')
    expect(rowButton.attributes('aria-label')).toBe('Relancer le contrôle : Client qBittorrent')
    await rowButton.trigger('click')
    await flushPromises()

    expect(
      calls.some((call) => call.method === 'POST' && call.path === '/api/doctor/checks/qbit'),
    ).toBe(true)
    expect(wrapper.get('[data-row-key="qbit"]').text()).toContain('Connecté.')
    expect(wrapper.get('[data-row-key="config"]').text()).toContain('Configuration valide.')
  })

  it('OnboardingView_showsServerRenderedDetailWithoutRetranslating', async () => {
    const detail = 'Connection refused by 127.0.0.1:8080 (rendu serveur)'
    const hint = 'Check the client address.'
    installFakeApi({
      'POST /api/doctor/run': {
        worst: 'fail',
        checks: [
          {
            id: 'qbit',
            code: 'client_unreachable',
            title: 'qBittorrent client',
            status: 'fail',
            detail,
            hint,
            durationMs: 3,
          },
        ],
      },
      'GET /api/status': busyStatus,
    })

    const wrapper = mount(OnboardingView)
    await flushPromises()

    const gotRow = wrapper.get('[data-row-key="qbit"]').text()
    expect(gotRow).toContain(detail)
    expect(gotRow).toContain(hint)
    expect(gotRow).not.toContain('Client injoignable')
    expect(gotRow).toContain('qBittorrent client')
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
      'GET /api/status': {
        candidates: {},
        proposals: {},
        steps: {},
        cross_seed_mode: 'disabled',
        language: 'fr',
      },
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
        language: 'fr',
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

  it('OnboardingView_listsTheTorrentsHeldAboveTheMissingPiecesCap', async () => {
    installFakeApi({
      'POST /api/doctor/run': okReport,
      'GET /api/status': {
        candidates: { verified: 1 },
        proposals: {},
        steps: {},
        cross_seed_mode: 'disabled',
        language: 'fr',
        held: [
          {
            candidate_id: 7,
            candidate_name: 'Show.S01',
            tracker: '',
            kind: 'partial_pack',
            reason: 'missing_pieces_above_cap',
            percent: 40,
            missing_pieces: 6,
            total_pieces: 10,
          },
        ],
      },
    })
    const wrapper = mount(OnboardingView)
    await flushPromises()

    const gotText = wrapper.find('[data-held]').text()

    expect(gotText).toContain('Show.S01')
    expect(gotText).toContain('6/10 pièces manquantes')
    expect(gotText).toContain('au-delà du plafond')
  })

  it('OnboardingView_rendersTheTextOfStructuredStatusWarnings', async () => {
    installFakeApi({
      'POST /api/doctor/run': okReport,
      'GET /api/status': {
        candidates: {},
        proposals: {},
        steps: {},
        cross_seed_mode: 'native',
        language: 'fr',
        warnings: [
          { code: 'warning.crossseed.fallback_native', text: 'repli sur la correspondance native' },
        ],
      },
    })
    const wrapper = mount(OnboardingView)
    await flushPromises()

    const gotText = wrapper.text()

    expect(gotText).toContain('Avertissement : repli sur la correspondance native')
    expect(gotText).not.toContain('[object Object]')
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
