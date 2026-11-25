import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { failure, installFakeApi, resetAppState, type FakeCall } from '@/test/fakeApi'
import { conflictProposal, dedupeDetails, maintenanceProposal, proposal } from '@/test/fixtures'
import { stubViewport } from '@/test/viewport'
import ProposalsView from './ProposalsView.vue'

const toastError = vi.fn()
const toastSuccess = vi.fn()
vi.mock('vue-sonner', () => ({
  toast: {
    error: (message: string) => toastError(message),
    success: (message: string) => toastSuccess(message),
  },
}))

const pendingOne = proposal({ id: 1, candidate_name: 'Film.A', group_id: 5, primary: true })
const pendingTwo = proposal({ id: 2, candidate_name: 'Film.B', group_id: 5, primary: false })
const acceptedRow = proposal({ id: 9, candidate_name: 'Film.Accepted', decision: 'accepted' })
const forceable = proposal({
  id: 3,
  kind: 'partial_annex',
  step: 'seeded_paused_incomplete',
  decision: 'applied',
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
  decision: 'applied',
})
const stale = proposal({ id: 6, current: false, candidate_name: 'Film.Stale' })

const ALL_ROWS = [
  pendingOne,
  pendingTwo,
  acceptedRow,
  conflictProposal(),
  forceable,
  annexNotSeeded,
  partialPack,
  stale,
]

async function mountView(rows = ALL_ROWS, extraRoutes: Record<string, unknown> = {}) {
  const calls = installFakeApi({
    'GET /api/proposals': rows,
    'POST /api/proposals/{id}/accept': { proposal_id: 1, action: 'accepted' },
    'POST /api/proposals/{id}/reject': { proposal_id: 1, action: 'rejected' },
    'POST /api/proposals/{id}/choose': { proposal_id: 8, action: 'chosen' },
    'POST /api/proposals/{id}/force': { proposal_id: 3, forced: true, warnings: [] },
    'POST /api/groups/{id}/accept': { group_id: 9, accepted: [7] },
    'GET /api/status': {
      candidates: {},
      proposals: {},
      steps: {},
      cross_seed_mode: 'disabled',
      language: 'fr',
    },
    ...extraRoutes,
  })
  const wrapper = mount(ProposalsView, { attachTo: document.body })
  await flushPromises()
  return { wrapper, calls }
}

const posts = (calls: FakeCall[]) => calls.filter((call) => call.method === 'POST')
const rowOf = (wrapper: VueWrapper, proposalId: number) =>
  wrapper.find(`tr[data-row-key="${proposalId}"]`)
const rowButton = (wrapper: VueWrapper, proposalId: number, action: string) =>
  rowOf(wrapper, proposalId).find(`button[data-action="${action}"]`)
const rowCheckbox = (wrapper: VueWrapper, proposalId: number) =>
  rowOf(wrapper, proposalId).find('input[type="checkbox"]')
const dialogButton = (testId: string) =>
  document.body.querySelector<HTMLButtonElement>(`[data-testid="${testId}"]`)
const dialogText = () => document.body.querySelector('[role="dialog"]')?.textContent ?? ''

async function confirmDialog(): Promise<void> {
  dialogButton('confirm-dialog-confirm')?.click()
  await flushPromises()
}

async function select(wrapper: VueWrapper, proposalIds: number[]): Promise<void> {
  for (const proposalId of proposalIds) await rowCheckbox(wrapper, proposalId).setValue(true)
}

describe('ProposalsView', () => {
  beforeEach(() => {
    resetAppState()
    stubViewport(1280)
    toastError.mockClear()
    toastSuccess.mockClear()
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    document.body.innerHTML = ''
  })

  it('shows percent verified, missing pieces and the translated kind and decision', async () => {
    const { wrapper } = await mountView()

    const row = rowOf(wrapper, 1)

    expect(row.text()).toContain('99,5')
    expect(row.text()).toContain('1 sur 200')
    expect(row.text()).toContain('Torrent complet')
    expect(row.text()).toContain('En attente')
    expect(row.text()).toContain('★')
  })

  it('sorts by decision by default, what waits for an action first', async () => {
    const { wrapper } = await mountView([acceptedRow, forceable, pendingOne])

    const gotOrder = wrapper.findAll('tbody tr').map((row) => row.attributes('data-row-key'))

    expect(gotOrder).toEqual(['1', '9', '3'])
  })

  it('has no own page title, the shell owns it', async () => {
    const { wrapper } = await mountView()

    expect(wrapper.find('h2').exists()).toBe(false)
  })

  it('hides dedupe and repair rows', async () => {
    const { wrapper } = await mountView([proposal(), maintenanceProposal(99, dedupeDetails)])

    expect(rowOf(wrapper, 99).exists()).toBe(false)
    expect(rowOf(wrapper, 1).exists()).toBe(true)
  })

  describe('anti-bulk rule', () => {
    it('offers no bulk accept on a free selection', async () => {
      const { wrapper } = await mountView()
      await select(wrapper, [1, 2])

      const toolbarActions = wrapper.findAll('[data-scope="toolbar"], [data-scope="bulk"]')

      const toolbarAccept = wrapper.find(
        '[data-action="proposals accept"][data-scope="toolbar"], [data-action="proposals accept"][data-scope="bulk"]',
      )
      expect(toolbarAccept.exists()).toBe(false)
      const gotActions = toolbarActions.map((element) => element.attributes('data-action'))
      expect(gotActions.filter((action) => action !== 'proposals list')).toEqual([
        'proposals reject',
      ])
    })

    it('offers only reject on the selection toolbar', async () => {
      const { wrapper } = await mountView()
      await select(wrapper, [1])

      const bulkButton = wrapper.get('[data-scope="bulk"]')

      expect(bulkButton.text()).toContain('Rejeter la sélection (1)')
      expect(bulkButton.attributes('data-action')).toBe('proposals reject')
    })

    it('disables selection on non-pending rows', async () => {
      const { wrapper } = await mountView()

      const gotSelectableIds = ALL_ROWS.filter((row) => rowCheckbox(wrapper, row.id).exists()).map(
        (row) => row.id,
      )

      expect(gotSelectableIds).toEqual([1, 2, 7])
    })

    it('rejects selected pending proposals one request each after confirmation', async () => {
      const { wrapper, calls } = await mountView()
      await select(wrapper, [1, 2])

      await wrapper.get('[data-scope="bulk"]').trigger('click')
      await flushPromises()

      expect(posts(calls)).toHaveLength(0)
      expect(dialogText()).toContain('Film.A')
      expect(dialogText()).toContain('Film.B')
      expect(dialogText()).not.toContain('proposals.')
      await confirmDialog()
      expect(posts(calls).map((call) => call.path)).toEqual([
        '/api/proposals/1/reject',
        '/api/proposals/2/reject',
      ])
      expect(toastSuccess).toHaveBeenCalledWith('2 proposition(s) rejetée(s).')
    })

    it('sends nothing when the reject dialog is cancelled', async () => {
      const { wrapper, calls } = await mountView()
      await select(wrapper, [1, 2])
      await wrapper.get('[data-scope="bulk"]').trigger('click')
      await flushPromises()

      dialogButton('confirm-dialog-cancel')?.click()
      await flushPromises()

      expect(posts(calls)).toHaveLength(0)
    })

    it('sends nothing when the reject dialog is closed with Escape', async () => {
      const { wrapper, calls } = await mountView()
      await select(wrapper, [1, 2])
      await wrapper.get('[data-scope="bulk"]').trigger('click')
      await flushPromises()

      document.body
        .querySelector('[role="dialog"]')
        ?.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
      await flushPromises()

      expect(posts(calls)).toHaveLength(0)
      expect(document.body.querySelector('[role="dialog"]')).toBeNull()
    })

    it('keeps rejecting the others when one request fails', async () => {
      const { wrapper, calls } = await mountView(ALL_ROWS, {
        'POST /api/proposals/{id}/reject': (call: FakeCall) =>
          call.path === '/api/proposals/1/reject'
            ? failure(409, 'stale_proposal')
            : { proposal_id: 2, action: 'rejected' },
      })
      await select(wrapper, [1, 2])
      await wrapper.get('[data-scope="bulk"]').trigger('click')
      await flushPromises()

      await confirmDialog()

      expect(posts(calls).map((call) => call.path)).toEqual([
        '/api/proposals/1/reject',
        '/api/proposals/2/reject',
      ])
      expect(toastError).toHaveBeenCalledTimes(1)
      expect(toastSuccess).toHaveBeenCalledWith('1 proposition(s) rejetée(s).')
    })

    it('clears the selection once the rejections are sent', async () => {
      const { wrapper } = await mountView()
      await select(wrapper, [1, 2])
      await wrapper.get('[data-scope="bulk"]').trigger('click')
      await flushPromises()
      await confirmDialog()

      expect(wrapper.get('[data-scope="bulk"]').attributes('disabled')).toBeDefined()
      expect(wrapper.get('[data-scope="bulk"]').text()).toContain('(0)')
      expect(rowCheckbox(wrapper, 1).element).toHaveProperty('checked', false)
    })

    describe('accept-group consent', () => {
      const groupRows = [
        proposal({ id: 1, candidate_id: 10, candidate_name: 'Film.A', group_id: 5, primary: true }),
        proposal({
          id: 2,
          candidate_id: 11,
          candidate_name: 'Film.CrossSeed',
          kind: 'cross_seed',
          group_id: 5,
          primary: false,
        }),
        proposal({
          id: 3,
          candidate_id: 12,
          candidate_name: 'Film.FailedMember',
          decision: 'failed',
          group_id: 5,
          primary: false,
        }),
      ]
      const routesFor = (mode: string, accepted = [1, 2]) => ({
        'GET /api/proposals': (call: FakeCall) =>
          call.query.get('group') ? groupRows : [pendingOne, ...ALL_ROWS.slice(1)],
        'GET /api/status': {
          candidates: {},
          proposals: {},
          steps: {},
          cross_seed_mode: mode,
          language: 'fr',
        },
        'POST /api/groups/{id}/accept': { group_id: 5, accepted },
      })

      it('lists what the server will accept in native mode, whatever the view filters', async () => {
        const { wrapper, calls } = await mountView(ALL_ROWS, routesFor('native'))
        await wrapper.get('[data-testid="filter-kind"]').setValue('full')
        await flushPromises()

        await rowButton(wrapper, 1, 'proposals accept-group').trigger('click')
        await flushPromises()

        expect(dialogText()).toContain('★ Film.A')
        expect(dialogText()).toContain('Film.CrossSeed')
        expect(dialogText()).toContain('cross-seed natif')
        expect(dialogText()).not.toContain('Film.FailedMember')
        const groupRequest = calls.find((call) => call.query.get('group') === '5')
        expect(groupRequest?.query.get('kind')).toBeNull()
        expect(groupRequest?.query.get('current_only')).toBe('true')
        expect(posts(calls)).toHaveLength(0)
      })

      it('lists only the primary when cross-seed is not native', async () => {
        const { wrapper } = await mountView(ALL_ROWS, routesFor('disabled'))

        await rowButton(wrapper, 1, 'proposals accept-group').trigger('click')
        await flushPromises()

        expect(dialogText()).toContain('★ Film.A')
        expect(dialogText()).not.toContain('Film.CrossSeed')
        expect(dialogText()).toContain('seule la principale')
      })

      it('accepts one group and reports how many proposals the server accepted', async () => {
        const { wrapper, calls } = await mountView(ALL_ROWS, routesFor('native', [1, 2]))
        await rowButton(wrapper, 1, 'proposals accept-group').trigger('click')
        await flushPromises()

        await confirmDialog()

        expect(posts(calls).map((call) => [call.path, call.body])).toEqual([
          ['/api/groups/5/accept', {}],
        ])
        expect(toastSuccess).toHaveBeenCalledWith('2 proposition(s) acceptée(s).')
      })

      it('does not enable accept-group on a failed primary', async () => {
        const failedPrimary = proposal({ id: 30, decision: 'failed', primary: true })
        const { wrapper } = await mountView([failedPrimary])

        expect(
          rowButton(wrapper, 30, 'proposals accept-group').attributes('disabled'),
        ).toBeDefined()
      })
    })

    it('sends nothing when the accept-group dialog is cancelled', async () => {
      const { wrapper, calls } = await mountView()
      await rowButton(wrapper, 1, 'proposals accept-group').trigger('click')
      await flushPromises()

      dialogButton('confirm-dialog-cancel')?.click()
      await flushPromises()

      expect(posts(calls)).toHaveLength(0)
    })

    it('offers accept-group only on primary rows', async () => {
      const { wrapper } = await mountView()

      expect(rowButton(wrapper, 1, 'proposals accept-group').exists()).toBe(true)
      expect(rowButton(wrapper, 2, 'proposals accept-group').exists()).toBe(false)
    })

    it('never offers force in a group action', async () => {
      const { wrapper } = await mountView()
      await select(wrapper, [1, 2])

      const forceButtons = wrapper.findAll('[data-action="proposals force"]')

      expect(forceButtons.every((button) => button.attributes('data-scope') === 'row')).toBe(true)
      await rowButton(wrapper, 1, 'proposals accept-group').trigger('click')
      await flushPromises()
      expect(dialogText().toLowerCase()).not.toContain('forc')
    })

    it('enables force only for a partial annex seeded paused, with its warning and phrase', async () => {
      const { wrapper, calls } = await mountView()

      const enabledIds = ALL_ROWS.map((row) => row.id).filter((id) => {
        const forceButton = rowButton(wrapper, id, 'proposals force')
        return forceButton.exists() && forceButton.attributes('disabled') === undefined
      })

      expect(enabledIds).toEqual([3])
      await rowButton(wrapper, 3, 'proposals force').trigger('click')
      await flushPromises()
      expect(dialogText()).toContain('la bibliothèque n’est jamais écrite')
      expect(dialogText()).toContain('copie indépendante')
      expect(posts(calls)).toHaveLength(0)
      await confirmDialog()
      expect(posts(calls)).toHaveLength(0)
      const phraseInput = document.body.querySelector<HTMLInputElement>(
        '[data-testid="confirm-dialog-phrase"]',
      )
      phraseInput!.value = 'FORCER'
      phraseInput!.dispatchEvent(new Event('input'))
      await flushPromises()
      await confirmDialog()
      expect(posts(calls).map((call) => [call.path, call.body])).toEqual([
        ['/api/proposals/3/force', { confirm: true }],
      ])
    })

    it.each(['forcer', 'FORCER ', 'FORCE', ' FORCER', 'Forcer'])(
      'sends nothing when the force confirmation word is %j',
      async (typedWord) => {
        const { wrapper, calls } = await mountView()
        await rowButton(wrapper, 3, 'proposals force').trigger('click')
        await flushPromises()

        const phraseInput = document.body.querySelector<HTMLInputElement>(
          '[data-testid="confirm-dialog-phrase"]',
        )
        phraseInput!.value = typedWord
        phraseInput!.dispatchEvent(new Event('input'))
        await flushPromises()
        await confirmDialog()

        expect(posts(calls)).toHaveLength(0)
        expect(dialogButton('confirm-dialog-confirm')?.disabled).toBe(true)
      },
    )

    it('words force as a start and warns that changed sources refuse it', async () => {
      const { wrapper } = await mountView()

      await rowButton(wrapper, 3, 'proposals force').trigger('click')
      await flushPromises()

      expect(dialogText()).toContain('Forcer le démarrage')
      expect(dialogText()).not.toContain('proposals.')
      expect(dialogText()).toContain('obsolète')
      expect(dialogText()).not.toContain('Forcer l’ajout')
    })

    it('never offers force on a partial pack', async () => {
      const { wrapper } = await mountView()

      expect(rowButton(wrapper, 5, 'proposals force').exists()).toBe(false)
    })

    it('disables every action of a stale row with the disabled attribute', async () => {
      const { wrapper } = await mountView()

      const row = rowOf(wrapper, 6)

      expect(row.text()).toContain('Obsolète')
      expect(rowCheckbox(wrapper, 6).exists()).toBe(false)
      const actionButtons = row.findAll('button[data-action]')
      expect(actionButtons.length).toBeGreaterThan(0)
      for (const actionButton of actionButtons)
        expect(actionButton.attributes('disabled')).toBeDefined()
    })
  })

  it('calls the matching API routes from the row actions', async () => {
    const { wrapper, calls } = await mountView()

    await rowButton(wrapper, 1, 'proposals accept').trigger('click')
    await flushPromises()
    await rowButton(wrapper, 2, 'proposals reject').trigger('click')
    await flushPromises()

    const gotRoutes = posts(calls).map((call) => `${call.path} ${JSON.stringify(call.body)}`)
    expect(gotRoutes).toEqual([
      '/api/proposals/1/accept {"allow_partial_pack":false}',
      '/api/proposals/2/reject {}',
    ])
  })

  it('does not offer reject on a row that is no longer pending', async () => {
    const { wrapper } = await mountView()

    expect(rowButton(wrapper, 9, 'proposals reject').attributes('disabled')).toBeDefined()
  })

  it('cannot accept a conflict directly and chooses among its candidates', async () => {
    const { wrapper, calls } = await mountView()

    expect(rowButton(wrapper, 7, 'proposals accept').attributes('disabled')).toBeDefined()
    expect(rowButton(wrapper, 1, 'proposals choose').exists()).toBe(false)
    await rowButton(wrapper, 7, 'proposals choose').trigger('click')
    await flushPromises()
    const radios = [...document.body.querySelectorAll<HTMLInputElement>('input[type="radio"]')]
    expect(radios.map((radio) => radio.value)).toEqual(['70', '71'])
    radios[1]?.click()
    await flushPromises()
    dialogButton('choose-confirm')?.click()
    await flushPromises()

    expect(posts(calls).map((call) => [call.path, call.body])).toEqual([
      ['/api/proposals/7/choose', { candidate_id: 71 }],
    ])
  })

  it('asks before accepting a partial pack and allows it', async () => {
    const pending = proposal({ id: 8, kind: 'partial_pack' })
    const { wrapper, calls } = await mountView([pending])

    await rowButton(wrapper, 8, 'proposals accept').trigger('click')
    await flushPromises()
    expect(posts(calls)).toHaveLength(0)
    expect(dialogText()).toContain('ne peut jamais le démarrer')
    await confirmDialog()

    expect(posts(calls).map((call) => call.body)).toEqual([{ allow_partial_pack: true }])
  })

  it('shows the text of structured warnings and excluded files in the expandable detail', async () => {
    const detailed = proposal({
      id: 12,
      warnings: [{ code: 'warning.sources_changed', text: 'sources changed' }],
      details: {
        percent: 90,
        verified_pieces: 9,
        missing_pieces: 1,
        total_pieces: 10,
        missing_media_files: ['Film/extra.mkv'],
        excluded_files: [
          { torrent_path: 'Film/sample.mkv', reason: 'sample', label: 'Échantillon' },
        ],
      },
    })
    const { wrapper } = await mountView([detailed])
    expect(rowOf(wrapper, 12).text()).not.toContain('sources changed')

    await rowOf(wrapper, 12).get('button[aria-expanded]').trigger('click')

    const detailText = rowOf(wrapper, 12).text()
    expect(detailText).toContain('sources changed')
    expect(detailText).not.toContain('warning.sources_changed')
    expect(detailText).toContain('Film/extra.mkv')
    expect(detailText).toContain('Film/sample.mkv')
    expect(detailText).toContain('Échantillon')
    expect(rowOf(wrapper, 12).get('button[aria-expanded]').attributes('aria-expanded')).toBe('true')
  })

  it('shows the count of warnings in its column', async () => {
    const warned = proposal({
      id: 13,
      warnings: [
        { code: 'a', text: 'first' },
        { code: 'b', text: 'second' },
      ],
    })
    const { wrapper } = await mountView([warned])

    const cells = rowOf(wrapper, 13).findAll('td')

    expect(cells.some((cell) => cell.text() === '2')).toBe(true)
  })

  it('lists the members of a conflict in its detail', async () => {
    const { wrapper } = await mountView()

    await rowOf(wrapper, 7).get('button[aria-expanded]').trigger('click')

    expect(rowOf(wrapper, 7).text()).toContain('Serie.S01.B')
  })

  it('sends the server filters as query parameters', async () => {
    const { wrapper, calls } = await mountView()

    await wrapper.get('[data-testid="filter-kind"]').setValue('conflict')
    await flushPromises()
    await wrapper.get('[data-testid="filter-decision"]').setValue('pending')
    await flushPromises()
    await wrapper.get('[data-testid="filter-group"]').setValue('9')
    await wrapper.get('[data-testid="filter-group"]').trigger('change')
    await flushPromises()
    await wrapper.get('[data-testid="filter-current"]').setValue(true)
    await flushPromises()

    const lastList = calls.filter((call) => call.path === '/api/proposals').at(-1)
    expect(Object.fromEntries(lastList?.query ?? [])).toEqual({
      kind: 'conflict',
      decision: 'pending',
      group: '9',
      current_only: 'true',
    })
  })

  it('searches by name without a request', async () => {
    const { wrapper, calls } = await mountView()
    const listCalls = calls.length

    await wrapper.get('[data-testid="filter-search"]').setValue('film.b')

    expect(wrapper.findAll('tbody tr').map((row) => row.attributes('data-row-key'))).toEqual(['2'])
    expect(calls).toHaveLength(listCalls)
  })

  it('shows the Source column and filters by source on the server', async () => {
    const fromProvider = proposal({
      id: 20,
      candidate_name: 'From.Provider',
      details: { ...proposal().details, source: 'provider' },
    })
    const fromIndexer = proposal({
      id: 21,
      candidate_name: 'From.Indexer',
      details: { ...proposal().details, source: 'torznab:a' },
    })
    const { wrapper, calls } = await mountView([fromProvider, fromIndexer], {
      'GET /api/proposals': (call: FakeCall) =>
        call.query.get('source') === 'torznab:a' ? [fromIndexer] : [fromProvider, fromIndexer],
    })

    const headers = wrapper.findAll('th').map((header) => header.text())
    expect(headers).toContain('Source')
    const sourceColumn = headers.indexOf('Source')
    expect(
      wrapper.findAll('tbody tr').map((row) => row.findAll('td')[sourceColumn]?.text()),
    ).toEqual(['provider', 'torznab:a'])

    await wrapper.get('[data-testid="filter-source"]').setValue('torznab:a')
    await flushPromises()

    expect(calls.at(-1)?.query.get('source')).toBe('torznab:a')
    expect(wrapper.findAll('tbody tr').map((row) => row.attributes('data-row-key'))).toEqual(['21'])
    const options = wrapper
      .findAll('[data-testid="filter-source"] option')
      .map((option) => option.text())
    expect(options).toContain('provider')
  })

  it('shows a translated toast for a stale proposal error', async () => {
    const calls = installFakeApi({
      'GET /api/proposals': [proposal()],
      'POST /api/proposals/{id}/accept': () => failure(409, 'stale_proposal', 'raw english text'),
    })
    const wrapper = mount(ProposalsView, { attachTo: document.body })
    await flushPromises()

    await rowButton(wrapper, 1, 'proposals accept').trigger('click')
    await flushPromises()

    expect(toastError).toHaveBeenCalledWith('Proposition périmée : relancez la vérification.')
    expect(calls.filter((call) => call.path === '/api/proposals').length).toBe(2)
  })

  it('keeps the selection visible and counted when the layout switches', async () => {
    const resize = stubViewport(1280)
    const { wrapper } = await mountView()
    await select(wrapper, [1, 2])

    resize(360)
    await flushPromises()

    const cardCheckboxes = wrapper.findAll('[data-testid="table-card"] input[type="checkbox"]')
    const gotChecked = cardCheckboxes.map((box) => (box.element as HTMLInputElement).checked)
    expect(gotChecked.filter(Boolean)).toHaveLength(2)
    expect(wrapper.get('[data-scope="bulk"]').text()).toContain('(2)')
    resize(1280)
    await flushPromises()
    expect((rowCheckbox(wrapper, 1).element as HTMLInputElement).checked).toBe(true)
  })

  it('shows the excluded reason code visibly when its label is missing', async () => {
    const unlabelled = proposal({
      id: 14,
      details: {
        percent: 90,
        verified_pieces: 9,
        missing_pieces: 1,
        total_pieces: 10,
        excluded_files: [{ torrent_path: 'Film/odd.bin', reason: 'brand_new_reason' }],
      },
    })
    const { wrapper } = await mountView([unlabelled])

    await rowOf(wrapper, 14).get('button[aria-expanded]').trigger('click')

    expect(rowOf(wrapper, 14).text()).toContain('brand_new_reason')
  })

  it('shows an empty state instead of guessing candidates for a conflict without members', async () => {
    const memberless = proposal({ id: 15, kind: 'conflict', candidate_name: 'Docu.Alone' })
    const { wrapper } = await mountView([memberless])

    await rowButton(wrapper, 15, 'proposals choose').trigger('click')
    await flushPromises()

    expect(document.body.querySelectorAll('input[type="radio"]')).toHaveLength(0)
    expect(dialogText()).toContain('Aucun candidat')
    expect(dialogButton('choose-confirm')?.disabled).toBe(true)
  })

  it('keeps the detail container in the DOM so aria-controls never dangles', async () => {
    const { wrapper } = await mountView()

    const toggle = rowOf(wrapper, 1).get('button[aria-expanded]')

    const controlled = toggle.attributes('aria-controls') ?? ''
    expect(document.getElementById(controlled)).not.toBeNull()
    expect(document.getElementById(controlled)?.hasAttribute('hidden')).toBe(true)
    await toggle.trigger('click')
    expect(document.getElementById(controlled)?.hasAttribute('hidden')).toBe(false)
  })

  it('lists name and kind in the reject dialog, even for duplicate names', async () => {
    const twins = [
      proposal({ id: 40, candidate_name: 'Twin', candidate_id: 40 }),
      proposal({ id: 41, candidate_name: 'Twin', candidate_id: 41, kind: 'partial_pack' }),
    ]
    const { wrapper } = await mountView(twins)
    await select(wrapper, [40, 41])

    await wrapper.get('[data-scope="bulk"]').trigger('click')
    await flushPromises()

    const items = [
      ...document.body.querySelectorAll('[data-testid="confirm-dialog-items"] li'),
    ].map((item) => item.textContent)
    expect(items).toEqual(['Twin (Torrent complet)', 'Twin (Pack partiel)'])
  })

  it('shows a tooltip with the label on keyboard focus of an icon-only action', async () => {
    const { wrapper } = await mountView()

    ;(rowButton(wrapper, 1, 'proposals accept').element as HTMLButtonElement).focus()
    await flushPromises()

    expect(document.body.querySelector('[role="tooltip"]')?.textContent).toContain('Accepter')
  })

  it('shows the update time in a sortable column with the full timestamp as title', async () => {
    const older = proposal({ id: 50, updated_at: '2026-01-01T10:00:00Z' })
    const newer = proposal({ id: 51, updated_at: '2026-03-01T10:00:00Z' })
    const { wrapper } = await mountView([older, newer])

    const headers = wrapper.findAll('th').map((header) => header.text())
    expect(headers).toContain('Mis à jour')
    const time = rowOf(wrapper, 50).get('time')
    expect(time.attributes('datetime')).toBe('2026-01-01T10:00:00Z')
    expect(time.attributes('title')).toContain('2026')
    const updatedHeader = wrapper.findAll('th').find((header) => header.text() === 'Mis à jour')
    await updatedHeader!.get('button').trigger('click')
    expect(updatedHeader!.attributes('aria-sort')).toBe('ascending')
    expect(wrapper.findAll('tbody tr').map((row) => row.attributes('data-row-key'))).toEqual([
      '50',
      '51',
    ])
  })

  it('shows the no-announce pill and keeps accept enabled', async () => {
    const withoutAnnounce = proposal({
      id: 11,
      candidate_name: 'Film.NoAnnounce',
      details: { ...proposal().details, seedable: false, announce_source: 'none' },
    })
    const withAnnounce = proposal({
      id: 12,
      candidate_name: 'Film.Announce',
      details: { ...proposal().details, seedable: true, announce_source: 'config' },
    })
    const { wrapper, calls } = await mountView([withoutAnnounce, withAnnounce])

    const pill = rowOf(wrapper, 11).find('[data-tone="warn"]')
    const explanation = "Le lien sera refusé tant qu'une entrée `trackers` n'est pas ajoutée"
    expect(pill.text()).toBe('Sans annonce')
    expect(pill.attributes('title')).toBe(explanation)
    expect(pill.attributes('aria-description')).toBe(explanation)
    expect(rowOf(wrapper, 12).text()).not.toContain('Sans annonce')
    const accept = rowButton(wrapper, 11, 'proposals accept')
    expect(accept.attributes('disabled')).toBeUndefined()
    expect(accept.attributes('title')).toBeUndefined()
    await accept.trigger('click')
    await flushPromises()
    expect(posts(calls).map((call) => call.path)).toEqual(['/api/proposals/11/accept'])
  })

  it('shows an empty state', async () => {
    const { wrapper } = await mountView([])

    expect(wrapper.text()).toContain('Aucune proposition')
  })
})
