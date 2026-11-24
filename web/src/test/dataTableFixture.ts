import { h } from 'vue'
import type { DataColumn } from '@/lib/dataTable'

export interface FixtureRow {
  id: string
  name: string
  count: number
}

export const fixtureRows: FixtureRow[] = [
  { id: 'a', name: 'Charlie', count: 3 },
  { id: 'b', name: 'Alice', count: 20 },
  { id: 'c', name: 'Bob', count: 1 },
]

export const fixtureColumns: DataColumn<FixtureRow>[] = [
  { id: 'name', accessorKey: 'name', header: 'Nom' },
  { id: 'count', accessorKey: 'count', header: 'Nombre' },
  {
    id: 'static',
    header: 'Fixe',
    enableSorting: false,
    cell: (context) => h('span', `fixe-${context.row.original.id}`),
  },
]

export function fixtureRowKey(row: FixtureRow): string {
  return row.id
}
