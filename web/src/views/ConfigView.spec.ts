import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ConfigSnapshot } from '@/api/types'
import { failure, installFakeApi, resetAppState, type FakeCall } from '@/test/fakeApi'
import { stubViewport } from '@/test/viewport'
import ConfigView from './ConfigView.vue'

const toastError = vi.fn()
vi.mock('vue-sonner', () => ({
  toast: { error: (message: string) => toastError(message), success: vi.fn() },
}))

const RADARR_ID = 'http://radarr.example.com:7878'
const ENCODED_RADARR_ID = 'http%3A%2F%2Fradarr.example.com%3A7878'
const ORPHAN_ID = 'http://gone.example.com:7878'

function configSnapshot(overrides: Partial<ConfigSnapshot> = {}): ConfigSnapshot {
  return {
    settings: {
      listen: {
        value: '127.0.0.1:8080',
        origin: 'default',
        locked: false,
        secret: false,
        set: false,
      },
      linkType: { value: 'symlink', origin: 'crossseed', locked: false, secret: false, set: true },
      language: { value: 'fr', origin: 'override', locked: false, secret: false, set: true },
      prowlarr: { value: null, origin: 'default', locked: false, secret: true, set: false },
    },
    lists: {
      radarr: [
        {
          itemId: RADARR_ID,
          element: { name: 'films', url: null, apiKey: null },
          origin: 'override',
          drift: true,
        },
      ],
      linkDirs: [
        { itemId: '/data/a', element: { path: '/data/a' }, origin: 'cross-seed', drift: false },
        { itemId: '/data/b', element: { path: '/data/b' }, origin: 'added', drift: false },
      ],
    },
    orphans: [],
    restartRequired: false,
    ...overrides,
  }
}

const mounted: VueWrapper[] = []
async function mountView(routes: Parameters<typeof installFakeApi>[0]): Promise<{
  wrapper: VueWrapper
  calls: FakeCall[]
}> {
  const calls = installFakeApi(routes)
  const wrapper = mount(ConfigView, { attachTo: document.body })
  mounted.push(wrapper)
  await flushPromises()
  return { wrapper, calls }
}

const bodyElement = <Element extends HTMLElement>(testId: string) =>
  document.body.querySelector<Element>(`[data-testid="${testId}"]`)

const writeCalls = (calls: FakeCall[]) => calls.filter((call) => call.method !== 'GET')

async function clickInBody(testId: string): Promise<void> {
  bodyElement<HTMLButtonElement>(testId)?.click()
  await flushPromises()
}

describe('ConfigView', () => {
  beforeEach(() => {
    resetAppState()
    stubViewport(1280)
    toastError.mockClear()
  })
  afterEach(() => {
    mounted.splice(0).forEach((wrapper) => wrapper.unmount())
    vi.unstubAllGlobals()
    document.body.innerHTML = ''
  })

  it("ConfigView affiche l'origine de chaque valeur", async () => {
    const { wrapper } = await mountView({ 'GET /api/config': configSnapshot() })

    const originOf = (rowKey: string) =>
      wrapper.get(`tr[data-row-key="${rowKey}"] [data-testid="config-origin"]`).text()

    expect(originOf('listen')).toBe('Valeur par défaut')
    expect(originOf('linkType')).toBe('cross-seed')
    expect(originOf('language')).toBe('Surcharge')
    expect(originOf(`radarr:${RADARR_ID}`)).toBe('Surcharge')
    expect(originOf('linkDirs:/data/a')).toBe('cross-seed')
    expect(originOf('linkDirs:/data/b')).toBe('Ajout')
  })

  it("ConfigView verrouille un réglage imposé par l'environnement", async () => {
    const settings = configSnapshot().settings!
    const locked = { ...settings.listen!, origin: 'env' as const, locked: true, set: true }
    const { wrapper } = await mountView({
      'GET /api/config': configSnapshot({ settings: { ...settings, listen: locked } }),
    })

    const lockedRow = wrapper.get('tr[data-row-key="listen"]')
    expect(lockedRow.get('[data-testid="config-origin"]').text()).toBe('Environnement')
    expect(lockedRow.get('[data-testid="config-locked"]').text()).toBe('Verrouillé')

    await wrapper.get('[data-testid="edit-setting-listen"]').trigger('click')
    await flushPromises()

    expect(bodyElement('config-locked-warning')?.textContent).toContain(
      'L’environnement impose cette valeur',
    )
  })

  it("ConfigView n'envoie pas un champ secret laissé vide", async () => {
    const { wrapper, calls } = await mountView({
      'GET /api/config': configSnapshot(),
      'PUT /api/config/lists/{list}/{itemId}': configSnapshot(),
    })

    await wrapper.get(`[data-testid="edit-item-radarr-${RADARR_ID}"]`).trigger('click')
    await flushPromises()
    const secretInput = bodyElement<HTMLInputElement>('config-field-apiKey')
    expect(secretInput?.type).toBe('password')
    expect(secretInput?.value).toBe('')
    expect(bodyElement('config-field-state-apiKey')?.textContent).toContain('Défini')
    const nameInput = bodyElement<HTMLInputElement>('config-field-name')!
    nameInput.value = 'films-4k'
    nameInput.dispatchEvent(new Event('input'))
    await clickInBody('config-form-submit')

    const [written] = writeCalls(calls)
    expect(written?.method).toBe('PUT')
    expect(written?.path).toBe(`/api/config/lists/radarr/${ENCODED_RADARR_ID}`)
    expect(written?.body).toEqual({ element: { name: 'films-4k' } })
  })

  it('ConfigView montre le bandeau de redémarrage après une écriture', async () => {
    const { wrapper, calls } = await mountView({
      'GET /api/config': configSnapshot(),
      'PUT /api/config/settings/{key}': configSnapshot({ restartRequired: true }),
    })
    expect(wrapper.find('[data-testid="config-restart"]').exists()).toBe(false)

    await wrapper.get('[data-testid="edit-setting-linkType"]').trigger('click')
    await flushPromises()
    await clickInBody('config-form-submit')

    expect(writeCalls(calls).map((call) => call.body)).toEqual([{ value: 'symlink' }])
    expect(wrapper.get('[data-testid="config-restart"]').text()).toContain('Redémarrez')
  })

  it('ConfigView signale un élément orphelin', async () => {
    const { wrapper, calls } = await mountView({
      'GET /api/config': configSnapshot({ orphans: [{ List: 'radarr', ItemID: ORPHAN_ID }] }),
      'DELETE /api/config/lists/{list}/{itemId}': configSnapshot(),
    })

    const orphanRow = wrapper.get(`tr[data-row-key="radarr:${ORPHAN_ID}"]`)
    expect(orphanRow.get('[data-testid="config-orphan"]').text()).toBe('Orphelin')
    expect(
      wrapper.get(`tr[data-row-key="radarr:${RADARR_ID}"] [data-testid="config-drift"]`).text(),
    ).toBe('Dérive')

    await orphanRow.get(`[data-testid="delete-orphan-radarr-${ORPHAN_ID}"]`).trigger('click')
    await flushPromises()
    await clickInBody('confirm-dialog-confirm')

    expect(writeCalls(calls).map((call) => `${call.method} ${call.path}`)).toEqual([
      'DELETE /api/config/lists/radarr/http%3A%2F%2Fgone.example.com%3A7878',
    ])
  })

  it('ConfigView revient à cross-seed par la route unset avec un identifiant encodé', async () => {
    const { wrapper, calls } = await mountView({
      'GET /api/config': configSnapshot(),
      'POST /api/config/lists/{list}/{itemId}/unset': configSnapshot(),
      'DELETE /api/config/settings/{key}': configSnapshot(),
    })
    expect(wrapper.find('[data-testid="unset-item-linkDirs-/data/a"]').exists()).toBe(false)

    await wrapper.get(`[data-testid="unset-item-radarr-${RADARR_ID}"]`).trigger('click')
    await flushPromises()
    await wrapper.get('[data-testid="unset-setting-language"]').trigger('click')
    await flushPromises()

    expect(writeCalls(calls).map((call) => `${call.method} ${call.path}`)).toEqual([
      `POST /api/config/lists/radarr/${ENCODED_RADARR_ID}/unset`,
      'DELETE /api/config/settings/language',
    ])
  })

  it('ConfigView affiche le message dédié quand la clé de scellement manque', async () => {
    const { wrapper } = await mountView({
      'GET /api/config': configSnapshot(),
      'PUT /api/config/settings/{key}': () => failure(409, 'secret_key_missing'),
    })

    await wrapper.get('[data-testid="edit-setting-prowlarr"]').trigger('click')
    await flushPromises()
    const secretInput = bodyElement<HTMLInputElement>('config-field-prowlarr')!
    secretInput.value = 'http://prowlarr.example.com/?apikey=dummy'
    secretInput.dispatchEvent(new Event('input'))
    await clickInBody('config-form-submit')

    expect(toastError).toHaveBeenCalledTimes(1)
    expect(String(toastError.mock.calls[0]?.[0])).toContain('NECROMANCIARR_SECRET_KEY')
    expect(String(toastError.mock.calls[0]?.[0])).not.toContain('dummy')
  })

  it('ConfigView affiche une configuration vide sans erreur', async () => {
    const { wrapper } = await mountView({
      'GET /api/config': { settings: {}, lists: null, orphans: null, restartRequired: false },
    })

    expect(wrapper.text()).toContain('Aucun réglage.')
    expect(wrapper.text()).toContain('Aucun élément.')
    expect(wrapper.find('[data-testid="add-item-radarr"]').exists()).toBe(true)
  })
})
