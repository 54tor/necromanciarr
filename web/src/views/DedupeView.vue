<script setup lang="ts">
import { FlaskConical, Link2, TriangleAlert, X } from '@lucide/vue'
import { computed, h, onMounted, ref } from 'vue'
import { toast } from 'vue-sonner'
import { api } from '@/api/endpoints'
import type { DedupeDetails, MaintenanceProposal } from '@/api/types'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import DataTable from '@/components/DataTable.vue'
import DedupeApplicableCell from '@/components/DedupeApplicableCell.vue'
import DedupeWarningsCell from '@/components/DedupeWarningsCell.vue'
import PageToolbar from '@/components/PageToolbar.vue'
import RowActionButton from '@/components/RowActionButton.vue'
import StatusPill from '@/components/StatusPill.vue'
import { Button } from '@/components/ui/button'
import { useApi } from '@/composables/useApi'
import { useMaintenance } from '@/composables/useMaintenance'
import { codeLabel, t } from '@/i18n'
import type { DataColumn } from '@/lib/dataTable'
import { dedupeWarningTexts } from '@/lib/dedupeWarnings'
import { formatBytes, formatExactBytes } from '@/lib/format'
import { DECISION_TONES, decisionRank } from '@/lib/proposalRules'
import { EMPTY_CELL, pathCell, wrapCell } from '@/lib/tableCells'

type Item = MaintenanceProposal<DedupeDetails>

const {
  items,
  acceptedItems,
  dryRun,
  busy,
  pending,
  loading,
  loadFailed,
  load,
  canApply,
  applyOne,
  applyAccepted,
  start,
} = useMaintenance<DedupeDetails>('dedupe', api.dedupeApply)
const { call } = useApi()

const isBlocked = computed(() => busy.value || pending.value)

const bytesCell = (bytes: number) =>
  h('span', { title: formatExactBytes(bytes), 'data-testid': 'bytes-to-read' }, formatBytes(bytes))

const columns = computed<DataColumn<Item>[]>(() => [
  {
    id: 'torrentFile',
    meta: { className: 'w-[13%] whitespace-normal' },
    header: t('dedupe.columns.torrentFile'),
    accessorFn: (item) => item.details.torrent_path,
    sortFn: 'text',
    cell: (context) => pathCell(context.getValue<string>()),
  },
  {
    id: 'libraryTarget',
    meta: { className: 'w-[13%] whitespace-normal' },
    header: t('dedupe.columns.libraryTarget'),
    accessorFn: (item) => item.details.library_path ?? '',
    sortFn: 'text',
    cell: (context) =>
      context.getValue<string>() ? pathCell(context.getValue<string>()) : EMPTY_CELL,
  },
  {
    id: 'kind',
    meta: { className: 'w-[10%] whitespace-normal' },
    header: t('dedupe.columns.kind'),
    accessorFn: (item) => (item.details.kind ? codeLabel('kind', item.details.kind) : ''),
    sortFn: 'text',
    cell: (context) => wrapCell(context.getValue<string>() || t('dedupe.kindUndefined')),
  },
  {
    id: 'proof',
    meta: { className: 'w-[13%] whitespace-normal' },
    header: t('dedupe.columns.proof'),
    accessorFn: (item) => codeLabel('reason', item.details.method),
    sortFn: 'text',
    cell: (context) => wrapCell(context.getValue<string>()),
  },
  {
    id: 'bytesToRead',
    meta: { className: 'w-[7%] whitespace-normal' },
    header: t('dedupe.columns.bytesToRead'),
    accessorFn: (item) => item.details.bytes_to_read,
    sortFn: 'basic',
    cell: (context) => bytesCell(context.getValue<number>()),
  },
  {
    id: 'decision',
    meta: { className: 'w-[11%] whitespace-normal' },
    header: t('dedupe.columns.decision'),
    accessorFn: (item) => decisionRank(item.decision),
    sortFn: 'basic',
    cell: (context) =>
      h(StatusPill, {
        tone: DECISION_TONES[context.row.original.decision],
        labelKey: `codes.decision.${context.row.original.decision}`,
      }),
  },
  {
    id: 'applicable',
    meta: { className: 'w-[14%] whitespace-normal' },
    header: t('dedupe.columns.applicable'),
    accessorFn: (item) => Number(item.details.actionable),
    sortFn: 'basic',
    cell: (context) => h(DedupeApplicableCell, { details: context.row.original.details }),
  },
  {
    id: 'warnings',
    meta: { className: 'w-[6%] whitespace-normal' },
    header: () =>
      h('span', { title: t('dedupe.columns.warnings') }, [
        h(TriangleAlert, { class: 'size-4', 'aria-hidden': 'true' }),
        h('span', { class: 'sr-only' }, t('dedupe.columns.warnings')),
      ]),
    accessorFn: (item) => dedupeWarningTexts(item).length,
    sortFn: 'basic',
    cell: (context) => h(DedupeWarningsCell, { texts: dedupeWarningTexts(context.row.original) }),
  },
])

const rowKey = (item: Item): string => String(item.id)

type ApplyRequest = { dryRun: boolean; items: Item[]; chain: boolean }
const request = ref<ApplyRequest>()
const confirmOpen = ref(false)

function openConfirmation(next: ApplyRequest): void {
  request.value = next
  confirmOpen.value = true
}

// Une simulation ne modifie rien : seul le réel passe par la confirmation, pour une ligne.
function applyRow(item: Item): void {
  if (dryRun.value) void applyOne(item, true)
  else openConfirmation({ dryRun: false, items: [item], chain: false })
}

// La liste est toujours montrée avant l'envoi, simulation comprise ; le mode est figé à l'ouverture.
function askAcceptedBatch(): void {
  openConfirmation({ dryRun: dryRun.value, items: [...acceptedItems.value], chain: true })
}

function confirmApply(): void {
  const confirmed = request.value
  if (!confirmed) return
  if (confirmed.chain) void applyAccepted(confirmed.dryRun, confirmed.items)
  else void applyOne(confirmed.items[0]!, confirmed.dryRun)
}

const isRealRequest = computed(() => request.value !== undefined && !request.value.dryRun)
const isRequestedRowPending = computed(
  () => request.value?.chain === false && request.value.items[0]?.decision !== 'accepted',
)
const confirmSummaryKey = computed(() => {
  if (!isRealRequest.value) return 'dedupe.confirm.summaryDry'
  return isRequestedRowPending.value
    ? 'dedupe.confirm.summaryRealPending'
    : 'dedupe.confirm.summaryReal'
})
const confirmTitle = computed(() =>
  request.value?.chain
    ? t('dedupe.confirm.bulkTitle', { count: request.value.items.length })
    : t('dedupe.confirm.rowTitle'),
)
const confirmItems = computed(() =>
  (request.value?.items ?? []).map((item) =>
    t('dedupe.confirm.item', {
      file: item.details.torrent_path,
      target: item.details.library_path ?? t('dedupe.confirm.noTarget'),
    }),
  ),
)

function applyKey(item: Item): 'simulate' | 'apply' | 'acceptAndApply' {
  if (dryRun.value) return 'simulate'
  return item.decision === 'accepted' ? 'apply' : 'acceptAndApply'
}

const canReject = (item: Item) => item.decision === 'pending'

async function reject(item: Item): Promise<void> {
  if ((await call(() => api.reject(item.id))) !== undefined)
    toast.success(t('dedupe.toasts.rejected'))
  await load()
}

onMounted(load)
</script>

<template>
  <section class="grid min-w-0 grid-cols-[minmax(0,1fr)] gap-4">
    <PageToolbar>
      <template #actions>
        <Button
          data-action="dedupe plan"
          data-scope="toolbar"
          :disabled="isBlocked"
          @click="start(() => api.dedupePlan())"
        >
          {{ t('dedupe.plan') }}
        </Button>
        <Button
          variant="secondary"
          data-action="dedupe apply"
          data-scope="toolbar"
          data-testid="apply-all"
          :disabled="isBlocked || acceptedItems.length === 0"
          @click="askAcceptedBatch"
        >
          {{ t('dedupe.applyAccepted', { count: acceptedItems.length }) }}
        </Button>
        <label class="flex items-center gap-2 text-sm">
          <input
            v-model="dryRun"
            type="checkbox"
            class="size-4 accent-primary"
            data-testid="dry-run"
          />
          {{ t('dedupe.dryRun') }}
        </label>
      </template>
    </PageToolbar>

    <DataTable
      caption-key="dedupe.caption"
      caption-hidden
      :columns="columns"
      :rows="items"
      :row-key="rowKey"
      :initial-sort="{ id: 'decision', desc: false }"
      :loading="loading"
      :error="loadFailed ? t('dedupe.loadFailed') : undefined"
      empty-key="dedupe.empty"
    >
      <template #card="{ row }">
        <p class="font-medium [overflow-wrap:anywhere]">{{ row.details.torrent_path }}</p>
        <dl class="mt-2 grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-sm">
          <dt class="text-muted-foreground">{{ t('dedupe.columns.libraryTarget') }}</dt>
          <dd class="[overflow-wrap:anywhere]">{{ row.details.library_path ?? EMPTY_CELL }}</dd>
          <dt class="text-muted-foreground">{{ t('dedupe.columns.kind') }}</dt>
          <dd>
            {{ row.details.kind ? codeLabel('kind', row.details.kind) : t('dedupe.kindUndefined') }}
          </dd>
          <dt class="text-muted-foreground">{{ t('dedupe.columns.proof') }}</dt>
          <dd>{{ codeLabel('reason', row.details.method) }}</dd>
          <dt class="text-muted-foreground">{{ t('dedupe.columns.bytesToRead') }}</dt>
          <dd>
            <span
              data-testid="bytes-to-read"
              :title="formatExactBytes(row.details.bytes_to_read)"
              >{{ formatBytes(row.details.bytes_to_read) }}</span
            >
          </dd>
          <dt class="text-muted-foreground">{{ t('dedupe.columns.decision') }}</dt>
          <dd>
            <StatusPill
              :tone="DECISION_TONES[row.decision]"
              :label-key="`codes.decision.${row.decision}`"
            />
          </dd>
          <dt class="text-muted-foreground">{{ t('dedupe.columns.applicable') }}</dt>
          <dd><DedupeApplicableCell :details="row.details" /></dd>
          <dt class="text-muted-foreground">{{ t('dedupe.columns.warnings') }}</dt>
          <dd><DedupeWarningsCell :texts="dedupeWarningTexts(row)" /></dd>
        </dl>
      </template>
      <template #row-actions="{ row }">
        <div class="flex flex-wrap justify-end gap-1 py-1">
          <RowActionButton
            :icon="dryRun ? FlaskConical : Link2"
            :label="t(`dedupe.actions.${applyKey(row)}`)"
            :accessible-name="
              t(`dedupe.actions.${applyKey(row)}Of`, { name: row.details.torrent_path })
            "
            data-action="dedupe apply"
            data-scope="row"
            :disabled="isBlocked || !canApply(row)"
            @click="applyRow(row)"
          />
          <RowActionButton
            :icon="X"
            variant="outline"
            :label="t('dedupe.actions.reject')"
            :accessible-name="t('dedupe.actions.rejectOf', { name: row.details.torrent_path })"
            data-action="proposals reject"
            data-scope="row"
            :disabled="isBlocked || !canReject(row)"
            @click="reject(row)"
          />
        </div>
      </template>
    </DataTable>

    <ConfirmDialog
      v-model:open="confirmOpen"
      :title="confirmTitle"
      :description="
        t(isRealRequest ? 'dedupe.confirm.descriptionReal' : 'dedupe.confirm.descriptionDry')
      "
      :confirm-label="t(isRealRequest ? 'dedupe.confirm.confirmReal' : 'dedupe.confirm.confirmDry')"
      :summary-key="confirmSummaryKey"
      :items="confirmItems"
      :confirm-phrase="isRealRequest ? t('dedupe.confirm.phrase') : undefined"
      @confirm="confirmApply"
    />
  </section>
</template>
