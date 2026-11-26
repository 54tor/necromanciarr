import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ConfigSetting, ConfigSnapshot } from '@/api/types'
import { failure, installFakeApi, resetAppState, type FakeCall } from '@/test/fakeApi'
import { stubViewport } from '@/test/viewport'
import ConfigView from './ConfigView.vue'

const toastError = vi.fn()
vi.mock('vue-sonner', () => ({
  toast: {
    error: (message: string, options?: unknown) => toastError(message, options),
    success: vi.fn(),
  },
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
          element: { user: 'films', url: null, apiKey: null },
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
    expect(bodyElement('config-field-apiKey-state')?.textContent).toContain('Défini')
    const nameInput = bodyElement<HTMLInputElement>('config-field-user')!
    expect(nameInput.value).toBe('films')
    nameInput.value = 'films-4k'
    nameInput.dispatchEvent(new Event('input'))
    await clickInBody('config-form-submit')

    const [written] = writeCalls(calls)
    expect(written?.method).toBe('PUT')
    expect(written?.path).toBe(`/api/config/lists/radarr/${ENCODED_RADARR_ID}`)
    expect(written?.body).toEqual({ element: { user: 'films-4k' } })
  })

  describe('annonces de tracker', () => {
    const trackerSnapshot = () =>
      configSnapshot({
        lists: {
          trackers: [
            {
              itemId: 'tracker-a',
              element: { key: 'tracker-a', announce: null, aliases: ['ta'] },
              origin: 'added',
              drift: false,
            },
          ],
        },
      })

    async function saveTrackerWithAnnounce(announceText: string): Promise<FakeCall | undefined> {
      const { wrapper, calls } = await mountView({
        'GET /api/config': trackerSnapshot(),
        'PUT /api/config/lists/{list}/{itemId}': trackerSnapshot(),
      })
      await wrapper.get('[data-testid="edit-item-trackers-tracker-a"]').trigger('click')
      await flushPromises()
      const announceField = bodyElement<HTMLTextAreaElement>('config-field-announce')!
      expect(announceField.tagName).toBe('TEXTAREA')
      expect(announceField.value).toBe('')
      expect(announceField.dataset.masked).toBe('true')
      announceField.value = announceText
      announceField.dispatchEvent(new Event('input'))
      await clickInBody('config-form-submit')
      return writeCalls(calls)[0]
    }

    it("ConfigView n'envoie pas une annonce de tracker laissée vide", async () => {
      const valeurAttendue = { element: { key: 'tracker-a', aliases: ['ta'] } }

      const valeurObtenue = (await saveTrackerWithAnnounce(''))?.body

      expect(valeurObtenue).toEqual(valeurAttendue)
    })

    it('ConfigView envoie les annonces de tracker en tableau, une par ligne', async () => {
      const valeurAttendue = {
        element: {
          key: 'tracker-a',
          announce: [
            'https://t.example.com/announce/fake-0000',
            'udp://t.example.com:6969/announce',
          ],
          aliases: ['ta'],
        },
      }

      const written = await saveTrackerWithAnnounce(
        '  https://t.example.com/announce/fake-0000 \n\nudp://t.example.com:6969/announce\n',
      )

      expect(written?.path).toBe('/api/config/lists/trackers/tracker-a')
      expect(written?.body).toEqual(valeurAttendue)
    })

    it('ConfigView révèle une annonce masquée à la demande', async () => {
      await mountView({ 'GET /api/config': trackerSnapshot() })
      mounted[0]!.get('[data-testid="edit-item-trackers-tracker-a"]').trigger('click')
      await flushPromises()

      await clickInBody('config-field-announce-reveal')

      expect(bodyElement('config-field-announce')?.dataset.masked).toBeUndefined()
    })
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
      'POST /api/config/lists/{list}/{itemId}/unset': configSnapshot(),
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
      'POST /api/config/lists/radarr/http%3A%2F%2Fgone.example.com%3A7878/unset',
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

  it('ConfigView garde le dialogue ouvert et montre la raison du refus', async () => {
    const refusalDetail = 'configuration refused: radarr: missing apiKey'
    let refusesTheWrite = true
    const { wrapper } = await mountView({
      'GET /api/config': configSnapshot(),
      'PUT /api/config/settings/{key}': () =>
        refusesTheWrite
          ? new Response(
              JSON.stringify({ error: { code: 'usage', message: 'x', detail: refusalDetail } }),
              { status: 400 },
            )
          : configSnapshot(),
    })

    await wrapper.get('[data-testid="edit-setting-prowlarr"]').trigger('click')
    await flushPromises()
    const secretInput = bodyElement<HTMLInputElement>('config-field-prowlarr')!
    secretInput.value = 'http://prowlarr.example.com/?apikey=dummy'
    secretInput.dispatchEvent(new Event('input'))
    await clickInBody('config-form-submit')

    expect(toastError).toHaveBeenCalledWith(expect.any(String), { description: refusalDetail })
    expect(bodyElement<HTMLInputElement>('config-field-prowlarr')?.value).toBe(
      'http://prowlarr.example.com/?apikey=dummy',
    )

    refusesTheWrite = false
    await clickInBody('config-form-submit')

    expect(bodyElement('config-form-submit')).toBeNull()
  })

  describe('formulaires typés', () => {
    const settingsWith = (key: string, setting: Partial<ConfigSetting>): ConfigSnapshot =>
      configSnapshot({
        settings: {
          [key]: {
            value: null,
            origin: 'default',
            locked: false,
            secret: false,
            set: false,
            ...setting,
          },
        },
      })

    async function saveSetting(snapshot: ConfigSnapshot, key: string, edit: () => Promise<void>) {
      const { wrapper, calls } = await mountView({
        'GET /api/config': snapshot,
        'PUT /api/config/settings/{key}': snapshot,
      })
      await wrapper.get(`[data-testid="edit-setting-${key}"]`).trigger('click')
      await flushPromises()
      await edit()
      await clickInBody('config-form-submit')
      return writeCalls(calls)[0]
    }

    const setField = async (testId: string, value: string) => {
      const field = bodyElement<HTMLInputElement>(testId)!
      field.value = value
      field.dispatchEvent(new Event(field.tagName === 'SELECT' ? 'change' : 'input'))
      await flushPromises()
    }

    it('ConfigView propose une liste de choix pour un réglage à valeurs fixes', async () => {
      const snapshot = settingsWith('crossSeedMode', {
        value: 'auto',
        origin: 'crossseed',
        set: true,
      })

      const written = await saveSetting(snapshot, 'crossSeedMode', async () => {
        const select = bodyElement<HTMLSelectElement>('config-field-crossSeedMode')!
        expect(select.tagName).toBe('SELECT')
        expect(Array.from(select.options).map((option) => option.value)).toEqual([
          '',
          'auto',
          'delegate',
          'native',
          'off',
        ])
        await setField('config-field-crossSeedMode', 'native')
      })

      expect(written?.body).toEqual({ value: 'native' })
    })

    it('ConfigView édite la déduplication par champs et convertit la durée', async () => {
      const dedupe = {
        torrentRoots: ['/data/torrents'],
        kinds: ['symlink'],
        relativeSymlinks: true,
        minFileAge: 600_000_000_000,
        recheckAfter: false,
        verify: 'auto',
      }

      const written = await saveSetting(
        settingsWith('dedupe', { value: dedupe, origin: 'override', set: true }),
        'dedupe',
        async () => {
          expect(bodyElement<HTMLInputElement>('config-field-dedupe.minFileAge')?.value).toBe('10m')
          expect(bodyElement<HTMLInputElement>('config-field-dedupe.kinds.symlink')?.checked).toBe(
            true,
          )
          await setField('config-field-dedupe.verify', 'hash')
          bodyElement<HTMLInputElement>('config-field-dedupe.kinds.hardlink')!.click()
          await flushPromises()
        },
      )

      expect(written?.body).toEqual({
        value: {
          torrentRoots: ['/data/torrents'],
          kinds: ['symlink', 'hardlink'],
          relativeSymlinks: true,
          minFileAge: '10m',
          recheckAfter: false,
          verify: 'hash',
        },
      })
    })

    it('ConfigView édite les correspondances de chemins en tableau', async () => {
      const mappings = [{ from: '/home/u/media', to: '/data/media' }]

      const written = await saveSetting(
        settingsWith('pathMappings', { value: mappings, origin: 'override', set: true }),
        'pathMappings',
        async () => {
          await clickInBody('config-field-pathMappings.add')
          await setField('config-field-pathMappings.1.from', '/home/u/tv')
          await setField('config-field-pathMappings.1.to', '/data/tv')
        },
      )

      expect(written?.body).toEqual({
        value: [
          { from: '/home/u/media', to: '/data/media' },
          { from: '/home/u/tv', to: '/data/tv' },
        ],
      })
    })

    it('ConfigView affiche les champs du type de fournisseur choisi', async () => {
      const { wrapper } = await mountView({
        'GET /api/config': configSnapshot({ lists: { fsProviders: [] } }),
      })
      await wrapper.get('[data-testid="add-item-fsProviders"]').trigger('click')
      await flushPromises()

      await setField('config-field-type', 'rclone')
      expect(bodyElement('config-field-rc')).not.toBeNull()
      expect(bodyElement('config-field-address')).toBeNull()

      await setField('config-field-type', 'sftp')
      expect(bodyElement('config-field-address')).not.toBeNull()
      expect(bodyElement('config-field-rc')).toBeNull()
    })

    it("ConfigView déduit l'identifiant de l'URL saisie tant qu'il n'est pas modifié", async () => {
      const { wrapper } = await mountView({
        'GET /api/config': configSnapshot({ lists: { radarr: [] } }),
      })
      await wrapper.get('[data-testid="add-item-radarr"]').trigger('click')
      await flushPromises()

      await setField(
        'config-field-url',
        'HTTP://alice:dummy@Radarr.example.com:80/api?apikey=dummy',
      )
      expect(bodyElement<HTMLInputElement>('config-item-id')?.value).toBe(
        'http://Radarr.example.com:80',
      )

      await setField('config-item-id', 'http://radarr.example.com:7878#2')
      await setField('config-field-url', 'http://other.example.com:7878')
      expect(bodyElement<HTMLInputElement>('config-item-id')?.value).toBe(
        'http://radarr.example.com:7878#2',
      )
    })

    it('ConfigView ajoute un tracker avec ses annonces en tableau et sans champ vide', async () => {
      const { wrapper, calls } = await mountView({
        'GET /api/config': configSnapshot({ lists: { trackers: [] } }),
        'PUT /api/config/lists/{list}/{itemId}': configSnapshot(),
      })
      await wrapper.get('[data-testid="add-item-trackers"]').trigger('click')
      await flushPromises()

      await setField('config-item-id', 'tracker-a')
      await setField('config-field-key', 'tracker-a')
      await setField(
        'config-field-announce',
        'https://a.example.com/announce\nhttps://b.example.com/announce',
      )
      await clickInBody('config-form-submit')

      expect(writeCalls(calls)[0]?.body).toEqual({
        element: {
          key: 'tracker-a',
          announce: ['https://a.example.com/announce', 'https://b.example.com/announce'],
        },
      })
    })

    it("ConfigView n'affiche aucun JSON brut pour ajouter à une liste vide", async () => {
      const { wrapper } = await mountView({
        'GET /api/config': configSnapshot({ lists: { torznab: [] } }),
      })

      await wrapper.get('[data-testid="add-item-torznab"]').trigger('click')
      await flushPromises()

      expect(bodyElement('config-field-url')).not.toBeNull()
      expect(bodyElement('config-field-name')).not.toBeNull()
      expect(bodyElement('config-field-maxQueriesPerDay')).not.toBeNull()
      expect(document.body.querySelector('textarea')).toBeNull()
      expect(document.body.textContent).not.toContain('objet JSON')
    })
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
