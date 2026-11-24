import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { installFakeApi, resetAppState } from '@/test/fakeApi'
import { job, linkItem, linkPage, maintenanceProposal, repairDetails } from '@/test/fixtures'
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
    await wrapper.find('[data-testid="repair-scan-form"]').trigger('submit')
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

  const recorded = [
    linkItem(1),
    linkItem(2, { kind: 'hardlink', state: 'diverged', reason: 'inode changed' }),
    linkItem(3, { state: 'lost', reason: 'the link target is missing' }),
    linkItem(4, { state: 'unreadable', reason: 'input/output error' }),
  ]

  it('LinksView_showsRecordedLinksWithStateAndFilter', async () => {
    const calls = installFakeApi({
      'GET /api/proposals': [],
      'GET /api/jobs': [],
      'GET /api/links': (call) =>
        call.query.get('state') === 'lost'
          ? linkPage([recorded[2]!])
          : linkPage(recorded, { counts: { ok: 1, diverged: 1, lost: 1, unreadable: 1 } }),
    })
    const wrapper = mount(LinksView)
    await flushPromises()

    const labels = recorded.map((item) =>
      wrapper.find(`[data-testid="link-${item.id}"] [data-state]`).text(),
    )
    expect(labels).toEqual(['Conforme', 'Divergent', 'Perdu', 'Illisible'])
    for (const badge of wrapper.findAll('[data-testid^="link-"] [data-state]'))
      expect(badge.find('svg').exists()).toBe(true)
    expect(wrapper.find('[data-testid="count-lost"]').text()).toBe('1')
    expect(wrapper.find('[data-testid="link-2"]').text()).toContain('lien physique')
    expect(wrapper.find('[data-testid="link-3"]').text()).toContain('the link target is missing')

    await wrapper.find('[data-testid="link-state-filter"]').setValue('lost')
    await flushPromises()

    const linkCalls = calls.filter((call) => call.path === '/api/links')
    expect(linkCalls.at(-1)?.query.get('state')).toBe('lost')
    expect(wrapper.find('[data-testid="link-1"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="link-3"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="links-empty"]').exists()).toBe(false)
  })

  it('LinksView_loadsMoreAndShowsTruncation', async () => {
    const calls = installFakeApi({
      'GET /api/proposals': [],
      'GET /api/jobs': [],
      'GET /api/links': (call) =>
        call.query.get('offset') === '2'
          ? linkPage(recorded.slice(2), { total: 4 })
          : linkPage(recorded.slice(0, 2), { total: 4, next: 2, truncated: true }),
    })
    const wrapper = mount(LinksView)
    await flushPromises()

    expect(wrapper.find('[data-testid="links-truncated"]').text()).toContain('partiels')
    expect(wrapper.findAll('[data-testid="search-repair"]').length).toBe(1)

    await wrapper.find('[data-testid="links-more"]').trigger('click')
    await flushPromises()

    expect(
      calls
        .filter((call) => call.path === '/api/links')
        .at(-1)
        ?.query.get('offset'),
    ).toBe('2')
    expect(wrapper.find('[data-testid="link-1"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="link-4"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="links-more"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="links-truncated"]').exists()).toBe(false)
  })

  it('LinksView_showsAnEmptyStateWithoutRecordedLinks', async () => {
    installFakeApi({
      'GET /api/proposals': [],
      'GET /api/jobs': [],
      'GET /api/links': linkPage([], { counts: { ok: 0, diverged: 0, lost: 0, unreadable: 0 } }),
    })
    const wrapper = mount(LinksView)
    await flushPromises()

    expect(wrapper.find('[data-testid="links-empty"]').text()).toContain('Aucun lien enregistré')
  })

  it('LinksView_offersRepairScanForBrokenLinks', async () => {
    const calls = installFakeApi({
      'GET /api/proposals': [],
      'GET /api/jobs': [],
      'GET /api/links': linkPage(recorded),
      'POST /api/repair/scan': () => job({ id: 5, action: 'repair scan' }),
    })
    const wrapper = mount(LinksView)
    await flushPromises()

    expect(wrapper.find('[data-testid="link-1"] [data-testid="search-repair"]').exists()).toBe(
      false,
    )
    for (const brokenId of [2, 3, 4])
      expect(
        wrapper.find(`[data-testid="link-${brokenId}"] [data-testid="search-repair"]`).text(),
      ).toContain('Chercher une réparation')

    await wrapper.find('[data-testid="link-3"] [data-testid="search-repair"]').trigger('click')
    await flushPromises()

    expect(
      calls.filter((call) => call.method === 'POST').map((call) => [call.path, call.body]),
    ).toEqual([['/api/repair/scan', { directory: '' }]])
  })
})
