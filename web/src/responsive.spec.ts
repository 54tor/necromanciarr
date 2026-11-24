import { mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { h } from 'vue'
import DataTable from '@/components/DataTable.vue'
import { resetAppState } from '@/test/fakeApi'
import {
  fixtureColumns,
  fixtureRowKey,
  fixtureRows,
  type FixtureRow,
} from '@/test/dataTableFixture'
import { stubViewport } from '@/test/viewport'

const mounted: VueWrapper[] = []
function mountAt(width: number, slots: Record<string, unknown> = {}) {
  stubViewport(width)
  const wrapper = mount(DataTable<FixtureRow>, {
    props: {
      captionKey: 'nav.liens',
      columns: fixtureColumns,
      rows: fixtureRows,
      rowKey: fixtureRowKey,
      emptyKey: 'nav.etapes',
    },
    slots: {
      'row-actions': (scope: { row: FixtureRow }) =>
        h('button', { 'data-action': 'open-row', 'data-scope': 'row' }, scope.row.id),
      ...slots,
    },
    attachTo: document.body,
  })
  mounted.push(wrapper as VueWrapper)
  return wrapper
}

function actions(wrapper: VueWrapper): string[] {
  return wrapper
    .findAll('[data-action]')
    .map((button) => `${button.attributes('data-action')}:${button.text()}`)
}

describe('DataTable responsive layout', () => {
  beforeEach(resetAppState)
  afterEach(() => {
    mounted.splice(0).forEach((wrapper) => wrapper.unmount())
    vi.unstubAllGlobals()
    document.body.innerHTML = ''
  })

  it('renders cards below 768px with the same data-action', () => {
    const wide = mountAt(1024)
    const narrow = mountAt(500)

    expect(wide.find('table').exists()).toBe(true)
    expect(narrow.find('table').exists()).toBe(false)
    expect(narrow.findAll('[data-testid="table-card"]')).toHaveLength(3)
    expect(actions(narrow)).toEqual(actions(wide))
    expect(actions(narrow)).toEqual(['open-row:a', 'open-row:b', 'open-row:c'])
  })

  it('labels every field of a card with its column header', () => {
    const narrow = mountAt(500)

    const firstCard = narrow.get('[data-testid="table-card"]')
    expect(firstCard.findAll('dt').map((label) => label.text())).toEqual(['Nom', 'Nombre', 'Fixe'])
    expect(firstCard.findAll('dd').map((value) => value.text())).toEqual(['Charlie', '3', 'fixe-a'])
  })

  it('uses the card slot when given and keeps the caption as the list name', () => {
    const narrow = mountAt(500, {
      card: (scope: { row: FixtureRow }) => h('p', { 'data-testid': 'custom' }, scope.row.name),
    })

    expect(narrow.findAll('[data-testid="custom"]').map((card) => card.text())).toEqual([
      'Charlie',
      'Alice',
      'Bob',
    ])
    expect(narrow.get('ul').attributes('aria-label')).toBe('Liens')
  })

  it('emits open from a card with Enter', async () => {
    const narrow = mountAt(500)

    await narrow.get('[data-testid="table-card"]').trigger('keydown', { key: 'Enter' })

    expect(narrow.emitted('open')?.[0]?.[0]).toEqual(fixtureRows[0])
  })

  it('never lets the table overflow the page horizontally', () => {
    const wide = mountAt(1024)

    expect(wide.get('[data-testid="table-scroll"]').classes()).toContain('overflow-auto')
  })
})
