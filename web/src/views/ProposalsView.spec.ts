import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { failure, installFakeApi, resetAppState, type FakeCall } from '@/test/fakeApi'
import { conflictProposal, maintenanceProposal, dedupeDetails, proposal } from '@/test/fixtures'
import ProposalsView from './ProposalsView.vue'

const toastError = vi.fn()
vi.mock('vue-sonner', () => ({
  toast: { error: (message: string) => toastError(message), success: vi.fn() },
}))

const forceable = proposal({
  id: 3,
  kind: 'partial_annex',
  step: 'seeded_paused_incomplete',
  decision: 'accepted',
})
const annexNotSeeded = proposal({
  id: 4,
  kind: 'partial_annex',
  step: 'linked',
  decision: 'accepted',
})
const partialPack = proposal({
  id: 5,
  kind: 'partial_pack',
  step: 'seeded_paused_incomplete',
  decision: 'accepted',
})
const stale = proposal({ id: 6, current: false })

async function mountView(
  rows = [proposal(), conflictProposal(), forceable, annexNotSeeded, partialPack, stale],
) {
  const calls = installFakeApi({
    'GET /api/proposals': rows,
    'POST /api/proposals/{id}/accept': { proposal_id: 1, action: 'accepted' },
    'POST /api/proposals/{id}/reject': { proposal_id: 1, action: 'rejected' },
    'POST /api/proposals/{id}/choose': { proposal_id: 8, action: 'chosen' },
    'POST /api/proposals/{id}/force': { proposal_id: 3, forced: true, warnings: [] },
    'POST /api/groups/{id}/accept': { group_id: 9, accepted: [7] },
  })
  const wrapper = mount(ProposalsView, { attachTo: document.body })
  await flushPromises()
  return { wrapper, calls }
}

const posts = (calls: FakeCall[]) => calls.filter((call) => call.method === 'POST')
const button = (wrapper: VueWrapper, proposalId: number, action: string) =>
  wrapper.find(`[data-testid="proposal-${proposalId}"] button[data-action="${action}"]`)

describe('ProposalsView', () => {
  beforeEach(() => {
    resetAppState()
    toastError.mockClear()
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    document.body.innerHTML = ''
  })

  it('ProposalsView_showsPercentVerifiedAndMissingPieces', async () => {
    const { wrapper } = await mountView()

    const card = wrapper.find('[data-testid="proposal-1"]')

    expect(card.find('[data-testid="verified"]').text()).toContain('99,5')
    expect(card.find('[data-testid="missing"]').text()).toBe('1')
    expect(card.find('[role="progressbar"]').attributes('aria-label')).toContain('99,5')
    expect(card.find('[data-testid="kind"]').text()).toBe('Torrent complet')
  })

  it('ProposalsView_hidesDedupeAndRepairRows', async () => {
    const { wrapper } = await mountView([proposal(), maintenanceProposal(99, dedupeDetails)])

    expect(wrapper.find('[data-testid="proposal-99"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="proposal-1"]').exists()).toBe(true)
  })

  it('ProposalsView_actionsCallTheMatchingApiRoutes', async () => {
    const { wrapper, calls } = await mountView()

    await button(wrapper, 1, 'proposals accept').trigger('click')
    await flushPromises()
    await button(wrapper, 1, 'proposals reject').trigger('click')
    await flushPromises()
    await button(wrapper, 7, 'proposals accept-group').trigger('click')
    await flushPromises()
    expect(document.body.textContent).toContain('cross-seed natif')
    document.body
      .querySelector<HTMLButtonElement>('[data-testid="confirm-dialog-confirm"]')
      ?.click()
    await flushPromises()

    const gotRoutes = posts(calls).map((call) => `${call.path} ${JSON.stringify(call.body)}`)
    expect(gotRoutes).toEqual([
      '/api/proposals/1/accept {"allow_partial_pack":false}',
      '/api/proposals/1/reject {}',
      '/api/groups/9/accept {}',
    ])
  })

  it('ProposalsView_chooseOffersOnlyCandidatesOfTheProposal', async () => {
    const { wrapper, calls } = await mountView()

    await button(wrapper, 7, 'proposals choose').trigger('click')
    await flushPromises()

    const radios = [...document.body.querySelectorAll<HTMLInputElement>('input[type="radio"]')]
    expect(radios.map((radio) => radio.value)).toEqual(['70', '71'])
    radios[1]?.click()
    await flushPromises()
    document.body.querySelector<HTMLButtonElement>('[data-testid="choose-confirm"]')?.click()
    await flushPromises()
    expect(posts(calls).map((call) => [call.path, call.body])).toEqual([
      ['/api/proposals/7/choose', { candidate_id: 71 }],
    ])
  })

  it('ProposalsView_enablesForceOnlyForPartialAnnexSeededPaused', async () => {
    const { wrapper, calls } = await mountView()

    const enabledIds = [1, 7, 3, 4, 5, 6].filter(
      (id) => button(wrapper, id, 'proposals force').attributes('disabled') === undefined,
    )

    expect(enabledIds).toEqual([3])
    await button(wrapper, 3, 'proposals force').trigger('click')
    await flushPromises()
    expect(document.body.textContent).toContain('réécrire des octets dans des fichiers liés')
    expect(posts(calls)).toHaveLength(0)
    document.body
      .querySelector<HTMLButtonElement>('[data-testid="confirm-dialog-confirm"]')
      ?.click()
    await flushPromises()
    expect(posts(calls).map((call) => [call.path, call.body])).toEqual([
      ['/api/proposals/3/force', { confirm: true }],
    ])
  })

  it('ProposalsView_neverForcesAPartialPack', async () => {
    const { wrapper } = await mountView()

    expect(button(wrapper, 5, 'proposals force').attributes('disabled')).toBeDefined()
  })

  it('ProposalsView_partialPackAcceptNeedsConfirmationAndAllowsPartial', async () => {
    const pending = proposal({ id: 8, kind: 'partial_pack' })
    const { wrapper, calls } = await mountView([pending])

    await button(wrapper, 8, 'proposals accept').trigger('click')
    await flushPromises()
    expect(posts(calls)).toHaveLength(0)
    document.body
      .querySelector<HTMLButtonElement>('[data-testid="confirm-dialog-confirm"]')
      ?.click()
    await flushPromises()

    expect(posts(calls).map((call) => call.body)).toEqual([{ allow_partial_pack: true }])
  })

  it('ProposalsView_disablesEveryActionOfAStaleRow', async () => {
    const { wrapper } = await mountView()

    const card = wrapper.find('[data-testid="proposal-6"]')

    expect(card.find('[data-testid="stale"]').text()).toBe('Obsolète')
    expect(card.attributes('aria-disabled')).toBeUndefined()
    expect(card.classes()).not.toContain('opacity-60')
    for (const actionButton of card.findAll('button'))
      expect(actionButton.attributes('disabled')).toBeDefined()
  })

  it('ProposalsView_offersGroupAcceptOnEveryPrimaryRow', async () => {
    const { wrapper } = await mountView([
      proposal({ id: 1, primary: true }),
      proposal({ id: 2, primary: false }),
    ])

    expect(button(wrapper, 1, 'proposals accept-group').exists()).toBe(true)
    expect(button(wrapper, 2, 'proposals accept-group').exists()).toBe(false)
  })

  it('ProposalsView_conflictCannotBeAcceptedDirectly', async () => {
    const { wrapper } = await mountView()

    expect(button(wrapper, 7, 'proposals accept').attributes('disabled')).toBeDefined()
    expect(button(wrapper, 1, 'proposals choose').exists()).toBe(false)
  })

  it('ProposalsView_sendsFiltersAsQueryParameters', async () => {
    const { wrapper, calls } = await mountView()

    await wrapper.find('[data-testid="filter-kind"]').setValue('conflict')
    await wrapper.find('[data-testid="filter-decision"]').setValue('pending')
    await wrapper.find('[data-testid="filter-group"]').setValue('9')
    await wrapper.find('[data-testid="filter-current"]').setValue(true)
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    const lastList = calls.filter((call) => call.path === '/api/proposals').at(-1)
    expect(Object.fromEntries(lastList?.query ?? [])).toEqual({
      kind: 'conflict',
      decision: 'pending',
      group: '9',
      current_only: 'true',
    })
  })

  it('ProposalsView_showsAFrenchToastForAStaleProposalError', async () => {
    const calls = installFakeApi({
      'GET /api/proposals': [proposal()],
      'POST /api/proposals/{id}/accept': () => failure(409, 'stale_proposal', 'raw english text'),
    })
    const wrapper = mount(ProposalsView)
    await flushPromises()

    await button(wrapper, 1, 'proposals accept').trigger('click')
    await flushPromises()

    expect(toastError).toHaveBeenCalledWith('Proposition périmée : relancez la vérification.')
    expect(calls.filter((call) => call.path === '/api/proposals').length).toBe(2)
  })

  it('ProposalsView_showsAnEmptyState', async () => {
    const { wrapper } = await mountView([])

    expect(wrapper.text()).toContain('Aucune proposition')
  })
})
