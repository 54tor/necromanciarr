<script setup lang="ts">
import { FlaskConical, Wrench } from '@lucide/vue'
import { computed, h, ref } from 'vue'
import type { MaintenanceProposal, RepairDetails } from '@/api/types'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import DataTable from '@/components/DataTable.vue'
import DedupeWarningsCell from '@/components/DedupeWarningsCell.vue'
import RepairApplicableCell from '@/components/RepairApplicableCell.vue'
import RowActionButton from '@/components/RowActionButton.vue'
import StatusPill from '@/components/StatusPill.vue'
import { codeLabel, t } from '@/i18n'
import type { DataColumn } from '@/lib/dataTable'
import { warningTextsOf } from '@/lib/dedupeWarnings'
import { DECISION_TONES, decisionRank } from '@/lib/proposalRules'
import { EMPTY_CELL, pathCell, wrapCell } from '@/lib/tableCells'

type Item = MaintenanceProposal<RepairDetails>

const props = defineProps<{
  items: Item[]
  loading: boolean
  loadFailed: boolean
  dryRun: boolean
  blocked: boolean
  canApply: (item: Item) => boolean
}>()
const emit = defineEmits<{ apply: [item: Item, dryRun: boolean] }>()

// Sans avertissement, la cellule resterait un « — » sans objet sous l'état.
function warningsCell(item: Item) {
  const texts = warningTextsOf(item.warnings)
  return texts.length > 0 ? [h(DedupeWarningsCell, { texts })] : []
}

const columns = computed<DataColumn<Item>[]>(() => [
  {
    id: 'link',
    meta: { className: 'w-[28%] whitespace-normal' },
    header: t('links.repairs.columns.link'),
    accessorFn: (item) => item.details.link_path,
    sortFn: 'text',
    cell: (context) => pathCell(context.getValue<string>()),
  },
  {
    id: 'newTarget',
    meta: { className: 'w-[28%] whitespace-normal' },
    header: t('links.repairs.columns.newTarget'),
    accessorFn: (item) => item.details.new_target ?? '',
    sortFn: 'text',
    cell: (context) =>
      context.getValue<string>() ? pathCell(context.getValue<string>()) : EMPTY_CELL,
  },
  {
    id: 'method',
    meta: { className: 'w-[12%] whitespace-normal' },
    header: t('links.repairs.columns.method'),
    accessorFn: (item) => (item.details.method ? codeLabel('reason', item.details.method) : ''),
    sortFn: 'text',
    cell: (context) =>
      context.getValue<string>() ? wrapCell(context.getValue<string>()) : EMPTY_CELL,
  },
  {
    id: 'applicable',
    meta: { className: 'w-[18%] whitespace-normal' },
    header: t('links.repairs.columns.applicable'),
    accessorFn: (item) => Number(item.details.actionable),
    sortFn: 'basic',
    cell: (context) =>
      h('div', [
        h(RepairApplicableCell, { details: context.row.original.details }),
        ...warningsCell(context.row.original),
      ]),
  },
  {
    id: 'decision',
    meta: { className: 'w-[10%] whitespace-normal' },
    header: t('links.repairs.columns.decision'),
    accessorFn: (item) => decisionRank(item.decision),
    sortFn: 'basic',
    cell: (context) =>
      h(StatusPill, {
        tone: DECISION_TONES[context.row.original.decision],
        labelKey: `codes.decision.${context.row.original.decision}`,
      }),
  },
])

const rowKey = (item: Item): string => String(item.id)

const requested = ref<Item>()
const confirmOpen = ref(false)

// Une simulation ne modifie rien : seul l'apply réel passe par la confirmation.
function applyRow(item: Item): void {
  if (props.dryRun) {
    emit('apply', item, true)
    return
  }
  requested.value = item
  confirmOpen.value = true
}

function confirmApply(): void {
  if (requested.value) emit('apply', requested.value, false)
}

const isPendingRequest = computed(() => requested.value?.decision !== 'accepted')
const confirmSummaryKey = computed(() =>
  isPendingRequest.value
    ? 'links.repairs.confirm.summaryRealPending'
    : 'links.repairs.confirm.summaryReal',
)
const confirmItems = computed(() =>
  requested.value
    ? [
        t('links.repairs.confirm.item', {
          link: requested.value.details.link_path,
          target: requested.value.details.new_target ?? t('links.repairs.confirm.noTarget'),
        }),
      ]
    : [],
)

function applyKey(item: Item): 'simulate' | 'apply' | 'acceptAndApply' {
  if (props.dryRun) return 'simulate'
  return item.decision === 'accepted' ? 'apply' : 'acceptAndApply'
}
</script>

<template>
  <section data-testid="repairs-panel" class="grid min-w-0 grid-cols-[minmax(0,1fr)] gap-3">
    <DataTable
      caption-key="links.repairs.caption"
      caption-hidden
      :columns="columns"
      :rows="items"
      :row-key="rowKey"
      :initial-sort="{ id: 'decision', desc: false }"
      :loading="loading"
      :error="loadFailed ? t('links.repairs.loadFailed') : undefined"
      empty-key="links.repairs.empty"
    >
      <template #card="{ row }">
        <p class="font-medium [overflow-wrap:anywhere]">{{ row.details.link_path }}</p>
        <dl class="mt-2 grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-sm">
          <dt class="text-muted-foreground">{{ t('links.repairs.columns.newTarget') }}</dt>
          <dd class="[overflow-wrap:anywhere]">{{ row.details.new_target ?? EMPTY_CELL }}</dd>
          <dt class="text-muted-foreground">{{ t('links.repairs.columns.method') }}</dt>
          <dd>{{ row.details.method ? codeLabel('reason', row.details.method) : EMPTY_CELL }}</dd>
          <dt class="text-muted-foreground">{{ t('links.repairs.columns.decision') }}</dt>
          <dd>
            <StatusPill
              :tone="DECISION_TONES[row.decision]"
              :label-key="`codes.decision.${row.decision}`"
            />
          </dd>
          <dt class="text-muted-foreground">{{ t('links.repairs.columns.applicable') }}</dt>
          <dd>
            <RepairApplicableCell :details="row.details" />
            <DedupeWarningsCell
              v-if="warningTextsOf(row.warnings).length > 0"
              :texts="warningTextsOf(row.warnings)"
            />
          </dd>
        </dl>
      </template>
      <template #row-actions="{ row }">
        <div class="flex flex-wrap justify-end gap-1 py-1">
          <RowActionButton
            :icon="dryRun ? FlaskConical : Wrench"
            :label="t(`links.repairs.actions.${applyKey(row)}`)"
            :accessible-name="
              t(`links.repairs.actions.${applyKey(row)}Of`, { name: row.details.link_path })
            "
            data-action="repair apply"
            data-scope="row"
            :disabled="blocked || !canApply(row)"
            @click="applyRow(row)"
          />
        </div>
      </template>
    </DataTable>

    <ConfirmDialog
      v-model:open="confirmOpen"
      :title="t('links.repairs.confirm.title')"
      :description="t('links.repairs.confirm.descriptionReal')"
      :confirm-label="t('links.repairs.confirm.confirmReal')"
      :summary-key="confirmSummaryKey"
      :items="confirmItems"
      :confirm-phrase="t('links.repairs.confirm.phrase')"
      @confirm="confirmApply"
    />
  </section>
</template>
