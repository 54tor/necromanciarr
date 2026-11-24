import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { failure, installFakeApi, resetAppState } from '@/test/fakeApi'
import { job } from '@/test/fixtures'
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
})
