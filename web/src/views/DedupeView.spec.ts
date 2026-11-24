import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { installFakeApi, resetAppState } from '@/test/fakeApi'
import { dedupeDetails, job, maintenanceProposal } from '@/test/fixtures'
import DedupeView from './DedupeView.vue'

vi.mock('vue-sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

const rows = [
  maintenanceProposal(21, dedupeDetails),
  maintenanceProposal(22, {
    ...dedupeDetails,
    actionable: false,
    ambiguous: ['/data/library/A.mkv', '/data/library/B.mkv'],
    reason: 'Plusieurs fichiers possibles',
  }),
  maintenanceProposal(23, dedupeDetails, 'accepted'),
]

function mountView() {
  const calls = installFakeApi({
    'GET /api/proposals': rows,
    'GET /api/jobs': () => [],
    'POST /api/dedupe/plan': () => job({ id: 9, action: 'dedupe plan' }),
    'POST /api/dedupe/{id}/apply': () => job({ id: 10, action: 'dedupe apply' }),
  })
  return { wrapper: mount(DedupeView), calls }
}

describe('DedupeView', () => {
  beforeEach(() => {
    resetAppState()
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('DedupeView_listsNonActionableRowsWithDisabledButtons', async () => {
    const { wrapper } = mountView()
    await flushPromises()

    const actionableRow = wrapper.find('[data-testid="dedupe-21"]')
    const blockedRow = wrapper.find('[data-testid="dedupe-22"]')

    expect(actionableRow.text()).toContain('2,8 Gio')
    expect(actionableRow.text()).toContain('lien physique')
    expect(actionableRow.find('button').attributes('disabled')).toBeUndefined()
    expect(blockedRow.find('[data-testid="ambiguous"]').text()).toContain('2 fichiers')
    expect(blockedRow.text()).toContain('/data/library/B.mkv')
    expect(blockedRow.text()).toContain('Plusieurs fichiers possibles')
    expect(blockedRow.find('button').attributes('disabled')).toBeDefined()
  })

  it('DedupeView_dryRunDoesNotCallApplyWithoutFlag', async () => {
    const { wrapper, calls } = mountView()
    await flushPromises()
    const applyBody = () =>
      calls.filter((call) => call.path === '/api/dedupe/21/apply').at(-1)?.body

    await wrapper.find('[data-testid="dedupe-21"] button').trigger('click')
    await flushPromises()
    expect(applyBody()).toEqual({ dry_run: false, accept_pending: true })

    await vi.advanceTimersByTimeAsync(2000)
    await wrapper.find('[data-testid="dry-run"]').setValue(true)
    await wrapper.find('[data-testid="dedupe-21"] button').trigger('click')
    await flushPromises()

    expect(applyBody()).toEqual({ dry_run: true, accept_pending: true })
  })

  it('DedupeView_runsThePlanAsAJob', async () => {
    const { wrapper, calls } = mountView()
    await flushPromises()

    await wrapper.find('button[data-action="dedupe plan"]').trigger('click')
    await flushPromises()

    expect(calls.at(-1)).toMatchObject({ method: 'POST', path: '/api/dedupe/plan', body: {} })
  })

  it('DedupeView_appliesAcceptedRowsOnlyAndKeepsAcceptPendingOff', async () => {
    let finished = false
    const calls = installFakeApi({
      'GET /api/proposals': rows,
      'GET /api/jobs': () =>
        finished
          ? [job({ id: 10, action: 'dedupe apply', state: 'done' })]
          : [job({ id: 10, action: 'dedupe apply' })],
      'POST /api/dedupe/{id}/apply': () => job({ id: 10, action: 'dedupe apply' }),
    })
    const wrapper = mount(DedupeView)
    await flushPromises()

    await wrapper.find('[data-testid="apply-all"]').trigger('click')
    await flushPromises()
    finished = true
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()

    const applyCalls = calls.filter((call) => call.method === 'POST')
    expect(applyCalls.map((call) => [call.path, call.body])).toEqual([
      ['/api/dedupe/23/apply', { dry_run: false, accept_pending: false }],
    ])
  })
})
