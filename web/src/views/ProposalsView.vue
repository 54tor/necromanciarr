<script setup lang="ts">
import {
  Check,
  CheckCheck,
  ListChecks,
  RefreshCw,
  Trash2,
  TriangleAlert,
  X,
  Zap,
} from '@lucide/vue'
import { computed, h, onMounted, reactive, ref, watch } from 'vue'
import { toast } from 'vue-sonner'
import { api } from '@/api/endpoints'
import type { Decision, Proposal, ProposalKind } from '@/api/types'
import ChooseDialog from '@/components/ChooseDialog.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import DataTable from '@/components/DataTable.vue'
import PageToolbar from '@/components/PageToolbar.vue'
import ProposalNameCell from '@/components/ProposalNameCell.vue'
import RowActionButton from '@/components/RowActionButton.vue'
import StatusPill from '@/components/StatusPill.vue'
import { Button } from '@/components/ui/button'
import { useApi } from '@/composables/useApi'
import { codeLabel, t } from '@/i18n'
import type { DataColumn } from '@/lib/dataTable'
import { formatDateTime, formatPercent, formatRelative } from '@/lib/format'
import { DECISION_LABELS, KIND_LABELS, REVIEW_KINDS } from '@/lib/labels'
import {
  canAccept,
  canAcceptGroup,
  canChoose,
  canForce,
  canReject,
  DECISION_TONES,
  decisionRank,
  groupTargets,
  hasSourceField,
  isSelectable,
} from '@/lib/proposalRules'
import { FIELD_CLASS } from '@/lib/styles'
import { cn } from '@/lib/utils'

const { call, pending } = useApi()
const proposals = ref<Proposal[]>([])
const isLoading = ref(false)
const loadFailed = ref(false)
const filters = reactive({
  kind: '' as ProposalKind | '',
  decision: '' as Decision | '',
  group: '',
  currentOnly: false,
  source: '',
  search: '',
})

const EMPTY_CELL = '—'
const PRIMARY_MARK = '★'
/** Entrée vide d'un filtre : « Nature : Toutes ». */
function allOf(filterKey: string): string {
  return t('common.labelValue', { label: t(filterKey), value: t('proposals.filters.all') })
}

const TOOLBAR_FIELD_CLASS = cn(FIELD_CLASS, 'w-auto')

// Le champ Source n'existe pas encore côté serveur (tâche B14) : colonne et filtre restent masqués.
const hasSource = computed(() => hasSourceField(proposals.value))
const sources = computed(() => [
  ...new Set(proposals.value.map((row) => row.source).filter((source) => source !== undefined)),
])

const visibleRows = computed(() => {
  const needle = filters.search.trim().toLowerCase()
  return proposals.value.filter(
    (row) =>
      (needle === '' || row.candidate_name.toLowerCase().includes(needle)) &&
      (filters.source === '' || row.source === filters.source),
  )
})

const expandedIds = ref<number[]>([])

function toggleDetail(row: Proposal): void {
  expandedIds.value = expandedIds.value.includes(row.id)
    ? expandedIds.value.filter((expandedId) => expandedId !== row.id)
    : [...expandedIds.value, row.id]
}

const columns = computed<DataColumn<Proposal>[]>(() => {
  const list: DataColumn<Proposal>[] = [
    {
      id: 'name',
      meta: { className: 'w-[15%] whitespace-normal' },
      header: t('proposals.columns.name'),
      accessorFn: (row) => row.candidate_name,
      sortFn: 'text',
      cell: (context) =>
        h(ProposalNameCell, {
          proposal: context.row.original,
          expanded: expandedIds.value.includes(context.row.original.id),
          onToggle: () => toggleDetail(context.row.original),
        }),
    },
    {
      id: 'kind',
      meta: { className: 'w-[10%] whitespace-normal' },
      header: t('proposals.columns.kind'),
      accessorFn: (row) => codeLabel('kind', row.kind),
      sortFn: 'text',
      cell: (context) => h('span', { class: 'whitespace-normal' }, context.getValue<string>()),
    },
    {
      id: 'decision',
      meta: { className: 'w-[12%] whitespace-normal' },
      header: t('proposals.columns.decision'),
      accessorFn: (row) => decisionRank(row.decision),
      sortFn: 'basic',
      cell: (context) => {
        const row = context.row.original
        return h('span', { class: 'inline-flex flex-wrap gap-1' }, [
          h(StatusPill, {
            tone: DECISION_TONES[row.decision],
            labelKey: `codes.decision.${row.decision}`,
          }),
          row.current ? undefined : h(StatusPill, { tone: 'warn', labelKey: 'proposals.stale' }),
        ])
      },
    },
  ]
  if (hasSource.value) {
    list.push({
      id: 'source',
      meta: { className: 'w-[8%] whitespace-normal' },
      header: t('proposals.columns.source'),
      accessorFn: (row) => row.source ?? '',
      sortFn: 'text',
    })
  }
  list.push(
    {
      id: 'verified',
      meta: { className: 'w-[8%] whitespace-normal' },
      header: t('proposals.columns.verified'),
      accessorFn: (row) => row.details.percent,
      sortFn: 'basic',
      cell: (context) => formatPercent(context.row.original.details.percent),
    },
    {
      id: 'missing',
      meta: { className: 'w-[12%] whitespace-normal' },
      header: t('proposals.columns.missing'),
      accessorFn: (row) => row.details.missing_pieces,
      sortFn: 'basic',
      cell: (context) =>
        t('proposals.missingOf', {
          missing: context.row.original.details.missing_pieces,
          total: context.row.original.details.total_pieces,
        }),
    },
    {
      id: 'group',
      meta: { className: 'w-[8%] whitespace-normal' },
      header: t('proposals.columns.group'),
      accessorFn: (row) => row.group_id,
      sortFn: 'basic',
      cell: (context) => {
        const row = context.row.original
        if (!row.primary) return String(row.group_id)
        return h('span', [
          `${row.group_id} `,
          h('span', { 'aria-hidden': 'true' }, PRIMARY_MARK),
          h('span', { class: 'sr-only' }, t('proposals.primary')),
        ])
      },
    },
    {
      id: 'step',
      meta: { className: 'w-[10%] whitespace-normal' },
      header: t('proposals.columns.step'),
      accessorFn: (row) => (row.step ? codeLabel('state', row.step) : EMPTY_CELL),
      sortFn: 'text',
      cell: (context) => h('span', { class: 'whitespace-normal' }, context.getValue<string>()),
    },
    {
      id: 'updated',
      meta: { className: 'w-[9%] whitespace-normal' },
      header: t('proposals.columns.updated'),
      accessorFn: (row) => Date.parse(row.updated_at) || 0,
      sortFn: 'basic',
      cell: (context) => {
        const updatedAt = context.row.original.updated_at
        if (!updatedAt) return EMPTY_CELL
        return h(
          'time',
          { datetime: updatedAt, title: formatDateTime(updatedAt) },
          formatRelative(updatedAt),
        )
      },
    },
    {
      id: 'warnings',
      meta: { className: 'w-[6%] whitespace-normal' },
      header: () =>
        h('span', { title: t('proposals.columns.warnings') }, [
          h(TriangleAlert, { class: 'size-4', 'aria-hidden': 'true' }),
          h('span', { class: 'sr-only' }, t('proposals.columns.warnings')),
        ]),
      accessorFn: (row) => row.warnings.length,
      sortFn: 'basic',
    },
  )
  return list
})

function rowKey(row: Proposal): string {
  return String(row.id)
}

async function load(): Promise<void> {
  isLoading.value = true
  const groupId = Number(filters.group)
  const list = await call(() =>
    api.listProposals({
      kind: filters.kind || undefined,
      decision: filters.decision || undefined,
      group: groupId > 0 ? groupId : undefined,
      current_only: filters.currentOnly || undefined,
    }),
  )
  isLoading.value = false
  loadFailed.value = list === undefined
  // La déduplication et la réparation ont leurs propres vues.
  if (list) proposals.value = list.filter((row) => REVIEW_KINDS.includes(row.kind))
}

async function act(task: () => Promise<unknown>, success: string): Promise<void> {
  if ((await call(task)) !== undefined) toast.success(success)
  await load()
}

// Un filtre serveur modifié relance la requête ; le groupe, saisi au clavier, n'est lu qu'à la validation (v-model.lazy).
watch(() => [filters.kind, filters.decision, filters.group, filters.currentOnly], load)

const selectedKeys = ref<string[]>([])
// Remonter le tableau remet sa sélection à zéro après un rejet groupé.
const tableVersion = ref(0)
const selectedRows = computed(() =>
  visibleRows.value.filter((row) => isSelectable(row) && selectedKeys.value.includes(rowKey(row))),
)

const rejectTargets = ref<Proposal[]>([])
const rejectOpen = ref(false)

function rejectLine(target: Proposal): string {
  return t('proposals.rejectItem', {
    name: target.candidate_name,
    kind: codeLabel('kind', target.kind),
  })
}

function askReject(): void {
  rejectTargets.value = selectedRows.value
  rejectOpen.value = true
}

// Une requête par proposition, envoyée seulement après la confirmation ; un échec n'arrête pas les autres.
async function rejectTargetsOneByOne(): Promise<void> {
  let rejectedCount = 0
  for (const target of rejectTargets.value) {
    if ((await call(() => api.reject(target.id))) !== undefined) rejectedCount += 1
  }
  if (rejectedCount > 0)
    toast.success(t('proposals.toasts.rejectedCount', { count: rejectedCount }))
  selectedKeys.value = []
  tableVersion.value += 1
  await load()
}

const partialTarget = ref<Proposal>()
const partialOpen = ref(false)

function accept(row: Proposal): void {
  if (row.kind !== 'partial_pack') {
    void act(() => api.accept(row.id), t('proposals.toasts.accepted'))
    return
  }
  partialTarget.value = row
  partialOpen.value = true
}

const forceTarget = ref<Proposal>()
const forceOpen = ref(false)

function askForce(row: Proposal): void {
  forceTarget.value = row
  forceOpen.value = true
}

const groupTarget = ref<Proposal>()
const groupItems = ref<string[]>([])
const groupSummaryKey = ref('proposals.group.summaryOther')
const groupMode = ref('')
const groupOpen = ref(false)

// Le consentement montre ce que le serveur acceptera : les membres du groupe sont relus sans les filtres de la vue.
async function askGroup(row: Proposal): Promise<void> {
  const members = await call(() => api.listProposals({ group: row.group_id, current_only: true }))
  const status = await call(() => api.status())
  if (members === undefined || status === undefined) return
  const isNative = status.cross_seed_mode === 'native'
  groupTarget.value = row
  groupMode.value = status.cross_seed_mode
  groupSummaryKey.value = isNative
    ? 'proposals.group.summaryNative'
    : 'proposals.group.summaryOther'
  groupItems.value = groupTargets(members, isNative).map((member) => {
    if (member.primary) return `${PRIMARY_MARK} ${member.candidate_name}`
    return member.kind === 'cross_seed'
      ? t('proposals.group.nativeOnly', { name: member.candidate_name })
      : member.candidate_name
  })
  groupOpen.value = true
}

async function acceptGroup(): Promise<void> {
  const target = groupTarget.value
  if (!target) return
  const result = await call(() => api.acceptGroup(target.group_id))
  if (result !== undefined)
    toast.success(t('proposals.toasts.groupCount', { count: result.accepted.length }))
  await load()
}

const chooseTarget = ref<Proposal>()

function choose(row: Proposal, candidateId: number): void {
  chooseTarget.value = undefined
  void act(() => api.choose(row.id, candidateId), t('proposals.toasts.chosen'))
}

onMounted(load)
</script>

<template>
  <section class="grid min-w-0 grid-cols-[minmax(0,1fr)] gap-4">
    <PageToolbar>
      <template #actions>
        <Button
          variant="outline"
          data-action="proposals list"
          data-scope="toolbar"
          :disabled="pending"
          @click="load"
        >
          <RefreshCw aria-hidden="true" /> {{ t('proposals.refresh') }}
        </Button>
        <Button
          variant="outline"
          data-action="proposals reject"
          data-scope="bulk"
          :disabled="pending || selectedRows.length === 0"
          @click="askReject"
        >
          <Trash2 aria-hidden="true" />
          {{ t('proposals.actions.rejectSelection', { count: selectedRows.length }) }}
        </Button>
      </template>
      <template #filters>
        <select
          v-model="filters.kind"
          :class="TOOLBAR_FIELD_CLASS"
          :aria-label="t('proposals.filters.kind')"
          data-testid="filter-kind"
        >
          <option value="">
            {{ allOf('proposals.filters.kind') }}
          </option>
          <option v-for="(label, kind) in KIND_LABELS" :key="kind" :value="kind">
            {{ label }}
          </option>
        </select>
        <select
          v-model="filters.decision"
          :class="TOOLBAR_FIELD_CLASS"
          :aria-label="t('proposals.filters.decision')"
          data-testid="filter-decision"
        >
          <option value="">
            {{ allOf('proposals.filters.decision') }}
          </option>
          <option v-for="(label, decision) in DECISION_LABELS" :key="decision" :value="decision">
            {{ label }}
          </option>
        </select>
        <select
          v-if="hasSource"
          v-model="filters.source"
          :class="TOOLBAR_FIELD_CLASS"
          :aria-label="t('proposals.filters.source')"
          data-testid="filter-source"
        >
          <option value="">
            {{ allOf('proposals.filters.source') }}
          </option>
          <option v-for="source in sources" :key="source" :value="source">{{ source }}</option>
        </select>
        <input
          v-model.lazy="filters.group"
          type="number"
          min="1"
          inputmode="numeric"
          :class="cn(TOOLBAR_FIELD_CLASS, 'w-28')"
          :aria-label="t('proposals.filters.group')"
          :placeholder="t('proposals.filters.group')"
          data-testid="filter-group"
        />
        <label class="flex items-center gap-2 text-sm">
          <input
            v-model="filters.currentOnly"
            type="checkbox"
            class="size-4 accent-primary"
            data-testid="filter-current"
          />
          {{ t('proposals.filters.currentOnly') }}
        </label>
        <input
          v-model="filters.search"
          type="search"
          :class="cn(TOOLBAR_FIELD_CLASS, 'w-56')"
          :aria-label="t('proposals.filters.search')"
          :placeholder="t('proposals.filters.search')"
          data-testid="filter-search"
        />
      </template>
    </PageToolbar>

    <DataTable
      :key="tableVersion"
      caption-key="proposals.caption"
      caption-hidden
      :columns="columns"
      :rows="visibleRows"
      :row-key="rowKey"
      :initial-sort="{ id: 'decision', desc: false }"
      :selectable="isSelectable"
      :loading="isLoading"
      :error="loadFailed ? t('proposals.loadFailed') : undefined"
      empty-key="proposals.empty"
      @select="selectedKeys = $event"
      @open="toggleDetail"
    >
      <template #card="{ row }">
        <ProposalNameCell
          :proposal="row"
          :expanded="expandedIds.includes(row.id)"
          @toggle="toggleDetail(row)"
        />
        <dl class="mt-2 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-sm">
          <dt class="text-muted-foreground">{{ t('proposals.columns.kind') }}</dt>
          <dd>{{ codeLabel('kind', row.kind) }}</dd>
          <dt class="text-muted-foreground">{{ t('proposals.columns.decision') }}</dt>
          <dd class="flex flex-wrap gap-1">
            <StatusPill
              :tone="DECISION_TONES[row.decision]"
              :label-key="`codes.decision.${row.decision}`"
            />
            <StatusPill v-if="!row.current" tone="warn" label-key="proposals.stale" />
          </dd>
          <dt v-if="row.source" class="text-muted-foreground">
            {{ t('proposals.columns.source') }}
          </dt>
          <dd v-if="row.source">{{ row.source }}</dd>
          <dt class="text-muted-foreground">{{ t('proposals.columns.verified') }}</dt>
          <dd>{{ formatPercent(row.details.percent) }}</dd>
          <dt class="text-muted-foreground">{{ t('proposals.columns.missing') }}</dt>
          <dd>
            {{
              t('proposals.missingOf', {
                missing: row.details.missing_pieces,
                total: row.details.total_pieces,
              })
            }}
          </dd>
          <dt class="text-muted-foreground">{{ t('proposals.columns.group') }}</dt>
          <dd>{{ row.group_id }}{{ row.primary ? ` ${PRIMARY_MARK}` : '' }}</dd>
          <dt class="text-muted-foreground">{{ t('proposals.columns.step') }}</dt>
          <dd>{{ row.step ? codeLabel('state', row.step) : EMPTY_CELL }}</dd>
          <dt class="text-muted-foreground">{{ t('proposals.columns.updated') }}</dt>
          <dd>
            <time
              v-if="row.updated_at"
              :datetime="row.updated_at"
              :title="formatDateTime(row.updated_at)"
              >{{ formatRelative(row.updated_at) }}</time
            >
            <template v-else>{{ EMPTY_CELL }}</template>
          </dd>
          <dt class="text-muted-foreground">{{ t('proposals.columns.warnings') }}</dt>
          <dd>{{ row.warnings.length }}</dd>
        </dl>
      </template>
      <template #row-actions="{ row }">
        <div class="flex flex-wrap justify-end gap-1 py-1">
          <RowActionButton
            :icon="Check"
            :label="t('proposals.actions.accept')"
            :accessible-name="t('proposals.actions.acceptOf', { name: row.candidate_name })"
            data-action="proposals accept"
            data-scope="row"
            :disabled="pending || !canAccept(row)"
            @click="accept(row)"
          />
          <RowActionButton
            :icon="X"
            variant="outline"
            :label="t('proposals.actions.reject')"
            :accessible-name="t('proposals.actions.rejectOf', { name: row.candidate_name })"
            data-action="proposals reject"
            data-scope="row"
            :disabled="pending || !canReject(row)"
            @click="act(() => api.reject(row.id), t('proposals.toasts.rejected'))"
          />
          <RowActionButton
            v-if="row.kind === 'conflict'"
            :icon="ListChecks"
            variant="secondary"
            :label="t('proposals.actions.choose')"
            :accessible-name="t('proposals.actions.chooseOf', { name: row.candidate_name })"
            data-action="proposals choose"
            data-scope="row"
            :disabled="pending || !canChoose(row)"
            @click="chooseTarget = row"
          />
          <RowActionButton
            v-if="row.primary"
            :icon="CheckCheck"
            variant="secondary"
            :label="t('proposals.actions.acceptGroup')"
            :accessible-name="t('proposals.actions.acceptGroupOf', { name: row.candidate_name })"
            data-action="proposals accept-group"
            data-scope="row"
            :disabled="pending || !canAcceptGroup(row)"
            @click="askGroup(row)"
          />
          <RowActionButton
            v-if="row.kind === 'partial_annex'"
            :icon="Zap"
            variant="destructive"
            :label="t('proposals.actions.force')"
            :accessible-name="t('proposals.actions.forceOf', { name: row.candidate_name })"
            data-action="proposals force"
            data-scope="row"
            :disabled="pending || !canForce(row)"
            @click="askForce(row)"
          />
        </div>
      </template>
    </DataTable>

    <ChooseDialog :proposal="chooseTarget" @choose="choose" @close="chooseTarget = undefined" />
    <ConfirmDialog
      v-model:open="rejectOpen"
      :title="t('proposals.reject.title')"
      :description="t('proposals.reject.description')"
      :confirm-label="t('proposals.reject.confirm')"
      summary-key="proposals.reject.summary"
      :items="rejectTargets.map(rejectLine)"
      @confirm="rejectTargetsOneByOne"
    />
    <ConfirmDialog
      v-model:open="groupOpen"
      :title="t('proposals.group.title', { group: groupTarget?.group_id ?? '' })"
      :description="t('proposals.group.description')"
      :confirm-label="t('proposals.group.confirm')"
      :summary-key="groupSummaryKey"
      :summary-params="{ mode: groupMode }"
      :items="groupItems"
      @confirm="acceptGroup"
    />
    <ConfirmDialog
      v-model:open="forceOpen"
      :title="t('proposals.force.title')"
      :description="t('proposals.force.description')"
      :confirm-label="t('proposals.force.confirm')"
      summary-key="proposals.force.summary"
      :items="forceTarget ? [forceTarget.candidate_name] : []"
      :confirm-phrase="t('proposals.force.phrase')"
      @confirm="forceTarget && act(() => api.force(forceTarget!.id), t('proposals.toasts.forced'))"
    />
    <ConfirmDialog
      v-model:open="partialOpen"
      :title="t('proposals.partial.title')"
      :description="t('proposals.partial.description')"
      :confirm-label="t('proposals.partial.confirm')"
      summary-key="proposals.partial.summary"
      :items="partialTarget ? [partialTarget.candidate_name] : []"
      @confirm="
        partialTarget &&
        act(() => api.accept(partialTarget!.id, true), t('proposals.toasts.partial'))
      "
    />
  </section>
</template>
