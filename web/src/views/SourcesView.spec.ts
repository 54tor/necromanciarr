import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { SourceRow } from '@/api/types'
import { failure, installFakeApi, resetAppState } from '@/test/fakeApi'
import { stubViewport } from '@/test/viewport'
import SourcesView from './SourcesView.vue'

const toastError = vi.fn()
vi.mock('vue-sonner', () => ({
  toast: { error: (message: string) => toastError(message), success: vi.fn() },
}))

// Faux secret à faible entropie : il ne doit jamais apparaître, même si le serveur le renvoyait par erreur.
const FAKE_KEY = 'fake-key-0000'

function sourceRows(lastError = ''): SourceRow[] {
  return [
    {
      order: 1,
      source: 'provider',
      type: 'provider',
      tracker: '',
      state: 'ok',
      queries: null,
      downloads: null,
      last_error: '',
      caps_cached: false,
    },
    {
      order: 2,
      source: 'torznab:b',
      type: 'torznab',
      tracker: 'tracker-b',
      state: 'exhausted',
      queries: { used: 200, cap: 200 },
      downloads: { used: 0, cap: 50 },
      last_error: '',
      caps_cached: false,
    },
    {
      order: 3,
      source: 'torznab:a',
      type: 'torznab',
      tracker: 'tracker-a',
      state: 'ok',
      queries: { used: 3, cap: 200 },
      downloads: { used: 1, cap: 50 },
      last_error: lastError,
      caps_cached: true,
    },
  ]
}

const cellTexts = (wrapper: VueWrapper, column: number) =>
  wrapper.findAll('tbody tr').map((row) => row.findAll('td')[column]?.text() ?? '')

describe('SourcesView', () => {
  beforeEach(() => {
    resetAppState()
    stubViewport(1280)
    toastError.mockClear()
  })
  afterEach(() => vi.unstubAllGlobals())

  it('lists sources in chain order', async () => {
    installFakeApi({ 'GET /api/sources': sourceRows() })
    const wrapper = mount(SourcesView, { attachTo: document.body })
    await flushPromises()

    expect(wrapper.findAll('tbody tr').map((row) => row.attributes('data-row-key'))).toEqual([
      'provider',
      'torznab:b',
      'torznab:a',
    ])
    expect(cellTexts(wrapper, 2)).toEqual(['Fournisseur', 'Torznab', 'Torznab'])
    expect(cellTexts(wrapper, 4)).toEqual([
      'Opérationnelle',
      'Quota du jour atteint',
      'Opérationnelle',
    ])
    expect(cellTexts(wrapper, 5)).toEqual(['s.o.', '200 / 200', '3 / 200'])
    expect(cellTexts(wrapper, 6)).toEqual(['s.o.', '0 / 50', '1 / 50'])
    expect(cellTexts(wrapper, 7)).toEqual(['s.o.', 'Non', 'Oui'])
    expect(wrapper.findAll('th').map((header) => header.text())).toContain('Cache des capacités')
    expect(wrapper.findAll('th button').length).toBe(0)
    expect(wrapper.find('[data-testid="test-provider"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('tests one source from its row', async () => {
    let listed = sourceRows()
    const calls = installFakeApi({
      'GET /api/sources': () => listed,
      'POST /api/sources/{name}/test': {
        source: 'torznab:a',
        result: 'failed',
        error: 'indexer_access_denied',
        message: `apikey=${FAKE_KEY}`,
      },
    })
    // La réponse du test reste en attente jusqu'à releaseTest : les boutons doivent être désactivés entre-temps.
    const answeringFetch = globalThis.fetch
    let releaseTest: () => void = () => {}
    vi.stubGlobal('fetch', (input: string, init: RequestInit = {}) =>
      init.method === 'POST'
        ? new Promise<Response>((resolve) => {
            releaseTest = () => resolve(answeringFetch(input, init))
          })
        : answeringFetch(input, init),
    )
    const wrapper = mount(SourcesView, { attachTo: document.body })
    await flushPromises()

    await wrapper.get('[data-testid="test-torznab:a"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="test-torznab:a"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="test-torznab:b"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="test-result"]').text()).toBe('Test de torznab:a en cours…')

    listed = sourceRows('indexer_access_denied')
    releaseTest()
    await flushPromises()

    const testCalls = calls.filter((call) => call.method === 'POST')
    expect(testCalls.map((call) => call.path)).toEqual(['/api/sources/torznab%3Aa/test'])
    expect(wrapper.get('[data-testid="test-result"]').text()).toBe(
      "Test en échec : torznab:a, L'indexeur refuse l'accès (clé d'API)",
    )
    expect(cellTexts(wrapper, 8)[2]).toBe("L'indexeur refuse l'accès (clé d'API)")
    expect(wrapper.text()).not.toContain('indexer_access_denied')
    expect(wrapper.html()).not.toContain(FAKE_KEY)
    expect(wrapper.get('[data-testid="test-torznab:a"]').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('tests the source chosen in the toolbar and shows API errors translated', async () => {
    const calls = installFakeApi({
      'GET /api/sources': sourceRows(),
      'POST /api/sources/{name}/test': () => failure(404, 'not_found', `raw text ${FAKE_KEY}`),
    })
    const wrapper = mount(SourcesView, { attachTo: document.body })
    await flushPromises()
    expect(wrapper.get('[data-testid="test-chosen"]').attributes('disabled')).toBeDefined()

    await wrapper.get('[data-testid="test-choice"]').setValue('torznab:b')
    await wrapper.get('[data-testid="test-chosen"]').trigger('click')
    await flushPromises()

    expect(calls.filter((call) => call.method === 'POST').map((call) => call.path)).toEqual([
      '/api/sources/torznab%3Ab/test',
    ])
    expect(toastError).toHaveBeenCalledTimes(1)
    expect(String(toastError.mock.calls[0]?.[0])).not.toContain(FAKE_KEY)
    wrapper.unmount()
  })

  it('clears the previous result before a new test', async () => {
    let answer: unknown = { source: 'torznab:a', result: 'ok' }
    installFakeApi({
      'GET /api/sources': sourceRows(),
      'POST /api/sources/{name}/test': () => answer,
    })
    const wrapper = mount(SourcesView, { attachTo: document.body })
    await flushPromises()
    await wrapper.get('[data-testid="test-torznab:a"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="test-result"]').text()).toBe('Test réussi : torznab:a')

    answer = failure(409, 'refused')
    await wrapper.get('[data-testid="test-torznab:a"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-testid="test-result"]').text()).toBe('')
    expect(toastError).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('shows the load error and the empty state', async () => {
    installFakeApi({ 'GET /api/sources': () => failure(500, 'failure') })
    const failed = mount(SourcesView, { attachTo: document.body })
    await flushPromises()
    expect(failed.text()).toContain('Impossible de charger les sources.')
    failed.unmount()

    installFakeApi({ 'GET /api/sources': [] })
    const empty = mount(SourcesView, { attachTo: document.body })
    await flushPromises()
    expect(empty.text()).toContain('Aucune source configurée.')
    empty.unmount()
  })
})
