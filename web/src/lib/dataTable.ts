import {
  createPaginatedRowModel,
  createSortedRowModel,
  rowPaginationFeature,
  rowSortingFeature,
  sortFn_alphanumeric,
  sortFn_basic,
  sortFn_datetime,
  sortFn_text,
  tableFeatures,
  type ColumnDef,
} from '@tanstack/vue-table'

/** Fonctionnalités du tableau : tri et pagination côté client, tout le reste inutile. */
export const dataTableFeatures = tableFeatures({
  rowSortingFeature,
  sortedRowModel: createSortedRowModel(),
  sortFns: {
    alphanumeric: sortFn_alphanumeric,
    basic: sortFn_basic,
    datetime: sortFn_datetime,
    text: sortFn_text,
  },
  rowPaginationFeature,
  paginatedRowModel: createPaginatedRowModel(),
})

/** DataColumn est la définition de colonne acceptée par `DataTable` (v9 : le type porte les fonctionnalités). */
export type DataColumn<Row extends object> = ColumnDef<typeof dataTableFeatures, Row>

/** Pagination serveur : le parent fournit la page courante et reçoit le nouveau décalage. */
export interface ServerPage {
  total: number
  offset: number
  onPage: (offset: number) => void
}
