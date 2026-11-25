<script setup lang="ts">
import { Search } from '@lucide/vue'
import { computed, h } from 'vue'
import type { LinkItem, LinkPage } from '@/api/types'
import DataTable from '@/components/DataTable.vue'
import LinkStateBadge from '@/components/LinkStateBadge.vue'
import RowActionButton from '@/components/RowActionButton.vue'
import StatusPill from '@/components/StatusPill.vue'
import { LINKS_PAGE_SIZE, LINK_STATES } from '@/composables/useRecordedLinks'
import { codeLabel, t } from '@/i18n'
import type { DataColumn } from '@/lib/dataTable'
import { formatBytes, formatExactBytes } from '@/lib/format'
import { EMPTY_CELL, pathCell, wrapCell } from '@/lib/tableCells'

const props = defineProps<{
  items: LinkItem[]
  page?: LinkPage
  offset: number
  loading: boolean
  loadFailed: boolean
  isFiltered: boolean
  busy: boolean
}>()
const emit = defineEmits<{ page: [offset: number]; repair: [] }>()

const reasonOf = (link: LinkItem) => (link.reason ? (link.label ?? link.reason) : '')
const isBroken = (link: LinkItem) => link.state !== 'ok'

const comparedPill = (link: LinkItem) =>
  link.compared
    ? t('links.compared')
    : h(StatusPill, { tone: 'muted', labelKey: 'links.notCompared', 'data-testid': 'not-compared' })

// Un lien du disque local n'a pas de fournisseur : il s'affiche « local », comme dans la vue Volumes.
function volumeOf(link: LinkItem): string {
  return link.volume || t('volumes.local')
}

const columns = computed<DataColumn<LinkItem>[]>(() => [
  {
    id: 'link',
    meta: { className: 'w-[21%] whitespace-normal' },
    header: t('links.columns.link'),
    accessorFn: (link) => link.link_path,
    cell: (context) => pathCell(context.getValue<string>()),
  },
  {
    id: 'kind',
    meta: { className: 'w-[11%] whitespace-normal' },
    header: t('links.columns.kind'),
    accessorFn: (link) => codeLabel('kind', link.kind),
    cell: (context) => wrapCell(context.getValue<string>()),
  },
  {
    id: 'target',
    meta: { className: 'w-[21%] whitespace-normal' },
    header: t('links.columns.target'),
    accessorFn: (link) => link.target_path,
    cell: (context) => pathCell(context.getValue<string>()),
  },
  {
    id: 'volume',
    meta: { className: 'w-[7%] whitespace-normal' },
    header: t('links.columns.volume'),
    accessorFn: volumeOf,
    cell: (context) => context.getValue<string>(),
  },
  {
    id: 'state',
    meta: { className: 'w-[17%] whitespace-normal' },
    header: t('links.columns.state'),
    accessorFn: (link) => link.state,
    cell: (context) => {
      const link = context.row.original
      return h('div', { class: 'grid gap-1 py-1 whitespace-normal' }, [
        h(LinkStateBadge, { state: link.state }),
        reasonOf(link)
          ? h(
              'p',
              { class: 'text-xs text-muted-foreground [overflow-wrap:anywhere]' },
              reasonOf(link),
            )
          : null,
      ])
    },
  },
  {
    id: 'compared',
    meta: { className: 'w-[13%] whitespace-normal' },
    header: t('links.columns.compared'),
    accessorFn: (link) => link.compared,
    cell: (context) => comparedPill(context.row.original),
  },
  {
    id: 'size',
    meta: { className: 'w-[8%] whitespace-normal' },
    header: t('links.columns.size'),
    accessorFn: (link) => link.size ?? 0,
    cell: (context) => {
      const size = context.row.original.size
      return size ? h('span', { title: formatExactBytes(size) }, formatBytes(size)) : EMPTY_CELL
    },
  },
])

const rowKey = (link: LinkItem): string => String(link.id)
const serverPage = computed(() => ({
  total: props.page?.total ?? 0,
  offset: props.offset,
  onPage: (newOffset: number) => emit('page', newOffset),
}))
</script>

<template>
  <section data-testid="links-panel" class="grid min-w-0 grid-cols-[minmax(0,1fr)] gap-3">
    <ul
      v-if="page"
      class="flex flex-wrap gap-3 text-sm"
      :aria-label="t('links.countsLabel')"
      data-testid="link-counts"
    >
      <li v-for="option in LINK_STATES" :key="option" class="flex items-center gap-1">
        <LinkStateBadge :state="option" />
        <span :data-testid="`count-${option}`">{{ page.counts[option] ?? 0 }}</span>
      </li>
    </ul>
    <div aria-live="polite" :aria-busy="loading" class="grid gap-2" data-testid="links-live">
      <p v-if="loading" class="text-sm text-muted-foreground">{{ t('links.inspecting') }}</p>
      <p
        v-if="page?.truncated"
        class="rounded-lg border border-state-warn p-3 text-sm"
        data-testid="links-truncated"
      >
        {{ t('links.truncated') }}
      </p>
    </div>

    <p
      v-if="loadFailed && page"
      role="alert"
      class="rounded-md border border-destructive p-3 text-destructive"
    >
      {{ t('links.loadFailed') }}
    </p>

    <DataTable
      caption-key="links.caption"
      caption-hidden
      :columns="columns"
      :rows="items"
      :row-key="rowKey"
      :page-size="LINKS_PAGE_SIZE"
      :server-page="serverPage"
      :error="loadFailed && !page ? t('links.loadFailed') : undefined"
      :empty-key="isFiltered ? 'links.emptyFiltered' : 'links.empty'"
    >
      <template #card="{ row }">
        <p class="font-medium [overflow-wrap:anywhere]">{{ row.link_path }}</p>
        <dl class="mt-2 grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-sm">
          <dt class="text-muted-foreground">{{ t('links.columns.target') }}</dt>
          <dd class="[overflow-wrap:anywhere]">{{ row.target_path }}</dd>
          <dt class="text-muted-foreground">{{ t('links.columns.kind') }}</dt>
          <dd>{{ codeLabel('kind', row.kind) }}</dd>
          <dt class="text-muted-foreground">{{ t('links.columns.volume') }}</dt>
          <dd>{{ volumeOf(row) }}</dd>
          <dt class="text-muted-foreground">{{ t('links.columns.state') }}</dt>
          <dd>
            <LinkStateBadge :state="row.state" />
            <span v-if="reasonOf(row)" class="mt-1 block text-xs text-muted-foreground">{{
              reasonOf(row)
            }}</span>
          </dd>
          <dt class="text-muted-foreground">{{ t('links.columns.compared') }}</dt>
          <dd>
            <StatusPill
              v-if="!row.compared"
              tone="muted"
              label-key="links.notCompared"
              data-testid="not-compared"
            />
            <template v-else>{{ t('links.compared') }}</template>
          </dd>
          <dt class="text-muted-foreground">{{ t('links.columns.size') }}</dt>
          <dd>{{ row.size ? formatBytes(row.size) : EMPTY_CELL }}</dd>
        </dl>
      </template>
      <template #row-actions="{ row }">
        <div v-if="isBroken(row)" class="flex justify-end py-1">
          <RowActionButton
            :icon="Search"
            variant="outline"
            :label="t('links.actions.searchRepair')"
            :accessible-name="t('links.actions.searchRepairOf', { name: row.link_path })"
            data-action="repair scan"
            data-scope="row"
            data-testid="search-repair"
            :disabled="busy"
            @click="emit('repair')"
          />
        </div>
      </template>
    </DataTable>
  </section>
</template>
