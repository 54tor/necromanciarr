import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { failure, installFakeApi, resetAppState } from '@/test/fakeApi'
import { job } from '@/test/fixtures'
import { stubViewport } from '@/test/viewport'
import StagesView from './StagesView.vue'

const toastError = vi.fn()
vi.mock('vue-sonner', () => ({
  toast: { error: (message: string) => toastError(message), success: vi.fn() },
}))

const verifyResult = {
  stage: 'verify',
  processed: 4,
  skipped: 1,
  failed: 0,
  verified: 3,
  partial_annex: 1,
  partial_pack: 0,
  conflicts: 2,
  proposals_created: 6,
  unidentified_files: 0,
  candidates_created: 0,
  candidates_updated: 0,
  candidates_unchanged: 0,
  candidates_stale: 0,
  resumed: 0,
  linked: 0,
  seeded: 0,
  seeded_paused_incomplete: 0,
}

describe('StagesView', () => {
  beforeEach(() => {
    resetAppState()
    stubViewport(1280)
    vi.useFakeTimers()
    toastError.mockClear()
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('StagesView_pollsJobsAndStopsOnUnmount', async () => {
    const calls = installFakeApi({ 'GET /api/jobs': () => [job({ id: 1 })] })
    const wrapper = mount(StagesView)
    await flushPromises()
    const countJobCalls = () => calls.filter((call) => call.path === '/api/jobs').length
    const afterMount = countJobCalls()

    await vi.advanceTimersByTimeAsync(2000)
    expect(countJobCalls()).toBe(afterMount + 1)
    await vi.advanceTimersByTimeAsync(4000)
    expect(countJobCalls()).toBe(afterMount + 3)

    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(10_000)
    expect(countJobCalls()).toBe(afterMount + 3)
  })

  it('StagesView_doesNotPollWhenNoJobRuns', async () => {
    const calls = installFakeApi({
      'GET /api/jobs': () => [job({ id: 1, state: 'done', finished_at: '2026-01-01T10:01:00Z' })],
    })
    mount(StagesView)
    await flushPromises()
    const afterMount = calls.length

    await vi.advanceTimersByTimeAsync(10_000)

    expect(calls.length).toBe(afterMount)
  })

  it('StagesView_disablesRunButtonsWhileAJobRuns', async () => {
    installFakeApi({ 'GET /api/jobs': () => [job()] })
    const wrapper = mount(StagesView)
    await flushPromises()

    const runButtons = wrapper.findAll('button[data-action="run"]')

    expect(runButtons).toHaveLength(5)
    for (const runButton of runButtons) expect(runButton.attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-testid="job-state"]').text()).toContain('Une étape est en cours')
    expect(wrapper.find('[data-testid="job-state"]').attributes('aria-live')).toBe('polite')
  })

  it('StagesView_runsAStageThenShowsItsCountersWhenTheJobEnds', async () => {
    let finished = false
    const calls = installFakeApi({
      'GET /api/jobs': () =>
        finished ? [job({ id: 4, state: 'done', result: verifyResult })] : [],
      'POST /api/stages/{stage}/run': () => job({ id: 4 }),
    })
    const wrapper = mount(StagesView)
    await flushPromises()

    await wrapper.find('button[data-stage="verify"]').trigger('click')
    await flushPromises()
    expect(calls.at(-1)).toMatchObject({ method: 'POST', path: '/api/stages/verify/run', body: {} })
    expect(wrapper.find('button[data-stage="scan"]').attributes('disabled')).toBeDefined()

    finished = true
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()

    expect(wrapper.find('button[data-stage="scan"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.text()).toContain('conflits')
    expect(wrapper.text()).toContain('propositions créées')
  })

  it('StagesView_explainsA409StageRunning', async () => {
    installFakeApi({
      'GET /api/jobs': [],
      'POST /api/stages/{stage}/run': () => failure(409, 'stage_running'),
    })
    const wrapper = mount(StagesView)
    await flushPromises()

    await wrapper.find('button[data-stage="scan"]').trigger('click')
    await flushPromises()

    expect(toastError).toHaveBeenCalledWith('Une étape est déjà en cours.')
  })

  it('StagesView_disablesRunButtonsDuringTheInFlightPost', async () => {
    let release: () => void = () => undefined
    const gate = new Promise<void>((resolve) => (release = resolve))
    const calls = installFakeApi({ 'GET /api/jobs': [] })
    const baseFetch = globalThis.fetch
    vi.stubGlobal('fetch', async (input: string, init?: RequestInit) => {
      if (init?.method === 'POST') await gate
      return baseFetch(input, init)
    })
    const wrapper = mount(StagesView)
    await flushPromises()

    await wrapper.find('button[data-stage="scan"]').trigger('click')
    await flushPromises()

    for (const runButton of wrapper.findAll('button[data-action="run"]')) {
      expect(runButton.attributes('disabled')).toBeDefined()
    }
    release()
    await flushPromises()
    expect(calls.length).toBeGreaterThan(0)
  })

  it('StagesView_showsAnUnreachableStateAfterThreeFailedPolls', async () => {
    let answered = 0
    installFakeApi({
      'GET /api/jobs': () => {
        answered += 1
        return answered === 1 ? [job()] : failure(500, 'failure')
      },
    })
    const wrapper = mount(StagesView)
    await flushPromises()
    expect(wrapper.find('[data-testid="unreachable"]').exists()).toBe(false)

    await vi.advanceTimersByTimeAsync(6000)
    await flushPromises()

    expect(wrapper.find('[data-testid="unreachable"]').text()).toContain('Serveur injoignable')
    expect(wrapper.find('[data-testid="job-state"]').text()).not.toContain('en cours')
  })

  it('StagesView_sortsJobsByStartDescendingByDefault', async () => {
    installFakeApi({
      'GET /api/jobs': () => [
        job({ id: 1, state: 'done', started_at: '2026-01-01T10:00:00Z' }),
        job({ id: 3, state: 'done', started_at: '2026-01-03T10:00:00Z' }),
        job({ id: 2, state: 'done', started_at: '2026-01-02T10:00:00Z' }),
      ],
    })
    const wrapper = mount(StagesView)
    await flushPromises()

    const gotOrder = wrapper.findAll('tbody tr').map((row) => row.attributes('data-row-key'))

    expect(gotOrder).toEqual(['3', '2', '1'])
    expect(wrapper.find('th[aria-sort="descending"]').text()).toContain('Début')
  })

  it('StagesView_showsTranslatedReasonLabelsInTheSummary', async () => {
    installFakeApi({
      'GET /api/jobs': () => [
        job({
          id: 5,
          state: 'done',
          result: {
            ...verifyResult,
            reasons: { no_candidate: 2, odd_reason: 1 },
            reason_labels: { no_candidate: 'aucun candidat trouvé' },
          },
        }),
      ],
    })
    const wrapper = mount(StagesView)
    await flushPromises()

    const gotSummary = wrapper.get('[data-row-key="5"]').text()

    expect(gotSummary).toContain('aucun candidat trouvé : 2')
    expect(gotSummary).toContain('odd_reason : 1')
    expect(gotSummary).not.toContain('no_candidate')
  })

  it('StagesView_opensTheFullResultFromTheRow', async () => {
    installFakeApi({
      'GET /api/jobs': () => [job({ id: 5, state: 'done', result: verifyResult })],
    })
    const wrapper = mount(StagesView, { attachTo: document.body })
    await flushPromises()

    await wrapper.get('[data-row-key="5"] button[data-testid="job-result"]').trigger('click')
    await flushPromises()

    const gotDialog = document.body.querySelector('[role="dialog"]')?.textContent ?? ''
    expect(gotDialog).toContain('Vérification')
    expect(gotDialog).toMatch(/complets : 3/)
    expect(gotDialog).toMatch(/packs partiels : 0/)
    wrapper.unmount()
    document.body.innerHTML = ''
  })

  it('StagesView_putsTheStageButtonsInTheToolbar', async () => {
    installFakeApi({ 'GET /api/jobs': [] })
    const wrapper = mount(StagesView)
    await flushPromises()

    const gotButtons = wrapper.findAll('[role="group"] button[data-action="run"]')

    expect(gotButtons.map((button) => button.attributes('data-stage'))).toEqual([
      'scan',
      'match',
      'verify',
      'link',
      'seed',
    ])
    expect(gotButtons.every((button) => button.attributes('data-scope') === 'toolbar')).toBe(true)
    expect(wrapper.find('h2').exists()).toBe(false)
  })

  it('StagesView_summarisesFailedAndRunningJobsAndShowsWarningsInTheResult', async () => {
    installFakeApi({
      'GET /api/jobs': () => [
        job({ id: 1, state: 'failed', error: { code: 'stage_running', message: 'raw' } }),
        job({ id: 2, state: 'running' }),
        job({
          id: 3,
          state: 'done',
          result: {
            ...verifyResult,
            verified: 0,
            warnings: [{ code: 'w', text: 'avis du serveur' }],
          },
        }),
      ],
    })
    const wrapper = mount(StagesView, { attachTo: document.body })
    await flushPromises()

    expect(wrapper.get('[data-row-key="1"]').text()).toContain('Une étape est déjà en cours.')
    expect(wrapper.get('[data-row-key="2"]').text()).toContain('—')
    await wrapper.get('[data-row-key="3"] button[data-testid="job-result"]').trigger('click')
    await flushPromises()
    expect(document.body.textContent).toContain('avis du serveur')
    document.body.querySelector<HTMLButtonElement>('[role="dialog"] button.border')?.click()
    await flushPromises()
    await wrapper.get('[data-row-key="2"] button[data-testid="job-result"]').trigger('click')
    await flushPromises()
    expect(document.body.textContent).toContain('Ce job n’a pas de résultat d’étape.')
    wrapper.unmount()
    document.body.innerHTML = ''
  })
})
