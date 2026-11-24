import { mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick, ref } from 'vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import DataTable from '@/components/DataTable.vue'
import StatusPill from '@/components/StatusPill.vue'
import { resetAppState } from '@/test/fakeApi'
import {
  fixtureColumns,
  fixtureRowKey,
  fixtureRows,
  type FixtureRow,
} from '@/test/dataTableFixture'
import { stubViewport } from '@/test/viewport'

const OpenerHost = defineComponent({
  components: { ConfirmDialog },
  setup: () => ({ open: ref(false) }),
  template: `<div><button data-testid="opener" @click="open = true">go</button>
    <ConfirmDialog v-model:open="open" title="t" description="d" confirm-label="ok" summary-key="nav.liens" /></div>`,
})

const mounted: VueWrapper[] = []
function mountTable(props: Record<string, unknown> = {}, slots: Record<string, unknown> = {}) {
  stubViewport(1280)
  const wrapper = mount(DataTable<FixtureRow>, {
    props: {
      captionKey: 'nav.liens',
      columns: fixtureColumns,
      rows: fixtureRows,
      rowKey: fixtureRowKey,
      emptyKey: 'nav.etapes',
      ...props,
    },
    slots,
    attachTo: document.body,
  })
  mounted.push(wrapper as VueWrapper)
  return wrapper
}

function bodyNames(wrapper: VueWrapper): string[] {
  return wrapper.findAll('tbody tr').map((row) => row.findAll('td')[0]!.text())
}

describe('DataTable accessibility', () => {
  beforeEach(resetAppState)
  afterEach(() => {
    mounted.splice(0).forEach((wrapper) => wrapper.unmount())
    vi.unstubAllGlobals()
    document.body.innerHTML = ''
  })

  it('sets aria-sort and caption on every data table', async () => {
    const wrapper = mountTable()

    expect(wrapper.get('table caption').text()).toBe('Liens')
    const headers = wrapper.findAll('th')
    expect(headers.every((header) => header.attributes('scope') === 'col')).toBe(true)
    expect(headers.map((header) => header.attributes('aria-sort'))).toEqual([
      'none',
      'none',
      undefined,
    ])
    expect(headers[2]!.find('button').exists()).toBe(false)

    await headers[0]!.get('button').trigger('click')
    expect(wrapper.findAll('th')[0]!.attributes('aria-sort')).toBe('ascending')
    expect(bodyNames(wrapper)).toEqual(['Alice', 'Bob', 'Charlie'])

    await wrapper.findAll('th')[0]!.get('button').trigger('click')
    expect(wrapper.findAll('th')[0]!.attributes('aria-sort')).toBe('descending')
    expect(bodyNames(wrapper)).toEqual(['Charlie', 'Bob', 'Alice'])
  })

  it('sorts numbers numerically through tanstack', async () => {
    const wrapper = mountTable()

    await wrapper.findAll('th')[1]!.get('button').trigger('click')

    expect(wrapper.findAll('tbody tr').map((row) => row.findAll('td')[1]!.text())).toEqual([
      '1',
      '3',
      '20',
    ])
  })

  it('hides the caption visually when captionHidden is set but keeps it in the tree', () => {
    const wrapper = mountTable({ captionHidden: true })

    expect(wrapper.get('caption').classes()).toContain('sr-only')
  })

  it('applies the initial sort', () => {
    const wrapper = mountTable({ initialSort: { id: 'count', desc: true } })

    expect(wrapper.findAll('th')[1]!.attributes('aria-sort')).toBe('descending')
    expect(bodyNames(wrapper)).toEqual(['Alice', 'Charlie', 'Bob'])
  })

  it('moves row focus with arrow keys and restores focus after dialog', async () => {
    const wrapper = mountTable({}, {})
    const rows = wrapper.findAll('tbody tr')
    expect(rows[0]!.attributes('tabindex')).toBe('0')
    expect(rows[1]!.attributes('tabindex')).toBe('-1')

    await rows[0]!.trigger('keydown', { key: 'ArrowDown' })
    expect(document.activeElement).toBe(rows[1]!.element)
    expect(wrapper.findAll('tbody tr')[1]!.attributes('tabindex')).toBe('0')
    await rows[1]!.trigger('keydown', { key: 'ArrowUp' })
    expect(document.activeElement).toBe(rows[0]!.element)
    await rows[0]!.trigger('keydown', { key: 'ArrowUp' })
    expect(document.activeElement).toBe(rows[0]!.element)

    await rows[1]!.trigger('keydown', { key: 'Enter' })
    expect(wrapper.emitted('open')?.[0]?.[0]).toEqual(fixtureRows[1])

    const host = mount(OpenerHost, { attachTo: document.body })
    const opener = host.get('[data-testid="opener"]')
    ;(opener.element as HTMLButtonElement).focus()
    await opener.trigger('click')
    await nextTick()
    expect(document.body.querySelector('[role="dialog"]')).not.toBeNull()

    document.body.querySelector<HTMLButtonElement>('[data-testid="confirm-dialog-cancel"]')!.click()
    await new Promise((resolve) => setTimeout(resolve, 20))

    expect(document.activeElement).toBe(opener.element)
    host.unmount()
  })

  it('restores focus to the opener when the dialog closes with Escape', async () => {
    const host = mount(OpenerHost, { attachTo: document.body })
    const opener = host.get('[data-testid="opener"]')
    ;(opener.element as HTMLButtonElement).focus()
    await opener.trigger('click')
    await nextTick()

    document.body
      .querySelector('[role="dialog"]')!
      .dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await new Promise((resolve) => setTimeout(resolve, 20))

    expect(document.activeElement).toBe(opener.element)
    host.unmount()
  })

  it('does not open a row when Enter comes from a button inside it', async () => {
    const wrapper = mountTable(
      {},
      {
        'row-actions': (scope: { row: FixtureRow }) =>
          h('button', { 'data-action': 'x' }, scope.row.id),
      },
    )

    await wrapper.get('tbody tr button').trigger('keydown', { key: 'Enter' })

    expect(wrapper.emitted('open')).toBeUndefined()
  })

  it('renders row actions as keyboard-reachable buttons with a labelled header', () => {
    const wrapper = mountTable(
      {},
      {
        'row-actions': (scope: { row: FixtureRow }) =>
          h('button', { 'data-action': 'x' }, scope.row.id),
      },
    )

    expect(
      wrapper.findAll('tbody tr').map((row) => row.get('button').attributes('data-action')),
    ).toEqual(['x', 'x', 'x'])
    expect(wrapper.findAll('th').at(-1)!.get('.sr-only').text()).toBe('Actions')
  })

  it('shows the empty state in a live region', () => {
    const wrapper = mountTable({ rows: [] })

    expect(wrapper.get('[data-testid="table-empty"]').text()).toBe('Étapes')
    expect(wrapper.get('[data-testid="table-status"]').attributes('aria-live')).toBe('polite')
  })

  it('marks the table busy while loading', () => {
    const wrapper = mountTable({ loading: true })

    expect(wrapper.get('[data-testid="data-table"]').attributes('aria-busy')).toBe('true')
    expect(wrapper.get('[data-testid="table-status"]').text()).toBe('Chargement…')
    expect(wrapper.find('[data-testid="table-empty"]').exists()).toBe(false)
  })

  it('shows the error as an alert instead of rows', () => {
    const wrapper = mountTable({ error: 'boom du serveur' })

    expect(wrapper.get('[role="alert"]').text()).toBe('boom du serveur')
    expect(wrapper.findAll('tbody tr')).toHaveLength(0)
  })

  it('paginates on the client with aria-rowindex and page buttons', async () => {
    const rows = Array.from({ length: 5 }, (_, index) => ({
      id: `r${index}`,
      name: `N${index}`,
      count: index,
    }))
    const wrapper = mountTable({ rows, pageSize: 2 })

    expect(wrapper.findAll('tbody tr').map((row) => row.attributes('aria-rowindex'))).toEqual([
      '2',
      '3',
    ])
    expect(wrapper.get('[data-testid="table-page-status"]').text()).toBe('1 à 2 sur 5')
    await wrapper.get('[data-testid="table-next"]').trigger('click')
    expect(wrapper.findAll('tbody tr').map((row) => row.attributes('aria-rowindex'))).toEqual([
      '4',
      '5',
    ])
    expect(wrapper.get('[data-testid="table-page-status"]').text()).toBe('3 à 4 sur 5')
    await wrapper.get('[data-testid="table-previous"]').trigger('click')
    expect(bodyNames(wrapper)).toEqual(['N0', 'N1'])
    expect(wrapper.get('[data-testid="table-previous"]').attributes('disabled')).toBeDefined()
  })

  it('asks the parent for the next server page instead of slicing', async () => {
    const onPage = vi.fn()
    const wrapper = mountTable({
      pageSize: 3,
      serverPage: { total: 7, offset: 3, onPage },
    })

    expect(wrapper.findAll('tbody tr').map((row) => row.attributes('aria-rowindex'))).toEqual([
      '5',
      '6',
      '7',
    ])
    expect(wrapper.get('[data-testid="table-page-status"]').text()).toBe('4 à 6 sur 7')
    await wrapper.get('[data-testid="table-next"]').trigger('click')
    await wrapper.get('[data-testid="table-previous"]').trigger('click')

    expect(onPage.mock.calls).toEqual([[6], [0]])
  })

  it('offers checkboxes only on selectable rows, labelled, with no select-all', async () => {
    const wrapper = mountTable({ selectable: (row: FixtureRow) => row.id !== 'b' })

    const boxes = wrapper.findAll('tbody input[type="checkbox"]')
    expect(boxes).toHaveLength(2)
    expect(boxes.map((box) => box.attributes('aria-label'))).toEqual([
      'Sélectionner la ligne 1',
      'Sélectionner la ligne 3',
    ])
    expect(wrapper.find('thead input[type="checkbox"]').exists()).toBe(false)

    await boxes[0]!.setValue(true)
    await boxes[1]!.setValue(true)
    await boxes[0]!.setValue(false)

    expect(wrapper.emitted('select')).toEqual([[['a']], [['a', 'c']], [['c']]])
  })

  it('has no checkbox without the selectable prop', () => {
    expect(mountTable().find('input[type="checkbox"]').exists()).toBe(false)
  })
})

describe('DataTable server mode', () => {
  beforeEach(resetAppState)
  afterEach(() => {
    mounted.splice(0).forEach((wrapper) => wrapper.unmount())
    vi.unstubAllGlobals()
    document.body.innerHTML = ''
  })

  it('turns sorting off without onSort', () => {
    const wrapper = mountTable({ serverPage: { total: 3, offset: 0, onPage: vi.fn() } })

    expect(wrapper.find('th button').exists()).toBe(false)
    expect(wrapper.findAll('th').some((header) => header.attributes('aria-sort'))).toBe(false)
  })

  it('emits onSort and keeps the server order when onSort is given', async () => {
    const onSort = vi.fn()
    const wrapper = mountTable({ serverPage: { total: 3, offset: 0, onPage: vi.fn(), onSort } })

    await wrapper.findAll('th')[0]!.get('button').trigger('click')

    expect(onSort.mock.calls).toEqual([[{ id: 'name', desc: false }]])
    expect(bodyNames(wrapper)).toEqual(['Charlie', 'Alice', 'Bob'])
    expect(wrapper.findAll('th')[0]!.attributes('aria-sort')).toBe('ascending')
  })
})

describe('DataTable focus and selection robustness', () => {
  beforeEach(resetAppState)
  afterEach(() => {
    mounted.splice(0).forEach((wrapper) => wrapper.unmount())
    vi.unstubAllGlobals()
    document.body.innerHTML = ''
  })

  it('keeps one tabbable row when the focused row disappears', async () => {
    const wrapper = mountTable()
    await wrapper.findAll('tbody tr')[0]!.trigger('keydown', { key: 'ArrowDown' })
    expect(wrapper.findAll('tbody tr')[1]!.attributes('tabindex')).toBe('0')

    await wrapper.setProps({ rows: [fixtureRows[0]!, fixtureRows[2]!] })

    expect(wrapper.findAll('tbody tr').map((row) => row.attributes('tabindex'))).toEqual([
      '0',
      '-1',
    ])
  })

  it('reflects selection in the checkbox state and prunes keys that left the page', async () => {
    const wrapper = mountTable({ selectable: () => true })
    await wrapper.findAll('tbody input')[0]!.setValue(true)
    await wrapper.findAll('tbody input')[1]!.setValue(true)
    expect((wrapper.findAll('tbody input')[0]!.element as HTMLInputElement).checked).toBe(true)

    await wrapper.setProps({ rows: [fixtureRows[1]!, fixtureRows[2]!] })

    expect(wrapper.emitted('select')?.at(-1)).toEqual([['b']])
    const gotChecked = wrapper
      .findAll('tbody input')
      .map((box) => (box.element as HTMLInputElement).checked)
    expect(gotChecked).toEqual([true, false])
  })

  it('drops the selection when paging away', async () => {
    const rows = Array.from({ length: 4 }, (_, index) => ({
      id: `r${index}`,
      name: `N${index}`,
      count: index,
    }))
    const wrapper = mountTable({ rows, pageSize: 2, selectable: () => true })
    await wrapper.findAll('tbody input')[0]!.setValue(true)

    await wrapper.get('[data-testid="table-next"]').trigger('click')

    expect(wrapper.emitted('select')?.at(-1)).toEqual([[]])
    expect(
      wrapper.findAll('tbody input').every((box) => !(box.element as HTMLInputElement).checked),
    ).toBe(true)
  })

  it('numbers checkbox labels from the page offset', () => {
    const wrapper = mountTable({
      selectable: () => true,
      serverPage: { total: 9, offset: 6, onPage: vi.fn() },
    })

    expect(wrapper.findAll('tbody input').map((box) => box.attributes('aria-label'))).toEqual([
      'Sélectionner la ligne 7',
      'Sélectionner la ligne 8',
      'Sélectionner la ligne 9',
    ])
  })

  it('moves focus to the page status when the next button becomes disabled', async () => {
    const rows = Array.from({ length: 3 }, (_, index) => ({
      id: `r${index}`,
      name: `N${index}`,
      count: index,
    }))
    const wrapper = mountTable({ rows, pageSize: 2 })
    const next = wrapper.get('[data-testid="table-next"]')
    ;(next.element as HTMLButtonElement).focus()

    await next.trigger('click')
    await nextTick()

    expect(document.activeElement).toBe(wrapper.get('[data-testid="table-page-status"]').element)
  })
})

describe('StatusPill', () => {
  beforeEach(resetAppState)

  it('always renders the text next to a decorative icon', () => {
    const tones = ['ok', 'warn', 'fail', 'info', 'muted'] as const
    for (const tone of tones) {
      const wrapper = mount(StatusPill, { props: { tone, labelKey: 'nav.liens' } })

      expect(wrapper.text()).toBe('Liens')
      expect(wrapper.attributes('data-tone')).toBe(tone)
      expect(wrapper.get('svg').attributes('aria-hidden')).toBe('true')
    }
  })
})

describe('ConfirmDialog content', () => {
  beforeEach(resetAppState)
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('lists the affected items under the summary', async () => {
    mount(ConfirmDialog, {
      props: {
        open: true,
        title: 't',
        description: 'd',
        confirmLabel: 'ok',
        summaryKey: 'nav.liens',
        items: ['un', 'deux'],
      },
      attachTo: document.body,
    })
    await nextTick()

    expect(document.body.querySelector('[data-testid="confirm-dialog-summary"]')?.textContent).toBe(
      'Liens',
    )
    expect(
      Array.from(document.body.querySelectorAll('[data-testid="confirm-dialog-items"] li')).map(
        (item) => item.textContent,
      ),
    ).toEqual(['un', 'deux'])
  })

  it('keeps the confirm button disabled until the phrase is typed', async () => {
    const confirmed = vi.fn()
    mount(ConfirmDialog, {
      props: {
        open: true,
        title: 't',
        description: 'd',
        confirmLabel: 'ok',
        summaryKey: 'nav.liens',
        confirmPhrase: 'FORCER',
        onConfirm: confirmed,
      },
      attachTo: document.body,
    })
    await nextTick()
    const confirm = document.body.querySelector<HTMLButtonElement>(
      '[data-testid="confirm-dialog-confirm"]',
    )!
    const field = document.body.querySelector<HTMLInputElement>(
      '[data-testid="confirm-dialog-phrase"]',
    )!

    expect(confirm.disabled).toBe(true)
    field.value = 'forcer'
    field.dispatchEvent(new Event('input'))
    await nextTick()
    expect(confirm.disabled).toBe(true)
    field.value = 'FORCER'
    field.dispatchEvent(new Event('input'))
    await nextTick()
    expect(confirm.disabled).toBe(false)
    confirm.click()
    expect(confirmed).toHaveBeenCalledOnce()
  })

  it('shows the caption above the cards on narrow screens unless it is hidden', () => {
    const wrapper = mountTable()
    stubViewport(360)
    const narrow = mount(DataTable<FixtureRow>, {
      props: {
        captionKey: 'nav.liens',
        columns: fixtureColumns,
        rows: fixtureRows,
        rowKey: fixtureRowKey,
        emptyKey: 'nav.etapes',
      },
    })
    const hidden = mount(DataTable<FixtureRow>, {
      props: {
        captionKey: 'nav.liens',
        captionHidden: true,
        columns: fixtureColumns,
        rows: fixtureRows,
        rowKey: fixtureRowKey,
        emptyKey: 'nav.etapes',
      },
    })

    expect(narrow.find('[data-testid="table-card"]').exists()).toBe(true)
    expect(narrow.get('[data-testid="table-caption"]').text()).toBe('Liens')
    expect(hidden.find('[data-testid="table-caption"]').exists()).toBe(false)
    wrapper.unmount()
    narrow.unmount()
    hidden.unmount()
  })

  it('applies the column className to the header and body cells', () => {
    const columns = fixtureColumns.map((column, position) =>
      position === 0 ? { ...column, meta: { className: 'w-1/4' } } : column,
    )
    const wrapper = mountTable({ columns })

    expect(wrapper.findAll('th')[0]!.classes()).toContain('w-1/4')
    expect(wrapper.findAll('tbody tr')[0]!.findAll('td')[0]!.classes()).toContain('w-1/4')
    expect(wrapper.findAll('th')[1]!.classes()).not.toContain('w-1/4')
  })
})
