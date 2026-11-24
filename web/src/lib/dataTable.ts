import {
  createPaginatedRowModel,
  createSortedRowModel,
  metaHelper,
  rowPaginationFeature,
  rowSortingFeature,
  sortFn_alphanumeric,
  sortFn_basic,
  sortFn_datetime,
  sortFn_text,
  tableFeatures,
  type ColumnDef,
} from '@tanstack/vue-table'

/** Métadonnées de colonne : classe appliquée à l'en-tête et aux cellules (largeur, alignement). */
export interface ColumnClassMeta {
  className?: string
}

/** Fonctionnalités du tableau : tri et pagination côté client, tout le reste inutile. */
export const dataTableFeatures = tableFeatures({
  rowSortingFeature,
  columnMeta: metaHelper<ColumnClassMeta>(),
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
  /** Sans ce rappel le tri est désactivé en mode serveur ; avec lui, le serveur trie et le tableau garde l'ordre reçu. */
  onSort?: (sort: { id: string; desc: boolean }) => void
}
