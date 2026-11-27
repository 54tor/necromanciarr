import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from '@/App.vue'
import type { ConfigSnapshot, SetupTestResult } from '@/api/types'
import { busyStatus, okReport } from '@/test/fixtures'
import { installFakeApi, resetAppState } from '@/test/fakeApi'
import { stubViewport } from '@/test/viewport'
import { useSetup } from '@/composables/useSetup'
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
    await wrapper.get('[data-testid="continue"]').trigger('click')
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
    await wrapper.get('[data-testid="continue"]').trigger('click')

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
    await wrapper.get('[data-testid="skip-step"]').trigger('click')

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
    await wrapper.get('[data-testid="skip-step"]').trigger('click')

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

function importedRadarr(element: Record<string, unknown>): ConfigSnapshot {
  return {
    ...emptySnapshot,
    lists: {
      radarr: [{ itemId: 'http://radarr:7878', element, origin: 'cross-seed', drift: false }],
    },
  }
}

async function readNeedsSetup(snapshot: ConfigSnapshot, isSkipped = false): Promise<boolean> {
  installFakeApi({ 'GET /api/config': snapshot })
  const setup = useSetup()
  if (isSkipped) setup.skip()
  await setup.load()
  return setup.needsSetup.value
}

describe('needsSetup', () => {
  beforeEach(() => resetAppState())

  it('needsSetup_isTrueWhenTheConfigurationIsEmpty', async () => {
    expect(await readNeedsSetup(emptySnapshot)).toBe(true)
  })

  it('needsSetup_isTrueWhenAnImportedServiceHasNoApiKey', async () => {
    expect(await readNeedsSetup(importedRadarr({ url: 'http://radarr:7878' }))).toBe(true)
  })

  it('needsSetup_isFalseWhenATorrentClientHasNoPassword', async () => {
    const snapshot: ConfigSnapshot = {
      ...emptySnapshot,
      lists: {
        radarr: [
          {
            itemId: 'http://radarr:7878',
            element: { url: 'http://radarr:7878', apiKey: null },
            origin: 'override',
            drift: false,
          },
        ],
        torrentClients: [
          {
            itemId: 'http://qbittorrent:8080',
            element: { url: 'http://qbittorrent:8080' },
            origin: 'cross-seed',
            drift: false,
          },
        ],
      },
    }
    expect(await readNeedsSetup(snapshot)).toBe(false)
  })

  it('needsSetup_isFalseWhenEverySecretIsSet', async () => {
    const snapshot = importedRadarr({ url: 'http://radarr:7878', apiKey: null })
    expect(await readNeedsSetup(snapshot)).toBe(false)
  })

  it('needsSetup_isFalseOnceSkipped', async () => {
    expect(await readNeedsSetup(emptySnapshot, true)).toBe(false)
  })
})

describe('SetupView secrets and services', () => {
  beforeEach(() => {
    resetAppState()
    stubViewport(1280)
  })
  afterEach(() => vi.unstubAllGlobals())

  it('SetupView_clearsTheTypedSecretsOnceTheStepIsSaved', async () => {
    installFakeApi({
      'GET /api/config': emptySnapshot,
      'POST /api/setup/test': okTest,
      'PUT /api/config/lists/radarr/{id}': emptySnapshot,
    })
    const wrapper = mount(SetupView)
    await flushPromises()
    await fill(wrapper, 'field-arrUrl', 'http://radarr:7878')
    await fill(wrapper, 'field-arrApiKey', 'key')
    await wrapper.get('[data-testid="test"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-testid="save"]').trigger('click')
    await flushPromises()

    expect(useSetup().form.arrApiKey).toBe('')
  })

  it('SetupView_clearsTheTypedSecretsWhenLeavingTheWizard', async () => {
    installFakeApi({ 'GET /api/config': emptySnapshot })
    const wrapper = mount(SetupView)
    await flushPromises()
    await fill(wrapper, 'field-arrApiKey', 'key')

    wrapper.unmount()

    expect(useSetup().form.arrApiKey).toBe('')
  })

  it('SetupView_addAnotherServiceClearsTheFormAndTheSecondSaveWritesItsOwnItem', async () => {
    const calls = installFakeApi({
      'GET /api/config': emptySnapshot,
      'POST /api/setup/test': okTest,
      'PUT /api/config/lists/{list}/{id}': emptySnapshot,
    })
    const wrapper = mount(SetupView)
    await flushPromises()
    await fill(wrapper, 'field-arrUrl', 'http://radarr:7878')
    await wrapper.get('[data-testid="test"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-testid="save"]').trigger('click')
    await flushPromises()

    await wrapper.get('[data-testid="add-another"]').trigger('click')

    const gotUrl = (wrapper.get('[data-testid="field-arrUrl"]').element as HTMLInputElement).value
    expect(gotUrl).toBe('')
    await fill(wrapper, 'field-arrKind', 'sonarr')
    await fill(wrapper, 'field-arrUrl', 'http://sonarr:8989')
    await wrapper.get('[data-testid="test"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-testid="save"]').trigger('click')
    await flushPromises()
    const gotPaths = calls.filter((call) => call.method === 'PUT').map((call) => call.path)
    expect(gotPaths).toEqual([
      '/api/config/lists/radarr/http%3A%2F%2Fradarr%3A7878',
      '/api/config/lists/sonarr/http%3A%2F%2Fsonarr%3A8989',
    ])
  })
})

async function goToIndexersStep(wrapper: ReturnType<typeof mount>) {
  await goToProviderStep(wrapper)
  await fill(wrapper, 'field-trackerKey', 'tracker')
  await fill(wrapper, 'field-trackerAnnounce', 'https://tracker.example.com/announce/passkey')
  await wrapper.get('[data-testid="save"]').trigger('click')
  await flushPromises()
}

async function fillIndexer(wrapper: ReturnType<typeof mount>) {
  await fill(wrapper, 'field-indexerName', 'alpha')
  await fill(wrapper, 'field-indexerUrl', 'https://prowlarr.example.com:9696/7/api')
  await fill(wrapper, 'field-indexerApiKey', 'dummy-api-key')
  await fill(wrapper, 'field-indexerTracker', 'Alpha')
}

async function testAndSaveIndexer(wrapper: ReturnType<typeof mount>) {
  await wrapper.get('[data-testid="test"]').trigger('click')
  await flushPromises()
  await wrapper.get('[data-testid="save"]').trigger('click')
  await flushPromises()
}

const indexerRoutes = (snapshot: ConfigSnapshot) => ({
  'GET /api/config': snapshot,
  'POST /api/setup/test': okTest,
  'PUT /api/config/lists/{list}/{id}': snapshot,
  'PUT /api/config/settings/{key}': snapshot,
})

async function goToProviderStep(wrapper: ReturnType<typeof mount>) {
  await fill(wrapper, 'field-arrUrl', 'http://radarr:7878')
  await wrapper.get('[data-testid="test"]').trigger('click')
  await flushPromises()
  await wrapper.get('[data-testid="save"]').trigger('click')
  await flushPromises()
  await wrapper.get('[data-testid="continue"]').trigger('click')
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

describe('SetupView indexers step', () => {
  beforeEach(() => {
    resetAppState()
    stubViewport(1280)
  })
  afterEach(() => vi.unstubAllGlobals())

  it('SetupView_indexersStepIsTheFifthAndLastStepWithOneHelpSentencePerField', async () => {
    installFakeApi(indexerRoutes(emptySnapshot))
    const wrapper = mount(SetupView)
    await flushPromises()

    await goToIndexersStep(wrapper)

    expect(wrapper.get('[data-testid="step-indicator"]').text()).toContain('5')
    const gotHelps = wrapper.findAll('[data-testid^="help-indexer"], [data-testid="help-prowlarr"]')
    expect(gotHelps).toHaveLength(5)
    gotHelps.forEach((help) => expect(help.text()).not.toBe(''))
    expect(wrapper.get('[data-testid="field-indexerApiKey"]').attributes('type')).toBe('password')
    expect(wrapper.get('[data-testid="save"]').attributes('disabled')).toBeDefined()
  })

  it('SetupView_skippingTheIndexersStepWritesNothingAndShowsTheFinalScreen', async () => {
    const calls = installFakeApi(indexerRoutes(emptySnapshot))
    const wrapper = mount(SetupView)
    await flushPromises()
    await goToIndexersStep(wrapper)
    const writesBefore = calls.filter((call) => call.method === 'PUT').length

    await wrapper.get('[data-testid="skip-step"]').trigger('click')

    expect(wrapper.find('[data-testid="finished"]').exists()).toBe(true)
    expect(calls.filter((call) => call.method === 'PUT')).toHaveLength(writesBefore)
  })

  it('SetupView_indexerIsTestedWithTheTorznabKindThenSavedByNameWithoutSourceOrder', async () => {
    const calls = installFakeApi(indexerRoutes(emptySnapshot))
    const wrapper = mount(SetupView)
    await flushPromises()
    await goToIndexersStep(wrapper)
    await fillIndexer(wrapper)

    await testAndSaveIndexer(wrapper)

    const gotTest = calls.filter((call) => call.method === 'POST').at(-1)
    expect(gotTest?.body).toEqual({
      kind: 'torznab',
      name: 'alpha',
      url: 'https://prowlarr.example.com:9696/7/api',
      apiKey: 'dummy-api-key',
      username: '',
      password: '',
    })
    const gotWrites = calls.filter((call) => call.method === 'PUT').slice(-1)
    expect(gotWrites.map((call) => call.path)).toEqual(['/api/config/lists/torznab/alpha'])
    expect(gotWrites[0]?.body).toEqual({
      element: {
        url: 'https://prowlarr.example.com:9696/7/api',
        apiKey: 'dummy-api-key',
        name: 'alpha',
        tracker: 'Alpha',
      },
    })
    expect(calls.some((call) => call.path.endsWith('/settings/sourceOrder'))).toBe(false)
    expect(useSetup().form.indexerApiKey).toBe('')
  })

  it('SetupView_indexerSaveAppendsToAStoredSourceOrder', async () => {
    const storedOrder: ConfigSnapshot = {
      ...emptySnapshot,
      settings: {
        sourceOrder: {
          value: ['provider', 'torznab:beta'],
          origin: 'added',
          locked: false,
          secret: false,
          set: true,
        },
      },
    }
    const calls = installFakeApi(indexerRoutes(storedOrder))
    const wrapper = mount(SetupView)
    await flushPromises()
    await goToIndexersStep(wrapper)
    await fillIndexer(wrapper)

    await testAndSaveIndexer(wrapper)

    const gotOrderWrite = calls.find((call) => call.path === '/api/config/settings/sourceOrder')
    expect(gotOrderWrite?.body).toEqual({ value: ['provider', 'torznab:beta', 'torznab:alpha'] })
  })

  it('SetupView_indexerSaveWritesTheDiagnosticOnlyProwlarrSettingWhenFilled', async () => {
    const calls = installFakeApi(indexerRoutes(emptySnapshot))
    const wrapper = mount(SetupView)
    await flushPromises()
    await goToIndexersStep(wrapper)
    await fillIndexer(wrapper)
    await fill(wrapper, 'field-prowlarr', 'http://prowlarr:9696?apikey=dummy-api-key')

    await testAndSaveIndexer(wrapper)

    const gotWrite = calls.find((call) => call.path === '/api/config/settings/prowlarr')
    expect(gotWrite?.body).toEqual({ value: 'http://prowlarr:9696?apikey=dummy-api-key' })
    expect(useSetup().form.prowlarr).toBe('')
  })

  it('SetupView_addAnotherIndexerResetsTheFormAndContinueShowsTheFinalScreen', async () => {
    installFakeApi(indexerRoutes(emptySnapshot))
    const wrapper = mount(SetupView)
    await flushPromises()
    await goToIndexersStep(wrapper)
    await fillIndexer(wrapper)
    await testAndSaveIndexer(wrapper)

    await wrapper.get('[data-testid="add-another"]').trigger('click')

    const gotName = (wrapper.get('[data-testid="field-indexerName"]').element as HTMLInputElement)
      .value
    expect(gotName).toBe('')
    await fillIndexer(wrapper)
    await testAndSaveIndexer(wrapper)
    await wrapper.get('[data-testid="continue"]').trigger('click')
    expect(wrapper.find('[data-testid="finished"]').exists()).toBe(true)
  })

  it('SetupView_editingAnIndexerFieldAfterATestDisablesSaveAgain', async () => {
    installFakeApi(indexerRoutes(emptySnapshot))
    const wrapper = mount(SetupView)
    await flushPromises()
    await goToIndexersStep(wrapper)
    await fillIndexer(wrapper)
    await wrapper.get('[data-testid="test"]').trigger('click')
    await flushPromises()

    await fill(wrapper, 'field-indexerUrl', 'https://prowlarr.example.com:9696/8/api')

    expect(wrapper.get('[data-testid="save"]').attributes('disabled')).toBeDefined()
  })
})
