<script setup lang="ts" generic="Row extends object">
import { ArrowDown, ArrowUp, ArrowUpDown, ChevronLeft, ChevronRight } from '@lucide/vue'
import { FlexRender, functionalUpdate, useTable, type SortingState } from '@tanstack/vue-table'
import { useMediaQuery } from '@vueuse/core'
import { computed, nextTick, ref, watch } from 'vue'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCaption,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { t } from '@/i18n'
import { dataTableFeatures, type DataColumn, type ServerPage } from '@/lib/dataTable'

const DEFAULT_PAGE_SIZE = 100
const NARROW_BREAKPOINT = '(min-width: 768px)'
const HEADER_ROW_COUNT = 1

const props = withDefaults(
  defineProps<{
    captionKey: string
    captionHidden?: boolean
    columns: DataColumn<Row>[]
    rows: Row[]
    rowKey: (row: Row) => string
    initialSort?: { id: string; desc: boolean }
    pageSize?: number
    serverPage?: ServerPage
    selectable?: (row: Row) => boolean
    emptyKey: string
    error?: string
    loading?: boolean
  }>(),
  {
    pageSize: DEFAULT_PAGE_SIZE,
    initialSort: undefined,
    serverPage: undefined,
    selectable: undefined,
    error: undefined,
  },
)
const emit = defineEmits<{ select: [keys: string[]]; open: [row: Row] }>()
defineSlots<{
  'row-actions'?: (scope: { row: Row }) => unknown
  card?: (scope: { row: Row }) => unknown
}>()

const isWide = useMediaQuery(NARROW_BREAKPOINT)

const sorting = ref<SortingState>(props.initialSort ? [props.initialSort] : [])
const clientPageIndex = ref(0)
const pageIndex = computed(() =>
  props.serverPage ? Math.floor(props.serverPage.offset / props.pageSize) : clientPageIndex.value,
)

const table = useTable({
  features: dataTableFeatures,
  columns: computed(() => props.columns),
  data: computed(() => props.rows),
  getRowId: (row: Row) => props.rowKey(row),
  state: computed(() => ({
    sorting: sorting.value,
    pagination: { pageIndex: pageIndex.value, pageSize: props.pageSize },
  })),
  enableSorting: computed(() => !props.serverPage || props.serverPage.onSort !== undefined),
  manualSorting: computed(() => props.serverPage?.onSort !== undefined),
  onSortingChange: (updater) => {
    sorting.value = functionalUpdate(updater, sorting.value)
    const [requested] = sorting.value
    if (requested) props.serverPage?.onSort?.({ id: requested.id, desc: requested.desc })
  },
  onPaginationChange: (updater) => {
    const next = functionalUpdate(updater, {
      pageIndex: pageIndex.value,
      pageSize: props.pageSize,
    })
    if (!props.serverPage) clientPageIndex.value = next.pageIndex
  },
  manualPagination: computed(() => props.serverPage !== undefined),
  rowCount: computed(() => props.serverPage?.total),
})

const headers = computed(() => table.getHeaderGroups().flatMap((group) => group.headers))
const visibleRows = computed(() => table.getRowModel().rows)
const hasSelection = computed(() => props.selectable !== undefined)
const total = computed(() => props.serverPage?.total ?? props.rows.length)
const offset = computed(() => props.serverPage?.offset ?? pageIndex.value * props.pageSize)
const showPager = computed(() => props.serverPage !== undefined || total.value > props.pageSize)
const hasPrevious = computed(() => offset.value > 0)
const hasNext = computed(() => offset.value + props.pageSize < total.value)
const pageStatus = computed(() =>
  t('table.pageStatus', {
    from: offset.value + 1,
    to: offset.value + visibleRows.value.length,
    total: total.value,
  }),
)
const isEmpty = computed(() => !props.loading && visibleRows.value.length === 0)

function rowIndex(position: number): number {
  return offset.value + position + 1 + HEADER_ROW_COUNT
}

function ariaSort(isSorted: false | 'asc' | 'desc'): 'none' | 'ascending' | 'descending' {
  if (isSorted === 'asc') return 'ascending'
  return isSorted === 'desc' ? 'descending' : 'none'
}

// Un clic alterne croissant/décroissant : le retour à « sans tri » n'apporte rien sur un tableau de gestion.
function toggleSort(header: (typeof headers.value)[number]): void {
  header.column.toggleSorting(header.column.getIsSorted() === 'asc')
}

const pageStatusElement = ref<HTMLElement>()

// Un bouton qui devient désactivé perd le focus : il passe à l'état de la page, qui annonce le résultat.
async function goToPage(targetOffset: number): Promise<void> {
  if (props.serverPage) props.serverPage.onPage(targetOffset)
  else clientPageIndex.value = Math.floor(targetOffset / props.pageSize)
  await nextTick()
  const focused = document.activeElement
  const lostFocus =
    focused === document.body || (focused instanceof HTMLButtonElement && focused.disabled)
  if (lostFocus) pageStatusElement.value?.focus()
}

const focusedKey = ref<string>()
// Si la ligne mémorisée a disparu (page, tri), la première ligne reste l'entrée clavier du tableau.
const tabbableKey = computed(() =>
  visibleRows.value.some((row) => row.id === focusedKey.value)
    ? focusedKey.value
    : visibleRows.value[0]?.id,
)

function moveFocus(event: KeyboardEvent, direction: 'previous' | 'next'): void {
  const current = event.currentTarget
  if (!(current instanceof HTMLElement)) return
  const sibling = direction === 'next' ? current.nextElementSibling : current.previousElementSibling
  if (!(sibling instanceof HTMLElement)) return
  focusedKey.value = sibling.dataset.rowKey
  sibling.focus()
  event.preventDefault()
}

function onRowKeydown(event: KeyboardEvent, row: Row): void {
  // Les touches d'un bouton ou d'une case à l'intérieur de la ligne ne lui appartiennent pas.
  if (event.target !== event.currentTarget) return
  if (event.key === 'ArrowDown') moveFocus(event, 'next')
  else if (event.key === 'ArrowUp') moveFocus(event, 'previous')
  else if (event.key === 'Enter') emit('open', row)
}

const selectedKeys = ref<string[]>([])

function toggleSelection(key: string, isChecked: boolean): void {
  selectedKeys.value = isChecked
    ? [...selectedKeys.value, key]
    : selectedKeys.value.filter((selectedKey) => selectedKey !== key)
  emit('select', selectedKeys.value)
}

function isSelectable(row: Row): boolean {
  return props.selectable?.(row) ?? false
}

function onCheckboxChange(event: Event, key: string): void {
  if (event.target instanceof HTMLInputElement) toggleSelection(key, event.target.checked)
}

// La sélection est locale à la page affichée : une clé qui n'est plus visible est retirée et signalée.
watch(
  () => visibleRows.value.map((row) => row.id),
  (visibleKeys) => {
    const kept = selectedKeys.value.filter((key) => visibleKeys.includes(key))
    if (kept.length === selectedKeys.value.length) return
    selectedKeys.value = kept
    emit('select', kept)
  },
)

function headerOf(columnId: string): (typeof headers.value)[number] | undefined {
  return headers.value.find((header) => header.column.id === columnId)
}
</script>

<template>
  <div
    data-testid="data-table"
    class="flex flex-col gap-2"
    :aria-busy="loading ? 'true' : undefined"
  >
    <p v-if="error" role="alert" class="rounded-md border border-destructive p-3 text-destructive">
      {{ error }}
    </p>
    <template v-else>
      <Table v-if="isWide" data-testid="table-scroll" class="caption-top">
        <TableCaption :class="captionHidden ? 'sr-only' : 'mt-0 mb-2 text-left'">
          {{ t(captionKey) }}
        </TableCaption>
        <TableHeader>
          <TableRow>
            <TableHead v-if="hasSelection" scope="col" class="w-8">
              <span class="sr-only">{{ t('table.selection') }}</span>
            </TableHead>
            <TableHead
              v-for="header in headers"
              :key="header.id"
              scope="col"
              :aria-sort="
                header.column.getCanSort() ? ariaSort(header.column.getIsSorted()) : undefined
              "
            >
              <button
                v-if="header.column.getCanSort()"
                type="button"
                class="inline-flex items-center gap-1 rounded-sm font-medium hover:text-primary focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
                @click="toggleSort(header)"
              >
                <FlexRender :header="header" />
                <ArrowUp
                  v-if="header.column.getIsSorted() === 'asc'"
                  class="size-3.5"
                  aria-hidden="true"
                />
                <ArrowDown
                  v-else-if="header.column.getIsSorted() === 'desc'"
                  class="size-3.5"
                  aria-hidden="true"
                />
                <ArrowUpDown v-else class="size-3.5 text-muted-foreground" aria-hidden="true" />
              </button>
              <FlexRender v-else :header="header" />
            </TableHead>
            <TableHead v-if="$slots['row-actions']" scope="col" class="text-right">
              <span class="sr-only">{{ t('table.actions') }}</span>
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          <TableRow
            v-for="(row, position) in visibleRows"
            :key="row.id"
            :data-row-key="row.id"
            :aria-rowindex="rowIndex(position)"
            :tabindex="row.id === tabbableKey ? 0 : -1"
            class="h-9 focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none focus-visible:ring-inset"
            @keydown="onRowKeydown($event, row.original)"
            @focus="focusedKey = row.id"
          >
            <TableCell v-if="hasSelection" class="w-8 py-0">
              <input
                v-if="isSelectable(row.original)"
                type="checkbox"
                class="size-4 accent-primary"
                :aria-label="t('table.selectRow', { row: offset + position + 1 })"
                @change="onCheckboxChange($event, row.id)"
              />
            </TableCell>
            <TableCell v-for="cell in row.getAllCells()" :key="cell.id" class="py-0">
              <FlexRender :cell="cell" />
            </TableCell>
            <TableCell v-if="$slots['row-actions']" class="py-0 text-right">
              <slot name="row-actions" :row="row.original" />
            </TableCell>
          </TableRow>
        </TableBody>
      </Table>

      <ul v-else :aria-label="t(captionKey)" class="flex flex-col gap-2">
        <li
          v-for="(row, position) in visibleRows"
          :key="row.id"
          data-testid="table-card"
          :data-row-key="row.id"
          tabindex="0"
          class="rounded-lg border border-border bg-card p-3 text-card-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
          @keydown="onRowKeydown($event, row.original)"
        >
          <input
            v-if="hasSelection && isSelectable(row.original)"
            type="checkbox"
            class="mb-2 size-4 accent-primary"
            :aria-label="t('table.selectRow', { row: offset + position + 1 })"
            @change="onCheckboxChange($event, row.id)"
          />
          <slot name="card" :row="row.original">
            <dl class="grid gap-1">
              <div
                v-for="cell in row.getAllCells()"
                :key="cell.id"
                class="flex justify-between gap-3"
              >
                <dt class="text-muted-foreground">
                  <FlexRender v-if="headerOf(cell.column.id)" :header="headerOf(cell.column.id)" />
                </dt>
                <dd class="min-w-0 text-right"><FlexRender :cell="cell" /></dd>
              </div>
            </dl>
          </slot>
          <div v-if="$slots['row-actions']" class="mt-2 flex flex-wrap justify-end gap-2">
            <slot name="row-actions" :row="row.original" />
          </div>
        </li>
      </ul>

      <div data-testid="table-status" aria-live="polite">
        <p v-if="loading" class="p-3 text-center text-muted-foreground">
          {{ t('table.loading') }}
        </p>
        <p
          v-else-if="isEmpty"
          data-testid="table-empty"
          class="p-6 text-center text-muted-foreground"
        >
          {{ t(emptyKey) }}
        </p>
      </div>

      <nav
        v-if="showPager"
        :aria-label="t('table.pagination')"
        class="flex items-center justify-end gap-2 text-sm"
      >
        <span
          ref="pageStatusElement"
          data-testid="table-page-status"
          tabindex="-1"
          class="text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
          >{{ pageStatus }}</span
        >
        <Button
          variant="outline"
          size="icon"
          data-testid="table-previous"
          :disabled="!hasPrevious"
          :aria-label="t('table.previous')"
          @click="goToPage(Math.max(0, offset - pageSize))"
        >
          <ChevronLeft aria-hidden="true" />
        </Button>
        <Button
          variant="outline"
          size="icon"
          data-testid="table-next"
          :disabled="!hasNext"
          :aria-label="t('table.next')"
          @click="goToPage(offset + pageSize)"
        >
          <ChevronRight aria-hidden="true" />
        </Button>
      </nav>
    </template>
  </div>
</template>
