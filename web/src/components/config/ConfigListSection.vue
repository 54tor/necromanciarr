<script setup lang="ts">
import { Pencil, Plus, Trash2, Undo2 } from '@lucide/vue'
import { computed, h } from 'vue'
import type { ConfigItem } from '@/api/types'
import ConfigOriginBadge from '@/components/config/ConfigOriginBadge.vue'
import DataTable from '@/components/DataTable.vue'
import RowActionButton from '@/components/RowActionButton.vue'
import StatusPill from '@/components/StatusPill.vue'
import { Button } from '@/components/ui/button'
import { t } from '@/i18n'
import { displayValue, isSecretField } from '@/lib/configFields'
import type { DataColumn } from '@/lib/dataTable'
import { wrapCell } from '@/lib/tableCells'

const props = defineProps<{ list: string; items: ConfigItem[]; disabled: boolean }>()
const emit = defineEmits<{
  add: []
  edit: [item: ConfigItem]
  unset: [item: ConfigItem]
  remove: [item: ConfigItem]
}>()

const OVERRIDE_ORIGIN = 'override'

function fieldsText(item: ConfigItem): string {
  return Object.entries(item.element)
    .map(([name, value]) => {
      const shown = isSecretField(props.list, name) ? t('config.secret.set') : displayValue(value)
      return `${name}: ${shown}`
    })
    .join(' · ')
}

function originCell(item: ConfigItem) {
  const badges = [h(ConfigOriginBadge, { origin: item.origin })]
  if (item.drift)
    badges.push(
      h(StatusPill, { 'data-testid': 'config-drift', tone: 'warn', labelKey: 'config.drift' }),
    )
  return h('span', { class: 'flex flex-wrap gap-1' }, badges)
}

const columns = computed<DataColumn<ConfigItem>[]>(() => [
  {
    id: 'itemId',
    header: t('config.columns.itemId'),
    accessorFn: (item) => item.itemId,
    enableSorting: false,
    cell: (context) => wrapCell(context.row.original.itemId),
  },
  {
    id: 'fields',
    header: t('config.columns.fields'),
    accessorFn: fieldsText,
    enableSorting: false,
    cell: (context) => wrapCell(fieldsText(context.row.original)),
  },
  {
    id: 'origin',
    header: t('config.columns.origin'),
    accessorFn: (item) => item.origin,
    enableSorting: false,
    cell: (context) => originCell(context.row.original),
  },
])

const rowKey = (item: ConfigItem) => `${props.list}:${item.itemId}`
</script>

<template>
  <section class="grid min-w-0 grid-cols-[minmax(0,1fr)] gap-2" :aria-labelledby="`list-${list}`">
    <div class="flex flex-wrap items-center gap-2">
      <h2 :id="`list-${list}`" class="text-base font-semibold">
        {{ t(`config.lists.${list}`) }}
      </h2>
      <Button
        size="sm"
        variant="outline"
        class="ml-auto"
        data-action="config item-set"
        data-scope="toolbar"
        :data-testid="`add-item-${list}`"
        :disabled="disabled"
        @click="emit('add')"
      >
        <Plus aria-hidden="true" /> {{ t('config.add') }}
      </Button>
    </div>
    <DataTable
      :caption-key="`config.lists.${list}`"
      caption-hidden
      :columns="columns"
      :rows="items"
      :row-key="rowKey"
      empty-key="config.emptyList"
    >
      <template #card="{ row }">
        <p class="font-medium [overflow-wrap:anywhere]">{{ row.itemId }}</p>
        <p class="mt-1 text-sm [overflow-wrap:anywhere]">{{ fieldsText(row) }}</p>
        <div class="mt-2 flex flex-wrap gap-1">
          <ConfigOriginBadge :origin="row.origin" />
          <StatusPill
            v-if="row.drift"
            data-testid="config-drift"
            tone="warn"
            label-key="config.drift"
          />
        </div>
      </template>
      <template #row-actions="{ row }">
        <div class="flex justify-end gap-1 py-1">
          <RowActionButton
            :icon="Pencil"
            variant="outline"
            :label="t('config.edit')"
            :accessible-name="t('config.editOf', { name: row.itemId })"
            data-action="config item-set"
            data-scope="row"
            :data-testid="`edit-item-${list}-${row.itemId}`"
            :disabled="disabled"
            @click="emit('edit', row)"
          />
          <RowActionButton
            v-if="row.origin === OVERRIDE_ORIGIN"
            :icon="Undo2"
            variant="outline"
            :label="t('config.revert')"
            :accessible-name="t('config.revertOf', { name: row.itemId })"
            data-action="config item-unset"
            data-scope="row"
            :data-testid="`unset-item-${list}-${row.itemId}`"
            :disabled="disabled"
            @click="emit('unset', row)"
          />
          <RowActionButton
            :icon="Trash2"
            variant="destructive"
            :label="t('config.remove.action')"
            :accessible-name="t('config.remove.actionOf', { name: row.itemId })"
            data-action="config item-delete"
            data-scope="row"
            :data-testid="`delete-item-${list}-${row.itemId}`"
            :disabled="disabled"
            @click="emit('remove', row)"
          />
        </div>
      </template>
    </DataTable>
  </section>
</template>
