import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setLanguage } from '@/i18n'
import { failure, installFakeApi, resetAppState, type FakeCall } from '@/test/fakeApi'
import { job, linkItem, linkPage, maintenanceProposal, repairDetails } from '@/test/fixtures'
import { stubViewport } from '@/test/viewport'
import LinksView from './LinksView.vue'

const toastError = vi.fn()
vi.mock('vue-sonner', () => ({
  toast: { error: (message: string) => toastError(message), success: vi.fn() },
}))

const LINKS_PAGE_SIZE = 200

const recorded = [
  linkItem(1),
  linkItem(2, {
    kind: 'hardlink',
    state: 'diverged',
    reason: 'link_inode_diverged',
    label: 'inode changed',
  }),
  linkItem(3, {
    state: 'lost',
    reason: 'link_target_missing',
    label: 'the link target is missing',
  }),
  linkItem(4, {
    state: 'unreadable',
    reason: 'link_inspection_failed',
    label: 'inspection failed',
  }),
]

const repairable = {
  ...maintenanceProposal(31, repairDetails),
  kind: 'repair' as const,
  warnings: [{ code: 'warning.no_client_recheck', text: 'raw server text' }],
}
const ambiguous = {
  ...maintenanceProposal(32, {
    ...repairDetails,
    link_path: '/data/library/Ambigu.mkv',
    status: 'ambiguous',
    method: undefined,
    new_target: undefined,
    actionable: false,
    candidates: ['/data/a/Film.mkv', '/data/b/Film.mkv'],
  }),
  kind: 'repair' as const,
}
const lost = {
  ...maintenanceProposal(33, {
    ...repairDetails,
    link_path: '/data/library/Perdu.mkv',
    status: 'lost',
    method: undefined,
    new_target: undefined,
    actionable: false,
  }),
  kind: 'repair' as const,
}
const needsRecheck = {
  ...maintenanceProposal(34, {
    ...repairDetails,
    link_path: '/data/library/Heuristique.mkv',
    method: 'size-mtime',
    actionable: false,
    reason: 'needs_client_recheck' as const,
  }),
  kind: 'repair' as const,
}
const acceptedRepair = {
  ...maintenanceProposal(
    35,
    { ...repairDetails, link_path: '/data/library/Acceptee.mkv' },
    'accepted',
  ),
  kind: 'repair' as const,
}
const ALL_REPAIRS = [repairable, ambiguous, lost, needsRecheck, acceptedRepair]

const mounted: VueWrapper[] = []

interface MountOptions {
  links?: (call: FakeCall) => unknown
  repairs?: unknown[]
}

async function mountView({ links, repairs = ALL_REPAIRS }: MountOptions = {}) {
  const calls = installFakeApi({
    'GET /api/proposals': repairs,
    'GET /api/jobs': () => [job({ id: 3, action: 'repair apply', state: 'running' })],
    'GET /api/links': links ?? (() => linkPage(recorded)),
    'POST /api/repair/scan': () => job({ id: 2, action: 'repair scan' }),
    'POST /api/repair/{id}/apply': () => job({ id: 3, action: 'repair apply' }),
  })
  const wrapper = mount(LinksView, { attachTo: document.body })
  mounted.push(wrapper)
  await flushPromises()
  return { wrapper, calls }
}

const posts = (calls: FakeCall[]) => calls.filter((call) => call.method === 'POST')
const linkRequests = (calls: FakeCall[]) => calls.filter((call) => call.path === '/api/links')
const rowOf = (wrapper: VueWrapper, rowKey: number) => wrapper.get(`tr[data-row-key="${rowKey}"]`)
const dryRunBox = (wrapper: VueWrapper) => wrapper.get<HTMLInputElement>('[data-testid="dry-run"]')
const dialogButton = (testId: string) =>
  document.body.querySelector<HTMLButtonElement>(`[data-testid="${testId}"]`)

async function openRepairsTab(wrapper: VueWrapper): Promise<void> {
  await wrapper.get('[data-testid="tab-repairs"]').trigger('mousedown', { button: 0 })
  await flushPromises()
}

async function typePhrase(word: string): Promise<void> {
  const phraseInput = document.body.querySelector<HTMLInputElement>(
    '[data-testid="confirm-dialog-phrase"]',
  )
  phraseInput!.value = word
  phraseInput!.dispatchEvent(new Event('input'))
  await flushPromises()
}

describe('LinksView', () => {
  beforeEach(() => {
    resetAppState()
    stubViewport(1280)
    vi.useFakeTimers()
    toastError.mockClear()
  })
  afterEach(() => {
    for (const wrapper of mounted.splice(0)) wrapper.unmount()
    vi.useRealTimers()
    vi.unstubAllGlobals()
    document.body.innerHTML = ''
  })

  describe('recorded links', () => {
    it('pages links from the server', async () => {
      const { wrapper, calls } = await mountView({
        links: (call) =>
          call.query.get('offset') === String(LINKS_PAGE_SIZE)
            ? linkPage([linkItem(201)], { total: 201 })
            : linkPage(recorded, { total: 201, next: LINKS_PAGE_SIZE }),
      })

      expect(linkRequests(calls)[0]?.query.get('offset')).toBeNull()
      expect(wrapper.get('[data-testid="table-page-status"]').text()).toContain('1 à 4 sur 201')

      await wrapper.get('button[aria-label="Page suivante"]').trigger('click')
      await flushPromises()

      expect(linkRequests(calls).at(-1)?.query.get('offset')).toBe(String(LINKS_PAGE_SIZE))
      expect(wrapper.find('tr[data-row-key="201"]').exists()).toBe(true)
      expect(wrapper.find('tr[data-row-key="1"]').exists()).toBe(false)
    })

    it('has no own page title, the shell owns it', async () => {
      const { wrapper } = await mountView()

      expect(wrapper.find('h2').exists()).toBe(false)
    })

    it('shows the seven columns of the links table', async () => {
      const { wrapper } = await mountView()

      const gotHeaders = wrapper
        .findAll('[data-testid="links-panel"] thead th')
        .map((th) => th.text())

      for (const expectedValue of ['Lien', 'Sorte', 'Cible', 'Volume', 'État', 'Comparé', 'Taille'])
        expect(gotHeaders).toContain(expectedValue)
    })

    it('shows state labels with an icon and the label of a reason, never the raw code', async () => {
      const { wrapper } = await mountView()

      const gotStates = recorded.map((item) => rowOf(wrapper, item.id).get('[data-state]').text())

      expect(gotStates).toEqual(['Conforme', 'Divergent', 'Perdu', 'Illisible'])
      for (const pill of wrapper.findAll('[data-tone][data-state]'))
        expect(pill.find('svg').exists()).toBe(true)
      expect(rowOf(wrapper, 3).text()).toContain('the link target is missing')
      expect(rowOf(wrapper, 3).text()).not.toContain('link_target_missing')
      expect(rowOf(wrapper, 2).text()).toContain('lien physique')
      expect(rowOf(wrapper, 1).text()).toContain('Local')
    })

    it('does not offer a sort, the server order is kept', async () => {
      const { wrapper } = await mountView()

      expect(wrapper.find('[data-testid="links-panel"] thead [aria-sort]').exists()).toBe(false)
      expect(wrapper.find('[data-testid="links-panel"] thead button').exists()).toBe(false)
    })

    it('shows counts per state', async () => {
      const { wrapper } = await mountView({
        links: () => linkPage(recorded, { counts: { ok: 7, diverged: 2, lost: 1, unreadable: 0 } }),
      })

      expect(wrapper.get('[data-testid="count-ok"]').text()).toBe('7')
      expect(wrapper.get('[data-testid="count-unreadable"]').text()).toBe('0')
    })

    it('filters by state from the first page', async () => {
      const { wrapper, calls } = await mountView({
        links: (call) =>
          call.query.get('state') === 'lost' ? linkPage([recorded[2]!]) : linkPage(recorded),
      })

      await wrapper.get('[data-testid="link-state-filter"]').setValue('lost')
      await flushPromises()

      expect(linkRequests(calls).at(-1)?.query.get('state')).toBe('lost')
      expect(linkRequests(calls).at(-1)?.query.get('offset')).toBeNull()
      expect(wrapper.find('tr[data-row-key="1"]').exists()).toBe(false)
      expect(wrapper.find('tr[data-row-key="3"]').exists()).toBe(true)
    })

    it('announces a truncated inspection in a live region', async () => {
      const { wrapper } = await mountView({ links: () => linkPage(recorded, { truncated: true }) })

      const notice = wrapper.get('[data-testid="links-truncated"]')

      expect(notice.text()).toContain('partiels')
      expect(wrapper.get('[data-testid="links-live"]').attributes('aria-live')).toBe('polite')
      expect(wrapper.get('[data-testid="links-live"]').element.contains(notice.element)).toBe(true)
    })

    it('marks uncompared links with a distinct pill', async () => {
      const { wrapper } = await mountView({
        links: () =>
          linkPage([
            linkItem(1),
            linkItem(2, {
              kind: 'reflink',
              compared: false,
              reason: 'link_not_compared',
              label: 'not compared',
            }),
          ]),
      })

      expect(rowOf(wrapper, 1).find('[data-testid="not-compared"]').exists()).toBe(false)
      const pill = rowOf(wrapper, 2).get('[data-testid="not-compared"]')
      expect(pill.text()).toBe('Non comparé')
      expect(pill.find('svg').exists()).toBe(true)
      expect(rowOf(wrapper, 2).get('[data-state]').text()).toBe('Conforme')
    })

    it('shows an empty state, and a filtered one', async () => {
      const { wrapper } = await mountView({
        links: () => linkPage([], { counts: { ok: 0, diverged: 0, lost: 0, unreadable: 0 } }),
      })
      expect(wrapper.get('[data-testid="table-empty"]').text()).toContain('Aucun lien enregistré.')

      await wrapper.get('[data-testid="link-state-filter"]').setValue('lost')
      await flushPromises()

      expect(wrapper.get('[data-testid="table-empty"]').text()).toContain('dans cet état')
    })

    it('shows a busy inspection once without retrying', async () => {
      const { wrapper, calls } = await mountView({ links: () => failure(409, 'inspection_busy') })
      await vi.advanceTimersByTimeAsync(50)
      await flushPromises()

      expect(linkRequests(calls)).toHaveLength(1)
      expect(toastError).toHaveBeenCalledWith(expect.stringContaining('examen des liens'))
      expect(wrapper.get('[data-testid="links-panel"] [role="alert"]').text()).toContain('liens')
    })

    it('ignores a stale response after the filter changed', async () => {
      installFakeApi({
        'GET /api/proposals': [],
        'GET /api/jobs': [],
        'GET /api/links': (call) =>
          call.query.get('state') === 'lost'
            ? linkPage([linkItem(3, { state: 'lost' })])
            : linkPage([linkItem(1)]),
      })
      const answered = globalThis.fetch
      let releaseFirst: () => void = () => undefined
      const firstGate = new Promise<void>((resolve) => (releaseFirst = resolve))
      let requestsSeen = 0
      vi.stubGlobal('fetch', async (input: string, init?: RequestInit) => {
        const isFirstLinksRequest = input.startsWith('/api/links') && requestsSeen++ === 0
        if (isFirstLinksRequest) await firstGate
        return answered(input, init)
      })
      const wrapper = mount(LinksView, { attachTo: document.body })
      mounted.push(wrapper)
      await flushPromises()
      expect(wrapper.get('[data-testid="links-live"]').attributes('aria-busy')).toBe('true')

      await wrapper.get('[data-testid="link-state-filter"]').setValue('lost')
      await flushPromises()
      releaseFirst()
      await flushPromises()

      expect(wrapper.find('tr[data-row-key="3"]').exists()).toBe(true)
      expect(wrapper.find('tr[data-row-key="1"]').exists()).toBe(false)
    })

    it('reloads the links from the first page when Inspect is pressed', async () => {
      const { wrapper, calls } = await mountView()
      const requestsBefore = linkRequests(calls).length

      await wrapper.get('[data-action="links list"][data-scope="toolbar"]').trigger('click')
      await flushPromises()

      expect(linkRequests(calls)).toHaveLength(requestsBefore + 1)
    })

    it('offers a repair search only on broken links', async () => {
      const { wrapper, calls } = await mountView()

      expect(rowOf(wrapper, 1).find('[data-testid="search-repair"]').exists()).toBe(false)
      for (const brokenId of [2, 3, 4])
        expect(rowOf(wrapper, brokenId).find('[data-testid="search-repair"]').exists()).toBe(true)

      await rowOf(wrapper, 3).get('[data-testid="search-repair"]').trigger('click')
      await flushPromises()

      expect(posts(calls).map((call) => [call.path, call.body])).toEqual([
        ['/api/repair/scan', { directory: '' }],
      ])
    })

    it('shows cards below 768 px with the state label and the same repair action', async () => {
      stubViewport(360)
      const { wrapper } = await mountView()

      const cards = wrapper.findAll('[data-testid="links-panel"] [data-testid="table-card"]')

      expect(cards).toHaveLength(recorded.length)
      expect(cards[2]!.text()).toContain('Perdu')
      expect(cards[2]!.find('button[data-action="repair scan"]').exists()).toBe(true)
    })

    it('follows the language and keeps machine codes out of the page', async () => {
      setLanguage('en')
      const { wrapper } = await mountView()

      const gotHeaders = wrapper
        .findAll('[data-testid="links-panel"] thead th')
        .map((th) => th.text())

      expect(gotHeaders).toEqual(
        expect.arrayContaining(['Link', 'Kind', 'Target', 'Volume', 'State']),
      )
      expect(rowOf(wrapper, 1).get('[data-state]').text()).toBe('Compliant')
    })
  })

  describe('toolbar', () => {
    it('scans with the optional directory, trimmed, from a labelled field', async () => {
      const { wrapper, calls } = await mountView()
      const field = wrapper.get('[data-testid="scan-directory"]')

      expect(field.attributes('aria-label')).toBe('Dossier à explorer (facultatif)')
      expect(field.attributes('autocomplete')).toBe('off')
      expect(field.attributes('list')).toBeUndefined()
      expect(field.attributes('value') ?? '').toBe('')
      await field.setValue(' /srv/media/lost ')
      await wrapper.get('[data-testid="repair-scan-form"]').trigger('submit')
      await flushPromises()

      expect(posts(calls).map((call) => [call.path, call.body])).toEqual([
        ['/api/repair/scan', { directory: '/srv/media/lost' }],
      ])
    })

    it('keeps every action of the page with its scope', async () => {
      const { wrapper } = await mountView()

      const gotActions = wrapper
        .findAll('[data-action]')
        .map((button) => `${button.attributes('data-action')}:${button.attributes('data-scope')}`)

      expect(gotActions).toEqual(
        expect.arrayContaining(['links list:toolbar', 'repair scan:toolbar']),
      )
    })
  })

  describe('repairs tab', () => {
    it('shows the repairs tab with applicable state', async () => {
      const { wrapper } = await mountView()
      await openRepairsTab(wrapper)

      const gotHeaders = wrapper
        .findAll('[data-testid="repairs-panel"] thead th')
        .map((th) => th.text())
      for (const expectedValue of ['Lien', 'Nouvelle cible', 'Méthode', 'Applicable', 'Décision'])
        expect(gotHeaders).toContain(expectedValue)
      expect(rowOf(wrapper, 31).text()).toContain('/data/new/Film.mkv')
      expect(rowOf(wrapper, 31).text()).toContain('empreinte du contenu')
      expect(rowOf(wrapper, 31).text()).toContain('En attente')
      expect(rowOf(wrapper, 31).get('[data-testid="applicable"]').text()).toContain('Applicable')
      expect(rowOf(wrapper, 32).get('[data-testid="applicable"]').text()).toContain(
        'Non applicable',
      )
    })

    it('lists ambiguous, lost and recheck-needing rows with really disabled buttons', async () => {
      const { wrapper } = await mountView()
      await openRepairsTab(wrapper)

      const applyButton = (rowKey: number) =>
        rowOf(wrapper, rowKey).get('button[data-action="repair apply"]')

      expect(applyButton(31).attributes('disabled')).toBeUndefined()
      for (const blocked of [32, 33, 34])
        expect(applyButton(blocked).attributes('disabled')).toBeDefined()
      expect(rowOf(wrapper, 32).text()).toContain('Ambigu')
      expect(rowOf(wrapper, 32).text()).toContain('/data/b/Film.mkv')
      expect(rowOf(wrapper, 33).text()).toContain('Introuvable')
      expect(rowOf(wrapper, 34).text()).toContain('sans client torrent')
    })

    it('renders proposal warnings from their code in the active language', async () => {
      const { wrapper } = await mountView()
      await openRepairsTab(wrapper)

      expect(rowOf(wrapper, 31).text()).toContain('aucun recontrôle du client')
      expect(rowOf(wrapper, 31).text()).not.toContain('raw server text')

      setLanguage('en')
      await flushPromises()

      expect(rowOf(wrapper, 31).text()).toContain('no client recheck')
    })

    it('keeps the dry run box checked by default', async () => {
      const { wrapper } = await mountView()

      expect(dryRunBox(wrapper).element.checked).toBe(true)
    })

    it('sends a dry run of a row directly, without confirmation, accepting a pending proposal', async () => {
      const { wrapper, calls } = await mountView()
      await openRepairsTab(wrapper)

      await rowOf(wrapper, 31).get('button[data-action="repair apply"]').trigger('click')
      await flushPromises()

      expect(document.body.querySelector('[role="dialog"]')).toBeNull()
      expect(posts(calls).at(-1)).toMatchObject({
        path: '/api/repair/31/apply',
        body: { dry_run: true, accept_pending: true },
      })
    })

    it('requires the typed word before a real apply and sends nothing before it', async () => {
      const { wrapper, calls } = await mountView()
      await openRepairsTab(wrapper)
      await dryRunBox(wrapper).setValue(false)

      await rowOf(wrapper, 35).get('button[data-action="repair apply"]').trigger('click')
      await flushPromises()

      expect(posts(calls)).toHaveLength(0)
      expect(dialogButton('confirm-dialog-confirm')?.disabled).toBe(true)
      expect(document.body.textContent).toContain('/data/library/Acceptee.mkv')

      await typePhrase('appliquer')
      expect(dialogButton('confirm-dialog-confirm')?.disabled).toBe(true)
      await typePhrase('APPLIQUER')
      dialogButton('confirm-dialog-confirm')?.click()
      await flushPromises()

      expect(posts(calls).at(-1)).toMatchObject({
        path: '/api/repair/35/apply',
        body: { dry_run: false, accept_pending: false },
      })
    })

    it('expects APPLY in English', async () => {
      setLanguage('en')
      const { wrapper } = await mountView()
      await openRepairsTab(wrapper)
      await dryRunBox(wrapper).setValue(false)

      await rowOf(wrapper, 35).get('button[data-action="repair apply"]').trigger('click')
      await flushPromises()

      expect(document.body.querySelector('[role="dialog"]')?.textContent).toContain('APPLY')
    })

    it('sends nothing when the confirmation is cancelled', async () => {
      const { wrapper, calls } = await mountView()
      await openRepairsTab(wrapper)
      await dryRunBox(wrapper).setValue(false)

      await rowOf(wrapper, 35).get('button[data-action="repair apply"]').trigger('click')
      await flushPromises()
      dialogButton('confirm-dialog-cancel')?.click()
      await flushPromises()

      expect(posts(calls)).toHaveLength(0)
    })

    it('shows the repairs as cards below 768 px with a disabled apply on blocked rows', async () => {
      stubViewport(360)
      const { wrapper } = await mountView()
      await openRepairsTab(wrapper)

      const cards = wrapper.findAll('[data-testid="repairs-panel"] [data-testid="table-card"]')

      expect(cards).toHaveLength(ALL_REPAIRS.length)
      expect(
        cards[1]!.get('button[data-action="repair apply"]').attributes('disabled'),
      ).toBeDefined()
    })

    it('shows an empty state and a load failure', async () => {
      const { wrapper } = await mountView({ repairs: [] })
      await openRepairsTab(wrapper)

      expect(
        wrapper.get('[data-testid="repairs-panel"] [data-testid="table-empty"]').text(),
      ).toContain('Aucune réparation')
    })

    it('switches tabs with the arrow keys', async () => {
      const { wrapper } = await mountView()

      await wrapper.get('[data-testid="tab-links"]').trigger('keydown', { key: 'ArrowRight' })
      await flushPromises()

      expect(wrapper.get('[data-testid="tab-repairs"]').attributes('aria-selected')).toBe('true')
    })
  })
})
