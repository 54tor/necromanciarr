import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { installFakeApi, resetAppState, type FakeCall } from '@/test/fakeApi'
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

  it('DedupeView_showsTheLabelOfTheReasonInsteadOfItsCode', async () => {
    installFakeApi({
      'GET /api/proposals': [
        maintenanceProposal(24, {
          ...dedupeDetails,
          actionable: false,
          reason: 'hash_unavailable',
          label: 'Empreinte indisponible',
        }),
      ],
      'GET /api/jobs': () => [],
    })
    const wrapper = mount(DedupeView)
    await flushPromises()

    const gotText = wrapper.find('[data-testid="dedupe-24"]').text()

    expect(gotText).toContain('Empreinte indisponible')
    expect(gotText).not.toContain('hash_unavailable')
  })

  it('DedupeView_dryRunDoesNotCallApplyWithoutFlag', async () => {
    const { wrapper, calls } = mountView()
    await flushPromises()
    const applyBody = () =>
      calls.filter((call) => call.path === '/api/dedupe/21/apply').at(-1)?.body
    expect((wrapper.find('[data-testid="dry-run"]').element as HTMLInputElement).checked).toBe(true)

    await wrapper.find('[data-testid="dedupe-21"] button').trigger('click')
    await flushPromises()
    expect(applyBody()).toEqual({ dry_run: true, accept_pending: true })

    await vi.advanceTimersByTimeAsync(2000)
    await wrapper.find('[data-testid="dry-run"]').setValue(false)
    await wrapper.find('[data-testid="dedupe-21"] button').trigger('click')
    await flushPromises()

    expect(applyBody()).toEqual({ dry_run: false, accept_pending: true })
    expect(wrapper.text()).toContain('Simulation (aucun fichier modifié)')
  })

  it('DedupeView_runsThePlanAsAJob', async () => {
    const { wrapper, calls } = mountView()
    await flushPromises()

    await wrapper.find('button[data-action="dedupe plan"]').trigger('click')
    await flushPromises()

    expect(calls.at(-1)).toMatchObject({ method: 'POST', path: '/api/dedupe/plan', body: {} })
  })

  const twoAccepted = [
    maintenanceProposal(23, dedupeDetails, 'accepted'),
    maintenanceProposal(24, dedupeDetails, 'accepted'),
  ]

  function mountChain(jobsState: { finished: boolean }) {
    const calls = installFakeApi({
      'GET /api/proposals': twoAccepted,
      'GET /api/jobs': () => [
        job({ id: 10, action: 'dedupe apply', state: jobsState.finished ? 'done' : 'running' }),
      ],
      'POST /api/dedupe/{id}/apply': () => job({ id: 10, action: 'dedupe apply' }),
    })
    return { wrapper: mount(DedupeView, { attachTo: document.body }), calls }
  }

  const confirmButton = () =>
    document.body.querySelector<HTMLButtonElement>('[data-testid="confirm-dialog-confirm"]')
  const posts = (calls: FakeCall[]) => calls.filter((call) => call.method === 'POST')

  it('DedupeView_bulkApplyInDryRunAsksNothingAndKeepsAcceptPendingOff', async () => {
    const state = { finished: false }
    const { wrapper, calls } = mountChain(state)
    await flushPromises()

    await wrapper.find('[data-testid="apply-all"]').trigger('click')
    await flushPromises()

    expect(confirmButton()).toBeNull()
    expect(posts(calls).map((call) => [call.path, call.body])).toEqual([
      ['/api/dedupe/23/apply', { dry_run: true, accept_pending: false }],
    ])
    wrapper.unmount()
  })

  it('DedupeView_bulkApplyAsksConfirmationWithTheCountBeforeRealApplication', async () => {
    const state = { finished: false }
    const { wrapper, calls } = mountChain(state)
    await flushPromises()
    await wrapper.find('[data-testid="dry-run"]').setValue(false)

    await wrapper.find('[data-testid="apply-all"]').trigger('click')
    await flushPromises()

    expect(document.body.textContent).toContain('2 remplacement(s)')
    expect(document.body.textContent).toContain('écrit réellement sur le disque')
    expect(posts(calls)).toHaveLength(0)
    confirmButton()?.click()
    await flushPromises()
    expect(posts(calls).map((call) => call.body)).toEqual([
      { dry_run: false, accept_pending: false },
    ])
    wrapper.unmount()
  })

  it('DedupeView_bulkApplyCapturesTheDryRunFlagOnceAtTheStartOfTheChain', async () => {
    const state = { finished: false }
    const { wrapper, calls } = mountChain(state)
    await flushPromises()

    await wrapper.find('[data-testid="apply-all"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-testid="dry-run"]').setValue(false)
    state.finished = true
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()

    expect(posts(calls).map((call) => call.body)).toEqual([
      { dry_run: true, accept_pending: false },
      { dry_run: true, accept_pending: false },
    ])
    wrapper.unmount()
  })

  it('DedupeView_bulkChainStopsWhenTheViewIsUnmounted', async () => {
    const state = { finished: false }
    const { wrapper, calls } = mountChain(state)
    await flushPromises()

    await wrapper.find('[data-testid="apply-all"]').trigger('click')
    await flushPromises()
    wrapper.unmount()
    state.finished = true
    await vi.advanceTimersByTimeAsync(10_000)
    await flushPromises()

    expect(posts(calls)).toHaveLength(1)
  })
})
