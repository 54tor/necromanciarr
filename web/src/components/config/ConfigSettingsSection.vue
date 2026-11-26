<script setup lang="ts">
import { Pencil, Undo2 } from '@lucide/vue'
import { computed, h } from 'vue'
import type { ConfigSetting } from '@/api/types'
import ConfigOriginBadge from '@/components/config/ConfigOriginBadge.vue'
import DataTable from '@/components/DataTable.vue'
import RowActionButton from '@/components/RowActionButton.vue'
import StatusPill from '@/components/StatusPill.vue'
import { t } from '@/i18n'
import { displayValue } from '@/lib/configFields'
import type { DataColumn } from '@/lib/dataTable'
import { wrapCell } from '@/lib/tableCells'

export interface SettingRow extends ConfigSetting {
  key: string
}

defineProps<{ rows: SettingRow[]; disabled: boolean; loading: boolean; error?: string }>()
const emit = defineEmits<{ edit: [row: SettingRow]; unset: [row: SettingRow] }>()

const OVERRIDE_ORIGIN = 'override'

function valueText(row: SettingRow): string {
  if (!row.secret) return displayValue(row.value)
  return row.set ? t('config.secret.set') : t('config.secret.unset')
}

function originCell(row: SettingRow) {
  const badges = [h(ConfigOriginBadge, { origin: row.origin })]
  if (row.locked)
    badges.push(
      h(StatusPill, { 'data-testid': 'config-locked', tone: 'warn', labelKey: 'config.locked' }),
    )
  return h('span', { class: 'flex flex-wrap gap-1' }, badges)
}

const columns = computed<DataColumn<SettingRow>[]>(() => [
  {
    id: 'key',
    header: t('config.columns.key'),
    accessorFn: (row) => row.key,
    enableSorting: false,
    cell: (context) => wrapCell(context.row.original.key),
  },
  {
    id: 'value',
    header: t('config.columns.value'),
    accessorFn: valueText,
    enableSorting: false,
    cell: (context) => wrapCell(valueText(context.row.original)),
  },
  {
    id: 'origin',
    header: t('config.columns.origin'),
    accessorFn: (row) => row.origin,
    enableSorting: false,
    cell: (context) => originCell(context.row.original),
  },
])

const rowKey = (row: SettingRow) => row.key
</script>

<template>
  <section class="grid min-w-0 grid-cols-[minmax(0,1fr)] gap-2" aria-labelledby="config-settings">
    <h2 id="config-settings" class="text-base font-semibold">{{ t('config.settings') }}</h2>
    <DataTable
      caption-key="config.settings"
      caption-hidden
      :columns="columns"
      :rows="rows"
      :row-key="rowKey"
      :loading="loading"
      :error="error"
      empty-key="config.emptySettings"
    >
      <template #card="{ row }">
        <p class="font-medium [overflow-wrap:anywhere]">{{ row.key }}</p>
        <p class="mt-1 text-sm [overflow-wrap:anywhere]">{{ valueText(row) }}</p>
        <div class="mt-2 flex flex-wrap gap-1">
          <ConfigOriginBadge :origin="row.origin" />
          <StatusPill
            v-if="row.locked"
            data-testid="config-locked"
            tone="warn"
            label-key="config.locked"
          />
        </div>
      </template>
      <template #row-actions="{ row }">
        <div class="flex justify-end gap-1 py-1">
          <RowActionButton
            :icon="Pencil"
            variant="outline"
            :label="t('config.edit')"
            :accessible-name="t('config.editOf', { name: row.key })"
            data-action="config set"
            data-scope="row"
            :data-testid="`edit-setting-${row.key}`"
            :disabled="disabled"
            @click="emit('edit', row)"
          />
          <RowActionButton
            v-if="row.origin === OVERRIDE_ORIGIN"
            :icon="Undo2"
            variant="outline"
            :label="t('config.revert')"
            :accessible-name="t('config.revertOf', { name: row.key })"
            data-action="config unset"
            data-scope="row"
            :data-testid="`unset-setting-${row.key}`"
            :disabled="disabled"
            @click="emit('unset', row)"
          />
        </div>
      </template>
    </DataTable>
  </section>
</template>
