import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setLanguage } from '@/i18n'
import { failure, installFakeApi, resetAppState, type FakeCall } from '@/test/fakeApi'
import { dedupeDetails, job, maintenanceProposal } from '@/test/fixtures'
import { stubViewport } from '@/test/viewport'
import DedupeView from './DedupeView.vue'

const toastError = vi.fn()
const toastSuccess = vi.fn()
vi.mock('vue-sonner', () => ({
  toast: {
    error: (message: string) => toastError(message),
    success: (message: string) => toastSuccess(message),
  },
}))

const pendingRow = maintenanceProposal(21, dedupeDetails)
const ambiguousRow = maintenanceProposal(22, {
  ...dedupeDetails,
  torrent_path: '/data/torrents/Ambigu.mkv',
  actionable: false,
  ambiguous: ['/data/library/A.mkv', '/data/library/B.mkv'],
  reason: 'Plusieurs fichiers possibles',
})
const acceptedRow = maintenanceProposal(23, dedupeDetails, 'accepted')
const isolatedRow = maintenanceProposal(25, {
  ...dedupeDetails,
  torrent_path: '/data/torrents/Isolee.mkv',
  library_path: undefined,
  actionable: false,
  reason: 'isolated_copy',
  label: 'Copie isolée : jamais reliée à la bibliothèque',
})
const twoAccepted = [
  maintenanceProposal(23, dedupeDetails, 'accepted'),
  maintenanceProposal(
    24,
    { ...dedupeDetails, torrent_path: '/data/torrents/Autre.mkv' },
    'accepted',
  ),
]

const ALL_ROWS = [pendingRow, ambiguousRow, acceptedRow, isolatedRow]

const mounted: VueWrapper[] = []

async function mountView(rows = ALL_ROWS, jobState: { finished: boolean } = { finished: false }) {
  const calls = installFakeApi({
    'GET /api/proposals': rows,
    'GET /api/jobs': () => [
      job({ id: 10, action: 'dedupe apply', state: jobState.finished ? 'done' : 'running' }),
    ],
    'POST /api/dedupe/plan': () => job({ id: 9, action: 'dedupe plan' }),
    'POST /api/dedupe/{id}/apply': () => job({ id: 10, action: 'dedupe apply' }),
    'POST /api/proposals/{id}/reject': { proposal_id: 21, action: 'rejected' },
  })
  const wrapper = mount(DedupeView, { attachTo: document.body })
  mounted.push(wrapper)
  await flushPromises()
  return { wrapper, calls }
}

const posts = (calls: FakeCall[]) => calls.filter((call) => call.method === 'POST')
const rowOf = (wrapper: VueWrapper, proposalId: number) =>
  wrapper.get(`tr[data-row-key="${proposalId}"]`)
const rowApply = (wrapper: VueWrapper, proposalId: number) =>
  rowOf(wrapper, proposalId).get('button[data-action="dedupe apply"]')
const dryRunBox = (wrapper: VueWrapper) => wrapper.get<HTMLInputElement>('[data-testid="dry-run"]')
const dialogButton = (testId: string) =>
  document.body.querySelector<HTMLButtonElement>(`[data-testid="${testId}"]`)
const dialogText = () => document.body.querySelector('[role="dialog"]')?.textContent ?? ''

async function typePhrase(word: string): Promise<void> {
  const phraseInput = document.body.querySelector<HTMLInputElement>(
    '[data-testid="confirm-dialog-phrase"]',
  )
  phraseInput!.value = word
  phraseInput!.dispatchEvent(new Event('input'))
  await flushPromises()
}

async function confirmDialog(): Promise<void> {
  dialogButton('confirm-dialog-confirm')?.click()
  await flushPromises()
}

async function pressEscape(): Promise<void> {
  document.body
    .querySelector('[role="dialog"]')
    ?.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
  await flushPromises()
}

describe('DedupeView', () => {
  beforeEach(() => {
    resetAppState()
    stubViewport(1280)
    vi.useFakeTimers()
    toastError.mockClear()
    toastSuccess.mockClear()
  })
  afterEach(() => {
    for (const wrapper of mounted.splice(0)) wrapper.unmount()
    vi.useRealTimers()
    vi.unstubAllGlobals()
    document.body.innerHTML = ''
  })

  it('has no own page title and no description card, the shell owns the title', async () => {
    const { wrapper } = await mountView()

    expect(wrapper.find('h2').exists()).toBe(false)
  })

  it('shows the eight columns of the replacement table', async () => {
    const { wrapper } = await mountView()

    const gotHeaders = wrapper.findAll('thead th').map((header) => header.text())

    for (const expectedValue of [
      'Fichier côté torrent',
      'Cible bibliothèque',
      'Sorte',
      'Preuve',
      'Octets à lire',
      'Décision',
      'Applicable',
      'Avertissements',
    ]) {
      expect(gotHeaders).toContain(expectedValue)
    }
  })

  it('sorts by decision by default, what waits for an action first', async () => {
    const { wrapper } = await mountView([acceptedRow, pendingRow, isolatedRow])

    const gotOrder = wrapper.findAll('tbody tr').map((row) => row.attributes('data-row-key'))

    expect(gotOrder).toEqual(['21', '25', '23'])
  })

  it('shows bytes to read before acceptance, with the exact count in a title', async () => {
    const { wrapper } = await mountView()

    const bytesCell = rowOf(wrapper, 21).get('[data-testid="bytes-to-read"]')

    expect(rowOf(wrapper, 21).text()).toContain('En attente')
    expect(bytesCell.text().replace(/\s/g, ' ')).toBe('977 Kio')
    expect(bytesCell.attributes('title')?.replace(/\s/g, ' ')).toBe('1 000 000 o')
  })

  it('shows bytes to read in the active language', async () => {
    setLanguage('en')
    const { wrapper } = await mountView()

    const bytesCell = rowOf(wrapper, 21).get('[data-testid="bytes-to-read"]')

    expect(bytesCell.text()).toBe('977 KiB')
    expect(bytesCell.attributes('title')).toBe('1,000,000 B')
  })

  it('shows the target, link kind, proof method and decision of a replacement', async () => {
    const { wrapper } = await mountView()

    const gotText = rowOf(wrapper, 21).text()

    expect(gotText).toContain('/data/torrents/Film.mkv')
    expect(gotText).toContain('/data/library/Film.mkv')
    expect(gotText).toContain('lien physique')
    expect(gotText).toContain('échantillonnage')
  })

  describe('non applicable rows', () => {
    it('lists them with really disabled apply buttons and the reason', async () => {
      const { wrapper } = await mountView()

      const ambiguousText = rowOf(wrapper, 22).text()
      const isolatedText = rowOf(wrapper, 25).text()

      expect(rowApply(wrapper, 22).attributes('disabled')).toBeDefined()
      expect(rowApply(wrapper, 25).attributes('disabled')).toBeDefined()
      expect(rowApply(wrapper, 21).attributes('disabled')).toBeUndefined()
      expect(ambiguousText).toContain('Non applicable')
      expect(ambiguousText).toContain('Plusieurs fichiers possibles')
      expect(ambiguousText).toContain('2 fichiers')
      expect(isolatedText).toContain('Copie isolée : jamais reliée à la bibliothèque')
    })

    it('shows the label of a reason code instead of the code', async () => {
      const { wrapper } = await mountView()

      const gotText = rowOf(wrapper, 25).text()

      expect(gotText).not.toContain('isolated_copy')
    })

    it('shows an unknown reason code as it is', async () => {
      const { wrapper } = await mountView([
        maintenanceProposal(26, { ...dedupeDetails, actionable: false, reason: 'future_code' }),
      ])

      expect(rowOf(wrapper, 26).text()).toContain('future_code')
    })

    it('lists the ambiguous files in the row detail', async () => {
      const { wrapper } = await mountView()

      expect(rowOf(wrapper, 22).text()).toContain('/data/library/B.mkv')
    })

    it('never sends an apply for a disabled row', async () => {
      const { wrapper, calls } = await mountView()

      await rowApply(wrapper, 22).trigger('click')
      await flushPromises()

      expect(posts(calls)).toHaveLength(0)
    })
  })

  describe('warnings', () => {
    const withWarnings = maintenanceProposal(30, {
      ...dedupeDetails,
      warnings: ['warning.dedupe.unreliable_inode', 'warning.no_client_recheck'],
    })

    it('renders the stable keys of the plan in the active language', async () => {
      const { wrapper } = await mountView([
        {
          ...withWarnings,
          warnings: [
            { code: 'warning.dedupe.unreliable_inode', text: 'texte du serveur' },
            { code: 'warning.no_client_recheck', text: 'texte du serveur 2' },
          ],
        },
      ])

      const gotText = rowOf(wrapper, 30).text()

      expect(gotText).toContain('inode non fiable')
      expect(gotText).toContain('aucun recontrôle du client')
      expect(gotText).not.toContain('warning.dedupe')
      expect(gotText).toContain('2 avertissements')
    })

    it('follows the interface language for a known key', async () => {
      setLanguage('en')
      const { wrapper } = await mountView([
        {
          ...withWarnings,
          warnings: [{ code: 'warning.dedupe.unreliable_inode', text: 'inode non fiable' }],
        },
      ])

      expect(rowOf(wrapper, 30).text()).toContain('unreliable inode')
    })

    it('shows the server text of a legacy or unknown code as it is', async () => {
      const { wrapper } = await mountView([
        {
          ...withWarnings,
          warnings: [{ code: 'legacy.text', text: 'ancienne phrase anglaise' }],
        },
      ])

      expect(rowOf(wrapper, 30).text()).toContain('ancienne phrase anglaise')
    })

    it('reads a plan key missing from the proposal warnings and the short legacy key', async () => {
      const { wrapper } = await mountView([
        {
          ...withWarnings,
          warnings: [],
          maintenance: {
            ...dedupeDetails,
            warnings: ['no_client_recheck', 'phrase brute d un ancien plan'],
          },
        },
      ])

      const gotText = rowOf(wrapper, 30).text()

      expect(gotText).toContain('aucun recontrôle du client')
      expect(gotText).toContain('phrase brute d un ancien plan')
    })

    it('shows a dash when a row has no warning', async () => {
      const { wrapper } = await mountView()

      expect(rowOf(wrapper, 21).text()).not.toContain('avertissement')
    })
  })

  describe('dry run', () => {
    it('keeps dry run checked by default', async () => {
      const { wrapper } = await mountView()

      expect(dryRunBox(wrapper).element.checked).toBe(true)
      expect(wrapper.text()).toContain('Simulation (aucun fichier modifié)')
    })

    it('sends a row apply as a dry run, with no confirmation, while the box is checked', async () => {
      const { wrapper, calls } = await mountView()

      await rowApply(wrapper, 21).trigger('click')
      await flushPromises()

      expect(document.body.querySelector('[role="dialog"]')).toBeNull()
      expect(posts(calls).map((call) => [call.path, call.body])).toEqual([
        ['/api/dedupe/21/apply', { dry_run: true, accept_pending: true }],
      ])
    })

    it('names the row action after the mode: simulate while checked, apply once unchecked', async () => {
      const { wrapper } = await mountView()

      const dryLabel = rowApply(wrapper, 23).attributes('aria-label')
      await dryRunBox(wrapper).setValue(false)
      const realLabel = rowApply(wrapper, 23).attributes('aria-label')

      expect(dryLabel).toContain('Simuler')
      expect(realLabel).toContain('Appliquer')
      expect(rowApply(wrapper, 21).attributes('aria-label')).toContain('Accepter et appliquer')
    })
  })

  describe('real apply of one row', () => {
    it('requires confirmation before a real apply and sends nothing before it', async () => {
      const { wrapper, calls } = await mountView()
      await dryRunBox(wrapper).setValue(false)

      await rowApply(wrapper, 21).trigger('click')
      await flushPromises()

      expect(posts(calls)).toHaveLength(0)
      expect(dialogText()).toContain('/data/torrents/Film.mkv')
      expect(dialogButton('confirm-dialog-confirm')?.disabled).toBe(true)
    })

    it('keeps the confirm button disabled until the exact phrase is typed', async () => {
      const { wrapper, calls } = await mountView()
      await dryRunBox(wrapper).setValue(false)
      await rowApply(wrapper, 21).trigger('click')
      await flushPromises()

      await typePhrase('appliquer')
      await confirmDialog()
      const sentWithWrongCase = posts(calls).length
      await typePhrase('APPLIQUER ')
      await confirmDialog()
      const sentWithTrailingSpace = posts(calls).length
      await typePhrase('APPLIQUER')
      const gotDisabled = dialogButton('confirm-dialog-confirm')?.disabled

      expect(sentWithWrongCase).toBe(0)
      expect(sentWithTrailingSpace).toBe(0)
      expect(gotDisabled).toBe(false)
    })

    it('sends the real apply once the phrase is typed, accepting a pending proposal', async () => {
      const { wrapper, calls } = await mountView()
      await dryRunBox(wrapper).setValue(false)
      await rowApply(wrapper, 21).trigger('click')
      await flushPromises()

      await typePhrase('APPLIQUER')
      await confirmDialog()

      expect(posts(calls).map((call) => [call.path, call.body])).toEqual([
        ['/api/dedupe/21/apply', { dry_run: false, accept_pending: true }],
      ])
    })

    it('does not accept again an already accepted proposal', async () => {
      const { wrapper, calls } = await mountView()
      await dryRunBox(wrapper).setValue(false)
      await rowApply(wrapper, 23).trigger('click')
      await flushPromises()

      await typePhrase('APPLIQUER')
      await confirmDialog()

      expect(posts(calls).map((call) => call.body)).toEqual([
        { dry_run: false, accept_pending: false },
      ])
    })

    it('asks for the English word in English', async () => {
      setLanguage('en')
      const { wrapper, calls } = await mountView()
      await dryRunBox(wrapper).setValue(false)
      await rowApply(wrapper, 21).trigger('click')
      await flushPromises()

      await typePhrase('APPLIQUER')
      await confirmDialog()
      const sentWithTheFrenchWord = posts(calls).length
      await typePhrase('APPLY')
      await confirmDialog()

      expect(sentWithTheFrenchWord).toBe(0)
      expect(posts(calls).map((call) => call.body)).toEqual([
        { dry_run: false, accept_pending: true },
      ])
    })

    it('sends nothing when the dialog is cancelled or closed with Escape', async () => {
      const { wrapper, calls } = await mountView()
      await dryRunBox(wrapper).setValue(false)

      await rowApply(wrapper, 21).trigger('click')
      await flushPromises()
      dialogButton('confirm-dialog-cancel')?.click()
      await flushPromises()
      await rowApply(wrapper, 21).trigger('click')
      await flushPromises()
      await pressEscape()

      expect(posts(calls)).toHaveLength(0)
      expect(document.body.querySelector('[role="dialog"]')).toBeNull()
    })

    it('forgets a typed phrase when the dialog is reopened', async () => {
      const { wrapper, calls } = await mountView()
      await dryRunBox(wrapper).setValue(false)
      await rowApply(wrapper, 21).trigger('click')
      await flushPromises()
      await typePhrase('APPLIQUER')
      await pressEscape()

      await rowApply(wrapper, 21).trigger('click')
      await flushPromises()
      await confirmDialog()

      expect(posts(calls)).toHaveLength(0)
      expect(dialogButton('confirm-dialog-confirm')?.disabled).toBe(true)
    })
  })

  describe('apply accepted', () => {
    it('is a toolbar action, disabled when no proposal is accepted', async () => {
      const { wrapper } = await mountView([pendingRow])

      const apply = wrapper.get('[data-testid="apply-all"]')

      expect(apply.attributes('data-scope')).toBe('toolbar')
      expect(apply.attributes('data-action')).toBe('dedupe apply')
      expect(apply.attributes('disabled')).toBeDefined()
    })

    it('lists the accepted proposals in a dialog before anything is sent, even in dry run', async () => {
      const { wrapper, calls } = await mountView(twoAccepted)

      await wrapper.get('[data-testid="apply-all"]').trigger('click')
      await flushPromises()

      expect(dialogText()).toContain('/data/torrents/Film.mkv')
      expect(dialogText()).toContain('/data/torrents/Autre.mkv')
      expect(dialogText()).toContain('Simulation')
      expect(posts(calls)).toHaveLength(0)
    })

    it('needs no phrase in dry run and chains accepted proposals as dry runs, accept pending off', async () => {
      const state = { finished: false }
      const { wrapper, calls } = await mountView(twoAccepted, state)
      await wrapper.get('[data-testid="apply-all"]').trigger('click')
      await flushPromises()

      expect(document.body.querySelector('[data-testid="confirm-dialog-phrase"]')).toBeNull()
      await confirmDialog()
      state.finished = true
      await vi.advanceTimersByTimeAsync(2000)
      await flushPromises()

      expect(posts(calls).map((call) => [call.path, call.body])).toEqual([
        ['/api/dedupe/23/apply', { dry_run: true, accept_pending: false }],
        ['/api/dedupe/24/apply', { dry_run: true, accept_pending: false }],
      ])
    })

    it('requires confirmation before a real apply of all accepted proposals', async () => {
      const { wrapper, calls } = await mountView(twoAccepted)
      await dryRunBox(wrapper).setValue(false)

      await wrapper.get('[data-testid="apply-all"]').trigger('click')
      await flushPromises()
      await confirmDialog()

      expect(posts(calls)).toHaveLength(0)
      expect(dialogText()).toContain('2 remplacements')
      expect(dialogButton('confirm-dialog-confirm')?.disabled).toBe(true)
    })

    it('sends the real chain only after the phrase, one request per proposal', async () => {
      const state = { finished: false }
      const { wrapper, calls } = await mountView(twoAccepted, state)
      await dryRunBox(wrapper).setValue(false)
      await wrapper.get('[data-testid="apply-all"]').trigger('click')
      await flushPromises()

      await typePhrase('APPLIQUER')
      await confirmDialog()
      expect(posts(calls).map((call) => call.body)).toEqual([
        { dry_run: false, accept_pending: false },
      ])
      state.finished = true
      await vi.advanceTimersByTimeAsync(2000)
      await flushPromises()

      expect(posts(calls).map((call) => call.path)).toEqual([
        '/api/dedupe/23/apply',
        '/api/dedupe/24/apply',
      ])
    })

    it('sends nothing when the bulk dialog is cancelled or closed with Escape', async () => {
      const { wrapper, calls } = await mountView(twoAccepted)
      await dryRunBox(wrapper).setValue(false)

      await wrapper.get('[data-testid="apply-all"]').trigger('click')
      await flushPromises()
      dialogButton('confirm-dialog-cancel')?.click()
      await flushPromises()
      await wrapper.get('[data-testid="apply-all"]').trigger('click')
      await flushPromises()
      await typePhrase('APPLIQUER')
      await pressEscape()

      expect(posts(calls)).toHaveLength(0)
    })

    it('passes a summary key to the dialog, never a raw string', async () => {
      const { wrapper } = await mountView(twoAccepted)
      await dryRunBox(wrapper).setValue(false)
      await wrapper.get('[data-testid="apply-all"]').trigger('click')
      await flushPromises()

      const gotSummary =
        document.body.querySelector('[data-testid="confirm-dialog-summary"]')?.textContent ?? ''

      expect(gotSummary).toContain('remplacé par un lien')
      expect(dialogText()).not.toMatch(/dedupe\.[a-zA-Z]/)
    })

    it('captures the dry run flag once at the start of the chain', async () => {
      const state = { finished: false }
      const { wrapper, calls } = await mountView(twoAccepted, state)
      await wrapper.get('[data-testid="apply-all"]').trigger('click')
      await flushPromises()
      await confirmDialog()

      await dryRunBox(wrapper).setValue(false)
      state.finished = true
      await vi.advanceTimersByTimeAsync(2000)
      await flushPromises()

      expect(posts(calls).map((call) => call.body)).toEqual([
        { dry_run: true, accept_pending: false },
        { dry_run: true, accept_pending: false },
      ])
    })

    it('keeps the mode shown in the dialog even if the box changes while it is open', async () => {
      const { wrapper, calls } = await mountView(twoAccepted)
      await wrapper.get('[data-testid="apply-all"]').trigger('click')
      await flushPromises()

      await dryRunBox(wrapper).setValue(false)
      await confirmDialog()

      expect(posts(calls).map((call) => call.body)).toEqual([
        { dry_run: true, accept_pending: false },
      ])
    })

    it('stops the chain when the view is unmounted', async () => {
      const state = { finished: false }
      const { wrapper, calls } = await mountView(twoAccepted, state)
      await wrapper.get('[data-testid="apply-all"]').trigger('click')
      await flushPromises()
      await confirmDialog()

      wrapper.unmount()
      state.finished = true
      await vi.advanceTimersByTimeAsync(10_000)
      await flushPromises()

      expect(posts(calls)).toHaveLength(1)
    })
  })

  describe('toolbar and reject', () => {
    it('runs the plan as a job from a toolbar button', async () => {
      const { wrapper, calls } = await mountView()

      const plan = wrapper.get('button[data-action="dedupe plan"]')
      await plan.trigger('click')
      await flushPromises()

      expect(plan.attributes('data-scope')).toBe('toolbar')
      expect(calls.at(-1)).toMatchObject({ method: 'POST', path: '/api/dedupe/plan', body: {} })
    })

    it('puts row triggers in the row scope', async () => {
      const { wrapper } = await mountView()

      const scopes = rowOf(wrapper, 21)
        .findAll('[data-action]')
        .map((button) => [button.attributes('data-action'), button.attributes('data-scope')])

      expect(scopes).toEqual([
        ['dedupe apply', 'row'],
        ['proposals reject', 'row'],
      ])
    })

    it('rejects a pending proposal from its row and only then', async () => {
      const { wrapper, calls } = await mountView()
      const rejectButton = (id: number) =>
        rowOf(wrapper, id).get('button[data-action="proposals reject"]')

      await rejectButton(21).trigger('click')
      await flushPromises()

      expect(rejectButton(23).attributes('disabled')).toBeDefined()
      expect(posts(calls).map((call) => call.path)).toEqual(['/api/proposals/21/reject'])
      expect(toastSuccess).toHaveBeenCalled()
    })

    it('shows an error state when the list cannot be loaded', async () => {
      installFakeApi({
        'GET /api/proposals': failure(500, 'internal'),
        'GET /api/jobs': () => [],
      })
      const wrapper = mount(DedupeView, { attachTo: document.body })
      await flushPromises()

      expect(wrapper.get('[role="alert"]').text()).toContain('Impossible de charger')
    })

    it('shows an explicit empty state', async () => {
      const { wrapper } = await mountView([])

      expect(wrapper.get('[data-testid="table-empty"]').text()).toContain(
        'Aucun remplacement proposé',
      )
    })
  })

  describe('narrow screens', () => {
    it('renders cards with the same actions and the bytes to read', async () => {
      stubViewport(360)
      const { wrapper } = await mountView()

      const card = wrapper.get('[data-testid="table-card"][data-row-key="21"]')

      expect(card.get('button[data-action="dedupe apply"]').attributes('data-scope')).toBe('row')
      expect(card.text().replace(/\s/g, ' ')).toContain('977 Kio')
    })
  })
})
