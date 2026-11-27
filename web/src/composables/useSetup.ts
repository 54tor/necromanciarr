import { computed, reactive, ref, watch } from 'vue'
import { api } from '@/api/endpoints'
import type { ConfigItem, ConfigSnapshot, SetupCandidate, SetupTestResult } from '@/api/types'
import { useApi } from '@/composables/useApi'
import { endpointId } from '@/lib/configFields'
import { setupSkipped } from '@/state/session'

const SETUP_STEPS = ['arr', 'torrentClient', 'folders', 'provider'] as const
export type SetupStepId = (typeof SETUP_STEPS)[number]
type ArrKind = 'radarr' | 'sonarr'

const CROSS_SEED_ORIGIN = 'cross-seed'
const TESTED_STEPS: readonly SetupStepId[] = ['arr', 'torrentClient']
const CLIENT_PREFIX = /^torrentClients\[\d+\]:\s*/

interface SetupForm {
  arrKind: ArrKind
  arrUrl: string
  arrApiKey: string
  clientUrl: string
  clientUsername: string
  clientPassword: string
  libraryRoots: string
  linkDir: string
  libraryApi: string
  libraryToken: string
  trackerKey: string
  trackerAnnounce: string
}

const emptyForm = (): SetupForm => ({
  arrKind: 'radarr',
  arrUrl: '',
  arrApiKey: '',
  clientUrl: '',
  clientUsername: '',
  clientPassword: '',
  libraryRoots: '',
  linkDir: '',
  libraryApi: '',
  libraryToken: '',
  trackerKey: '',
  trackerAnnounce: '',
})

const snapshot = ref<ConfigSnapshot>()
const stepIndex = ref(0)
const finished = ref(false)
const testResult = ref<SetupTestResult>()
const trackerMissing = ref(false)
const isPrefilled = ref(false)
const arrSaved = ref(false)
const form = reactive<SetupForm>(emptyForm())

/** Exposé pour que les tests remettent l'état partagé à zéro entre deux cas. */
export function resetSetupState(): void {
  snapshot.value = undefined
  stepIndex.value = 0
  finished.value = false
  testResult.value = undefined
  trackerMissing.value = false
  isPrefilled.value = false
  arrSaved.value = false
  setupSkipped.value = false
  Object.assign(form, emptyForm())
}

function clearSecrets(): void {
  form.arrApiKey = ''
  form.clientPassword = ''
  form.libraryToken = ''
  form.trackerAnnounce = ''
}

const TESTED_FIELDS = (): string[] => [
  form.arrKind,
  form.arrUrl,
  form.arrApiKey,
  form.clientUrl,
  form.clientUsername,
  form.clientPassword,
]

watch(TESTED_FIELDS, () => {
  testResult.value = undefined
})

const itemsOf = (list: string): ConfigItem[] => snapshot.value?.lists?.[list] ?? []
const importedItem = (list: string) =>
  itemsOf(list).find((item) => item.origin === CROSS_SEED_ORIGIN)
const textOf = (value: unknown): string => (typeof value === 'string' ? value : '')

function prefillFromCrossSeed(): void {
  if (isPrefilled.value) return
  isPrefilled.value = true
  const arr = importedItem('radarr') ?? importedItem('sonarr')
  if (arr) {
    form.arrKind = importedItem('radarr') ? 'radarr' : 'sonarr'
    form.arrUrl = textOf(arr.element.url)
  }
  const client = importedItem('torrentClients')
  if (client) {
    form.clientUrl = textOf(client.element.url)
    form.clientUsername = textOf(client.element.user)
  }
  const roots = snapshot.value?.settings?.libraryRoots?.value
  if (Array.isArray(roots)) form.libraryRoots = roots.join('\n')
  const linkDir = importedItem('linkDirs')
  if (linkDir) form.linkDir = textOf(linkDir.element.path)
}

const SETUP_LISTS = ['radarr', 'sonarr', 'torrentClients']
const ARR_LISTS_WITH_REQUIRED_KEY = ['radarr', 'sonarr']
const REQUIRED_KEY_FIELD = 'apiKey'

const isConfigEmpty = computed(
  () => snapshot.value !== undefined && SETUP_LISTS.every((list) => itemsOf(list).length === 0),
)
// Le serveur garde la clé d'un secret posé (valeur null) et omet celle d'un secret absent ; le mot de passe d'un
// client torrent reste facultatif (qBittorrent local sans authentification).
const hasUnsetSecret = computed(() =>
  ARR_LISTS_WITH_REQUIRED_KEY.some((list) =>
    itemsOf(list).some((item) => !(REQUIRED_KEY_FIELD in item.element)),
  ),
)
const needsSetup = computed(
  () =>
    snapshot.value !== undefined &&
    (isConfigEmpty.value || hasUnsetSecret.value) &&
    !setupSkipped.value,
)
const currentStep = computed<SetupStepId>(() => SETUP_STEPS[stepIndex.value] ?? 'provider')
const restartRequired = computed(() => snapshot.value?.restartRequired === true)
const isTestPassed = computed(
  () => testResult.value?.status === 'ok' || testResult.value?.status === 'warn',
)
const needsTest = computed(() => TESTED_STEPS.includes(currentStep.value))
const canSave = computed(() => !needsTest.value || isTestPassed.value)

export const displayDetail = (detail: string): string => detail.replace(CLIENT_PREFIX, '')

const readLines = (text: string): string[] =>
  text
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => line !== '')

function omitBlank(entries: Record<string, string>): Record<string, string> {
  return Object.fromEntries(Object.entries(entries).filter(([, value]) => value.trim() !== ''))
}

export function useSetup() {
  const { call, pending } = useApi()

  async function load(): Promise<void> {
    try {
      snapshot.value = await api.getConfig()
    } catch {
      return
    }
    prefillFromCrossSeed()
  }

  async function testCandidate(
    kind: SetupCandidate['kind'],
    fields: Omit<SetupCandidate, 'kind'>,
  ): Promise<void> {
    testResult.value = await call(() => api.testCandidate({ kind, ...fields }))
  }

  function runTest(): Promise<void> {
    if (currentStep.value === 'torrentClient') {
      return testCandidate('qbittorrent', {
        url: form.clientUrl,
        apiKey: '',
        username: form.clientUsername,
        password: form.clientPassword,
      })
    }
    return testCandidate(form.arrKind, {
      url: form.arrUrl,
      apiKey: form.arrApiKey,
      username: '',
      password: '',
    })
  }

  async function writeItem(list: string, itemId: string, element: Record<string, unknown>) {
    const written = await call(() => api.putConfigItem(list, itemId, element))
    if (written) snapshot.value = written
    return written !== undefined
  }

  async function writeSetting(key: string, value: unknown) {
    const written = await call(() => api.putConfigSetting(key, value))
    if (written) snapshot.value = written
    return written !== undefined
  }

  async function saveArr(): Promise<boolean> {
    const element = omitBlank({ url: form.arrUrl, apiKey: form.arrApiKey })
    return writeItem(form.arrKind, endpointId(form.arrUrl), element)
  }

  async function saveTorrentClient(): Promise<boolean> {
    const element = omitBlank({
      type: 'qbittorrent',
      url: form.clientUrl,
      user: form.clientUsername,
      password: form.clientPassword,
    })
    return writeItem('torrentClients', endpointId(form.clientUrl), element)
  }

  async function saveFolders(): Promise<boolean> {
    const isRootsSaved = await writeSetting('libraryRoots', readLines(form.libraryRoots))
    if (!isRootsSaved) return false
    const linkDir = form.linkDir.trim()
    return linkDir === '' || writeItem('linkDirs', linkDir, { path: linkDir })
  }

  async function saveProvider(): Promise<boolean> {
    const trackerKey = form.trackerKey.trim()
    const announce = readLines(form.trackerAnnounce)
    trackerMissing.value = trackerKey === '' || announce.length === 0
    if (trackerMissing.value) return false
    const isApiSaved =
      form.libraryApi.trim() === '' || (await writeSetting('libraryApi', form.libraryApi.trim()))
    const isTokenSaved =
      form.libraryToken.trim() === '' ||
      (await writeSetting('libraryToken', form.libraryToken.trim()))
    if (!isApiSaved || !isTokenSaved) return false
    return writeItem('trackers', trackerKey, { key: trackerKey, announce })
  }

  const SAVERS: Record<SetupStepId, () => Promise<boolean>> = {
    arr: saveArr,
    torrentClient: saveTorrentClient,
    folders: saveFolders,
    provider: saveProvider,
  }

  async function saveStep(): Promise<void> {
    if (!canSave.value) return
    if (!(await SAVERS[currentStep.value]())) return
    testResult.value = undefined
    clearSecrets()
    if (currentStep.value === 'arr') arrSaved.value = true
    else advance()
  }

  function advance(): void {
    if (stepIndex.value === SETUP_STEPS.length - 1) finished.value = true
    else stepIndex.value += 1
  }

  function continueAfterArr(): void {
    arrSaved.value = false
    advance()
  }

  function addAnotherArr(): void {
    form.arrKind = 'radarr'
    form.arrUrl = ''
    form.arrApiKey = ''
    testResult.value = undefined
    arrSaved.value = false
  }

  return {
    steps: SETUP_STEPS,
    currentStep,
    stepNumber: computed(() => stepIndex.value + 1),
    form,
    testResult,
    trackerMissing,
    finished,
    restartRequired,
    arrSaved,
    needsSetup,
    needsTest,
    canSave,
    pending,
    load,
    testCandidate,
    runTest,
    saveStep,
    continueAfterArr,
    addAnotherArr,
    clearSecrets,
    skip: () => {
      setupSkipped.value = true
    },
  }
}
