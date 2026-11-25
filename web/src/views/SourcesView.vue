<script setup lang="ts">
import { FlaskConical, RefreshCw } from '@lucide/vue'
import { computed, h, onMounted, ref } from 'vue'
import { api } from '@/api/endpoints'
import type { SourceRow, SourceState, SourceTestResult, SourceUsage } from '@/api/types'
import DataTable from '@/components/DataTable.vue'
import PageToolbar from '@/components/PageToolbar.vue'
import RowActionButton from '@/components/RowActionButton.vue'
import StatusPill from '@/components/StatusPill.vue'
import { Button } from '@/components/ui/button'
import { useApi } from '@/composables/useApi'
import { codeLabel, t } from '@/i18n'
import type { DataColumn } from '@/lib/dataTable'
import { FIELD_CLASS } from '@/lib/styles'
import { EMPTY_CELL } from '@/lib/tableCells'
import { cn } from '@/lib/utils'

const { call, pending } = useApi()
const rows = ref<SourceRow[]>([])
const loading = ref(false)
const loadFailed = ref(false)
const chosenSource = ref('')
const testingSource = ref('')
const lastResult = ref<SourceTestResult>()

const STATE_TONES: Record<SourceState, 'ok' | 'warn' | 'fail' | 'muted'> = {
  ok: 'ok',
  blocked: 'warn',
  exhausted: 'warn',
  disabled: 'muted',
}
const TOOLBAR_FIELD_CLASS = cn(FIELD_CLASS, 'w-auto')

// Le fournisseur se contrôle par le diagnostic : seuls les indexeurs se testent ici.
const testableSources = computed(() =>
  rows.value.filter((row) => row.type === 'torznab').map((row) => row.source),
)
const isTesting = computed(() => testingSource.value !== '')

function usageText(usage: SourceUsage | null): string {
  return usage
    ? t('sources.usage', { used: usage.used, cap: usage.cap })
    : t('sources.notApplicable')
}

function resultText(result: SourceTestResult): string {
  if (result.result === 'ok') return t('sources.passed', { source: result.source })
  return t('sources.failed', {
    source: result.source,
    reason: codeLabel('reason', result.error ?? ''),
  })
}

const columns = computed<DataColumn<SourceRow>[]>(() => [
  {
    id: 'order',
    header: t('sources.columns.order'),
    accessorFn: (row) => row.order,
    enableSorting: false,
    cell: (context) =>
      context.row.original.order > 0 ? context.row.original.order : t('sources.outsideChain'),
  },
  {
    id: 'source',
    header: t('sources.columns.source'),
    accessorFn: (row) => row.source,
    enableSorting: false,
  },
  {
    id: 'type',
    header: t('sources.columns.type'),
    accessorFn: (row) => t(`sources.types.${row.type}`),
    enableSorting: false,
  },
  {
    id: 'tracker',
    header: t('sources.columns.tracker'),
    accessorFn: (row) => row.tracker || EMPTY_CELL,
    enableSorting: false,
  },
  {
    id: 'state',
    header: t('sources.columns.state'),
    accessorFn: (row) => row.state,
    enableSorting: false,
    cell: (context) =>
      h(StatusPill, {
        tone: STATE_TONES[context.row.original.state] ?? 'muted',
        labelKey: `sources.state.${context.row.original.state}`,
      }),
  },
  {
    id: 'queries',
    header: t('sources.columns.queries'),
    accessorFn: (row) => usageText(row.queries),
    enableSorting: false,
  },
  {
    id: 'downloads',
    header: t('sources.columns.downloads'),
    accessorFn: (row) => usageText(row.downloads),
    enableSorting: false,
  },
  {
    id: 'last_error',
    header: t('sources.columns.lastError'),
    accessorFn: (row) => (row.last_error ? codeLabel('reason', row.last_error) : EMPTY_CELL),
    enableSorting: false,
  },
])

function rowKey(row: SourceRow): string {
  return row.source
}

async function load(): Promise<void> {
  loading.value = true
  const listed = await call(() => api.listSources())
  loading.value = false
  loadFailed.value = listed === undefined
  if (listed) rows.value = listed
}

async function testSource(source: string): Promise<void> {
  if (source === '' || isTesting.value) return
  testingSource.value = source
  const result = await call(() => api.testSource(source))
  testingSource.value = ''
  if (result) lastResult.value = result
  await load()
}

onMounted(load)
</script>

<template>
  <section class="grid min-w-0 grid-cols-[minmax(0,1fr)] gap-4">
    <PageToolbar>
      <template #actions>
        <select
          v-model="chosenSource"
          :class="TOOLBAR_FIELD_CLASS"
          :aria-label="t('sources.choose')"
          data-testid="test-choice"
        >
          <option value="" disabled>{{ t('sources.choose') }}</option>
          <option v-for="source in testableSources" :key="source" :value="source">
            {{ source }}
          </option>
        </select>
        <Button
          data-action="sources test"
          data-scope="toolbar"
          data-testid="test-chosen"
          :disabled="chosenSource === '' || isTesting"
          @click="testSource(chosenSource)"
        >
          <FlaskConical aria-hidden="true" /> {{ t('sources.test') }}
        </Button>
        <Button
          variant="outline"
          data-action="sources list"
          data-scope="toolbar"
          data-testid="refresh"
          :disabled="pending"
          @click="load"
        >
          <RefreshCw aria-hidden="true" /> {{ t('sources.refresh') }}
        </Button>
      </template>
    </PageToolbar>

    <p class="text-sm" aria-live="polite" data-testid="test-result">
      <template v-if="isTesting">{{ t('sources.testing', { source: testingSource }) }}</template>
      <template v-else-if="lastResult">{{ resultText(lastResult) }}</template>
    </p>

    <DataTable
      caption-key="sources.caption"
      caption-hidden
      :columns="columns"
      :rows="rows"
      :row-key="rowKey"
      :loading="loading"
      :error="loadFailed ? t('sources.loadFailed') : undefined"
      empty-key="sources.empty"
    >
      <template #card="{ row }">
        <p class="font-medium [overflow-wrap:anywhere]">{{ row.source }}</p>
        <dl class="mt-2 grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-sm">
          <dt class="text-muted-foreground">{{ t('sources.columns.order') }}</dt>
          <dd>{{ row.order > 0 ? row.order : t('sources.outsideChain') }}</dd>
          <dt class="text-muted-foreground">{{ t('sources.columns.type') }}</dt>
          <dd>{{ t(`sources.types.${row.type}`) }}</dd>
          <dt class="text-muted-foreground">{{ t('sources.columns.tracker') }}</dt>
          <dd class="[overflow-wrap:anywhere]">{{ row.tracker || EMPTY_CELL }}</dd>
          <dt class="text-muted-foreground">{{ t('sources.columns.state') }}</dt>
          <dd>
            <StatusPill
              :tone="STATE_TONES[row.state] ?? 'muted'"
              :label-key="`sources.state.${row.state}`"
            />
          </dd>
          <dt class="text-muted-foreground">{{ t('sources.columns.queries') }}</dt>
          <dd>{{ usageText(row.queries) }}</dd>
          <dt class="text-muted-foreground">{{ t('sources.columns.downloads') }}</dt>
          <dd>{{ usageText(row.downloads) }}</dd>
          <dt class="text-muted-foreground">{{ t('sources.columns.lastError') }}</dt>
          <dd>{{ row.last_error ? codeLabel('reason', row.last_error) : EMPTY_CELL }}</dd>
        </dl>
      </template>
      <template #row-actions="{ row }">
        <div v-if="row.type === 'torznab'" class="flex justify-end py-1">
          <RowActionButton
            :icon="FlaskConical"
            variant="outline"
            :label="t('sources.testRow')"
            :accessible-name="t('sources.testRowOf', { source: row.source })"
            data-action="sources test"
            data-scope="row"
            :data-testid="`test-${row.source}`"
            :disabled="isTesting"
            @click="testSource(row.source)"
          />
        </div>
      </template>
    </DataTable>
  </section>
</template>
