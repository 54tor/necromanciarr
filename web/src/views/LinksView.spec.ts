import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { installFakeApi, resetAppState } from '@/test/fakeApi'
import { job, maintenanceProposal, repairDetails } from '@/test/fixtures'
import LinksView from './LinksView.vue'

vi.mock('vue-sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

const rows = [
  { ...maintenanceProposal(31, repairDetails), kind: 'repair' as const },
  {
    ...maintenanceProposal(32, {
      ...repairDetails,
      status: 'ambiguous',
      method: undefined,
      new_target: undefined,
      actionable: false,
      candidates: ['/data/a/Film.mkv', '/data/b/Film.mkv'],
    }),
    kind: 'repair' as const,
  },
  {
    ...maintenanceProposal(33, {
      ...repairDetails,
      status: 'lost',
      method: undefined,
      new_target: undefined,
      actionable: false,
    }),
    kind: 'repair' as const,
  },
]

describe('LinksView', () => {
  beforeEach(resetAppState)
  afterEach(() => vi.unstubAllGlobals())

  it('LinksView_showsRepairMethodAndAmbiguity', async () => {
    installFakeApi({ 'GET /api/proposals': rows, 'GET /api/jobs': [] })
    const wrapper = mount(LinksView)
    await flushPromises()

    const repairable = wrapper.find('[data-testid="repair-31"]')
    const ambiguous = wrapper.find('[data-testid="repair-32"]')
    const lost = wrapper.find('[data-testid="repair-33"]')

    expect(repairable.find('[data-testid="method"]').text()).toContain('empreinte du contenu')
    expect(repairable.text()).toContain('/data/new/Film.mkv')
    expect(repairable.find('button').attributes('disabled')).toBeUndefined()
    expect(ambiguous.find('[data-testid="ambiguous"]').exists()).toBe(true)
    expect(ambiguous.text()).toContain('/data/b/Film.mkv')
    expect(ambiguous.find('button').attributes('disabled')).toBeDefined()
    expect(lost.find('[data-testid="status"]').text()).toBe('Introuvable')
    expect(lost.find('button').attributes('disabled')).toBeDefined()
  })

  it('LinksView_scansWithTheOptionalDirectoryAndAppliesARepair', async () => {
    const calls = installFakeApi({
      'GET /api/proposals': rows,
      'GET /api/jobs': [],
      'POST /api/repair/scan': () => job({ id: 2, action: 'repair scan' }),
      'POST /api/repair/{id}/apply': () => job({ id: 3, action: 'repair apply' }),
    })
    const wrapper = mount(LinksView)
    await flushPromises()

    await wrapper.find('[data-testid="scan-directory"]').setValue(' /data/lost ')
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(
      calls.filter((call) => call.method === 'POST').map((call) => [call.path, call.body]),
    ).toEqual([['/api/repair/scan', { directory: '/data/lost' }]])
  })

  it('LinksView_appliesARepairWithTheDryRunFlag', async () => {
    const calls = installFakeApi({
      'GET /api/proposals': rows,
      'GET /api/jobs': [],
      'POST /api/repair/{id}/apply': () => job({ id: 3, action: 'repair apply' }),
    })
    const wrapper = mount(LinksView)
    await flushPromises()

    await wrapper.find('[data-testid="dry-run"]').setValue(true)
    await wrapper.find('[data-testid="repair-31"] button').trigger('click')
    await flushPromises()

    expect(calls.at(-1)).toMatchObject({
      path: '/api/repair/31/apply',
      body: { dry_run: true, accept_pending: true },
    })
  })
})
