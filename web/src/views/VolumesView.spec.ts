import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { VolumeRow } from '@/api/types'
import { failure, installFakeApi, resetAppState } from '@/test/fakeApi'
import { stubViewport } from '@/test/viewport'
import VolumesView from './VolumesView.vue'

const toastError = vi.fn()
vi.mock('vue-sonner', () => ({
  toast: { error: (message: string) => toastError(message), success: vi.fn() },
}))

// Texte brut qu'un serveur pourrait glisser dans une réponse : il ne doit jamais être rendu.
const RAW_TEXT = 'dial tcp alice@seedbox.example.com: raw'

function volumeRows(): VolumeRow[] {
  return [
    { volume: '', type: 'local', address: '', state: 'ok', capabilities: null, probed_at: null },
    {
      volume: 'seedbox',
      type: 'sftp',
      address: 'seedbox.example.com:22',
      state: 'broken',
      capabilities: {
        symlink: true,
        hardlink: true,
        atomic_replace: false,
        server_copy: false,
        identity: true,
        hashes: ['sha1'],
        reasons: { serverCopy: 'extension_missing' },
      },
      probed_at: '2026-11-20T08:30:00Z',
    },
    {
      volume: 'other',
      type: 'sftp',
      address: 'other.example.com:2222',
      state: 'unknown',
      capabilities: null,
      probed_at: null,
    },
  ]
}

const cellTexts = (wrapper: VueWrapper, column: number) =>
  wrapper.findAll('tbody tr').map((row) => row.findAll('td')[column]?.text() ?? '')

function rowOf(wrapper: VueWrapper, key: string) {
  return wrapper.get(`tbody tr[data-row-key="sftp:${key}"]`)
}

describe('VolumesView', () => {
  beforeEach(() => {
    resetAppState()
    stubViewport(1280)
    toastError.mockClear()
  })
  afterEach(() => vi.unstubAllGlobals())

  it('shows capability pills with text', async () => {
    installFakeApi({ 'GET /api/volumes': volumeRows() })
    const wrapper = mount(VolumesView, { attachTo: document.body })
    await flushPromises()

    expect(
      wrapper
        .findAll('th')
        .map((header) => header.text())
        .slice(0, 6),
    ).toEqual(['Volume', 'Type', 'Adresse', 'État', 'Capacités', 'Sondé le'])
    expect(cellTexts(wrapper, 0)).toEqual(['local', 'seedbox', 'other'])
    expect(cellTexts(wrapper, 3)).toEqual(['Opérationnel', 'Coupé', 'Inconnu'])
    const pills = rowOf(wrapper, 'seedbox')
      .findAll('[data-capability]')
      .map((pill) => [pill.attributes('data-capability'), pill.text()])
    expect(pills).toEqual([
      ['symlink', 'Lien symbolique : oui'],
      ['hardlink', 'Lien physique : oui'],
      ['atomic_replace', 'Remplacement atomique : non'],
      ['server_copy', 'Copie côté serveur : non'],
      ['identity', 'Identité distante : oui'],
    ])
    expect(rowOf(wrapper, 'other').text()).toContain('Non sondé')
    expect(rowOf(wrapper, 'seedbox').findAll('td')[5]?.text()).toContain('2026')
    expect(wrapper.find('[data-testid="probe-"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('probes one volume from its row', async () => {
    let listed = volumeRows()
    const calls = installFakeApi({
      'GET /api/volumes': () => listed,
      'POST /api/volumes/{name}/probe': {
        volume: 'seedbox',
        result: 'failed',
        error: 'host_key_unknown',
        message: RAW_TEXT,
      },
    })
    const answeringFetch = globalThis.fetch
    let releaseProbe: () => void = () => {}
    vi.stubGlobal('fetch', (input: string, init: RequestInit = {}) =>
      init.method === 'POST'
        ? new Promise<Response>((resolve) => {
            releaseProbe = () => resolve(answeringFetch(input, init))
          })
        : answeringFetch(input, init),
    )
    const wrapper = mount(VolumesView, { attachTo: document.body })
    await flushPromises()

    await wrapper.get('[data-testid="probe-seedbox"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="probe-seedbox"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="probe-other"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="probe-result"]').text()).toBe('Sonde de seedbox en cours…')

    listed = volumeRows().map((row) => (row.volume === 'seedbox' ? { ...row, state: 'ok' } : row))
    releaseProbe()
    await flushPromises()

    expect(calls.filter((call) => call.method === 'POST').map((call) => call.path)).toEqual([
      '/api/volumes/seedbox/probe',
    ])
    expect(wrapper.get('[data-testid="probe-result"]').text()).toBe(
      "Sonde en échec : seedbox, l'hôte n'a aucune entrée dans knownHosts",
    )
    expect(wrapper.text()).not.toContain('host_key_unknown')
    expect(wrapper.html()).not.toContain(RAW_TEXT)
    expect(rowOf(wrapper, 'seedbox').text()).toContain('Opérationnel')
    expect(wrapper.get('[data-testid="probe-seedbox"]').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('probes the volume chosen in the toolbar and shows API errors translated', async () => {
    const calls = installFakeApi({
      'GET /api/volumes': volumeRows(),
      'POST /api/volumes/{name}/probe': () => failure(409, 'stage_running', RAW_TEXT),
    })
    const wrapper = mount(VolumesView, { attachTo: document.body })
    await flushPromises()
    expect(wrapper.get('[data-testid="probe-chosen"]').attributes('disabled')).toBeDefined()

    await wrapper.get('[data-testid="probe-choice"]').setValue('other')
    await wrapper.get('[data-testid="probe-chosen"]').trigger('click')
    await flushPromises()

    expect(calls.filter((call) => call.method === 'POST').map((call) => call.path)).toEqual([
      '/api/volumes/other/probe',
    ])
    expect(toastError).toHaveBeenCalledTimes(1)
    expect(String(toastError.mock.calls[0]?.[0])).not.toContain(RAW_TEXT)
    wrapper.unmount()
  })

  it('shows the allowed remote commands of a volume', async () => {
    const lines = ['stat --version', 'stat -L -c %d\t%i -- {paths}']
    installFakeApi({
      'GET /api/volumes': volumeRows(),
      'GET /api/volumes/{name}/commands': {
        volume: 'seedbox',
        lines,
        guard_script: 'docs/reference/remote-command-guard.sh',
      },
    })
    const wrapper = mount(VolumesView, { attachTo: document.body })
    await flushPromises()

    await wrapper.get('[data-testid="commands-seedbox"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-testid="commands-lines"]').element.textContent).toBe(lines.join('\n'))
    expect(wrapper.get('[data-testid="commands"]').text()).toContain(
      'docs/reference/remote-command-guard.sh',
    )
    wrapper.unmount()
  })

  it('shows the load error and the empty state', async () => {
    installFakeApi({ 'GET /api/volumes': () => failure(500, 'failure') })
    const failed = mount(VolumesView, { attachTo: document.body })
    await flushPromises()
    expect(failed.text()).toContain('Impossible de charger les volumes.')
    failed.unmount()

    installFakeApi({ 'GET /api/volumes': [] })
    const empty = mount(VolumesView, { attachTo: document.body })
    await flushPromises()
    expect(empty.text()).toContain('Aucun volume déclaré.')
    empty.unmount()
  })
})
