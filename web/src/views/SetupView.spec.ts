import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from '@/App.vue'
import type { ConfigSnapshot, SetupTestResult } from '@/api/types'
import { busyStatus, okReport } from '@/test/fixtures'
import { installFakeApi, resetAppState } from '@/test/fakeApi'
import { stubViewport } from '@/test/viewport'
import SetupView from './SetupView.vue'

const emptySnapshot: ConfigSnapshot = {
  settings: {},
  lists: {},
  orphans: [],
  restartRequired: false,
}

const okTest: SetupTestResult = { status: 'ok', code: 'ok', detail: 'Joignable.' }
const failedTest: SetupTestResult = {
  status: 'fail',
  code: 'connection_refused',
  detail: 'Connexion refusée.',
  hint: 'Vérifiez l’adresse.',
}

function savedSnapshot(restartRequired: boolean): ConfigSnapshot {
  return { ...emptySnapshot, restartRequired }
}

async function fill(wrapper: ReturnType<typeof mount>, testId: string, value: string) {
  await wrapper.get(`[data-testid="${testId}"]`).setValue(value)
}

describe('SetupView', () => {
  beforeEach(() => {
    resetAppState()
    stubViewport(1280)
  })
  afterEach(() => vi.unstubAllGlobals())

  it('SetupView_showsTheArrStepFirstWithOneHelpSentencePerField', async () => {
    installFakeApi({ 'GET /api/config': emptySnapshot })

    const wrapper = mount(SetupView)
    await flushPromises()

    expect(wrapper.get('[data-testid="step-indicator"]').text()).toContain('1')
    expect(wrapper.find('[data-testid="field-arrUrl"]').exists()).toBe(true)
    const gotHelps = wrapper.findAll('[data-testid^="help-arr"]')
    expect(gotHelps).toHaveLength(3)
    gotHelps.forEach((help) => expect(help.text()).not.toBe(''))
  })

  it('SetupView_failedTestShowsServerDetailAndHintAndKeepsSaveDisabled', async () => {
    installFakeApi({ 'GET /api/config': emptySnapshot, 'POST /api/setup/test': failedTest })
    const wrapper = mount(SetupView)
    await flushPromises()

    await fill(wrapper, 'field-arrUrl', 'http://radarr:7878')
    await fill(wrapper, 'field-arrApiKey', 'key')
    await wrapper.get('[data-testid="test"]').trigger('click')
    await flushPromises()

    const gotResult = wrapper.get('[data-testid="test-result"]').text()
    expect(gotResult).toContain('Connexion refusée.')
    expect(gotResult).toContain('Vérifiez l’adresse.')
    expect(wrapper.get('[data-testid="save"]').attributes('disabled')).toBeDefined()
  })

  it('SetupView_successfulTestEnablesSaveThenWritesTheItemAndAdvances', async () => {
    const calls = installFakeApi({
      'GET /api/config': emptySnapshot,
      'POST /api/setup/test': okTest,
      'PUT /api/config/lists/radarr/{id}': savedSnapshot(true),
    })
    const wrapper = mount(SetupView)
    await flushPromises()
    await fill(wrapper, 'field-arrUrl', 'http://radarr:7878')
    await fill(wrapper, 'field-arrApiKey', 'key')
    await wrapper.get('[data-testid="test"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-testid="save"]').attributes('disabled')).toBeUndefined()
    await wrapper.get('[data-testid="save"]').trigger('click')
    await flushPromises()

    const gotWrite = calls.find((call) => call.method === 'PUT')
    expect(gotWrite?.path).toBe('/api/config/lists/radarr/http%3A%2F%2Fradarr%3A7878')
    expect(gotWrite?.body).toEqual({ element: { url: 'http://radarr:7878', apiKey: 'key' } })
    expect(wrapper.get('[data-testid="step-indicator"]').text()).toContain('2')
    expect(wrapper.find('[data-testid="field-clientUrl"]').exists()).toBe(true)
  })

  it('SetupView_editingAFieldAfterATestDisablesSaveAgain', async () => {
    installFakeApi({ 'GET /api/config': emptySnapshot, 'POST /api/setup/test': okTest })
    const wrapper = mount(SetupView)
    await flushPromises()
    await fill(wrapper, 'field-arrUrl', 'http://radarr:7878')
    await wrapper.get('[data-testid="test"]').trigger('click')
    await flushPromises()

    await fill(wrapper, 'field-arrUrl', 'http://radarr:7879')

    expect(wrapper.get('[data-testid="save"]').attributes('disabled')).toBeDefined()
  })

  it('SetupView_stripsTheTorrentClientsPrefixFromTheDetail', async () => {
    installFakeApi({
      'GET /api/config': emptySnapshot,
      'POST /api/setup/test': { ...okTest, detail: 'torrentClients[0]: Authentification refusée.' },
      'PUT /api/config/lists/radarr/{id}': emptySnapshot,
    })
    const wrapper = mount(SetupView)
    await flushPromises()
    await fill(wrapper, 'field-arrUrl', 'http://radarr:7878')
    await wrapper.get('[data-testid="test"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-testid="save"]').trigger('click')
    await flushPromises()

    await fill(wrapper, 'field-clientUrl', 'http://qbittorrent:8080')
    await wrapper.get('[data-testid="test"]').trigger('click')
    await flushPromises()

    const gotResult = wrapper.get('[data-testid="test-result"]').text()
    expect(gotResult).toContain('Authentification refusée.')
    expect(gotResult).not.toContain('torrentClients[0]')
  })

  it('SetupView_prefillsFromTheImportedCrossSeedConfiguration', async () => {
    installFakeApi({
      'GET /api/config': {
        ...emptySnapshot,
        lists: {
          radarr: [
            {
              itemId: 'http://radarr:7878',
              element: { url: 'http://radarr:7878', apiKey: null },
              origin: 'cross-seed',
              drift: false,
            },
          ],
          torrentClients: [
            {
              itemId: 'http://qbittorrent:8080',
              element: { type: 'qbittorrent', url: 'http://qbittorrent:8080', user: 'alice' },
              origin: 'cross-seed',
              drift: false,
            },
          ],
        },
      },
    })

    const wrapper = mount(SetupView)
    await flushPromises()

    const gotUrl = (wrapper.get('[data-testid="field-arrUrl"]').element as HTMLInputElement).value
    expect(gotUrl).toBe('http://radarr:7878')
  })

  it('SetupView_providerStepRefusesToFinishWithoutATracker', async () => {
    const calls = installFakeApi({
      'GET /api/config': emptySnapshot,
      'POST /api/setup/test': okTest,
      'PUT /api/config/lists/{list}/{id}': emptySnapshot,
      'PUT /api/config/settings/{key}': emptySnapshot,
    })
    const wrapper = mount(SetupView)
    await flushPromises()
    await goToProviderStep(wrapper)

    await fill(wrapper, 'field-libraryApi', 'https://library.example.com')
    await wrapper.get('[data-testid="save"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-testid="tracker-required"]').text()).not.toBe('')
    expect(wrapper.find('[data-testid="finished"]').exists()).toBe(false)
    expect(calls.some((call) => call.path.includes('/settings/libraryApi'))).toBe(false)
  })

  it('SetupView_showsTheRestartCommandsAtTheEndWhenARestartIsRequired', async () => {
    installFakeApi({
      'GET /api/config': emptySnapshot,
      'POST /api/setup/test': okTest,
      'PUT /api/config/lists/{list}/{id}': savedSnapshot(true),
      'PUT /api/config/settings/{key}': savedSnapshot(true),
    })
    const wrapper = mount(SetupView)
    await flushPromises()
    await goToProviderStep(wrapper)

    await fill(wrapper, 'field-libraryApi', 'https://library.example.com')
    await fill(wrapper, 'field-trackerKey', 'tracker')
    await fill(wrapper, 'field-trackerAnnounce', 'https://tracker.example.com/announce/passkey')
    await wrapper.get('[data-testid="save"]').trigger('click')
    await flushPromises()

    const gotText = wrapper.get('[data-testid="finished"]').text()
    expect(gotText).toContain('Redémarrez Necromanciarr')
    expect(gotText).toContain('docker restart necromanciarr')
    expect(gotText).toContain('necromanciarr serve')
  })

  it('SetupView_doesNotAskForARestartWhenNoneIsRequired', async () => {
    installFakeApi({
      'GET /api/config': emptySnapshot,
      'POST /api/setup/test': okTest,
      'PUT /api/config/lists/{list}/{id}': emptySnapshot,
      'PUT /api/config/settings/{key}': emptySnapshot,
    })
    const wrapper = mount(SetupView)
    await flushPromises()
    await goToProviderStep(wrapper)

    await fill(wrapper, 'field-trackerKey', 'tracker')
    await fill(wrapper, 'field-trackerAnnounce', 'https://tracker.example.com/announce/passkey')
    await wrapper.get('[data-testid="save"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-testid="finished"]').text()).not.toContain('docker restart')
  })

  it('App_showsTheAssistantWhenTheConfigurationIsEmptyAndSkipLeavesItWithoutBlocking', async () => {
    installFakeApi({
      'GET /api/config': emptySnapshot,
      'POST /api/doctor/run': okReport,
      'GET /api/status': busyStatus,
      'GET /api/proposals': [],
    })
    window.location.hash = '#/propositions'
    const wrapper = mount(App)
    await flushPromises()

    expect(wrapper.find('[data-testid="step-indicator"]').exists()).toBe(true)
    await wrapper.get('[data-testid="skip"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="step-indicator"]').exists()).toBe(false)
    expect(window.location.hash).toBe('#/propositions')
  })
})

async function goToProviderStep(wrapper: ReturnType<typeof mount>) {
  await fill(wrapper, 'field-arrUrl', 'http://radarr:7878')
  await wrapper.get('[data-testid="test"]').trigger('click')
  await flushPromises()
  await wrapper.get('[data-testid="save"]').trigger('click')
  await flushPromises()
  await fill(wrapper, 'field-clientUrl', 'http://qbittorrent:8080')
  await wrapper.get('[data-testid="test"]').trigger('click')
  await flushPromises()
  await wrapper.get('[data-testid="save"]').trigger('click')
  await flushPromises()
  await fill(wrapper, 'field-libraryRoots', '/data/library')
  await fill(wrapper, 'field-linkDir', '/data/links')
  await wrapper.get('[data-testid="save"]').trigger('click')
  await flushPromises()
}
