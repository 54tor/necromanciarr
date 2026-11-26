<script setup lang="ts">
import { Trash2 } from '@lucide/vue'
import { computed, h } from 'vue'
import type { ConfigOrphan } from '@/api/types'
import DataTable from '@/components/DataTable.vue'
import RowActionButton from '@/components/RowActionButton.vue'
import StatusPill from '@/components/StatusPill.vue'
import { t } from '@/i18n'
import type { DataColumn } from '@/lib/dataTable'
import { wrapCell } from '@/lib/tableCells'

defineProps<{ orphans: ConfigOrphan[]; disabled: boolean }>()
const emit = defineEmits<{ remove: [orphan: ConfigOrphan] }>()

const orphanBadge = () =>
  h(StatusPill, { 'data-testid': 'config-orphan', tone: 'fail', labelKey: 'config.orphan' })

const columns = computed<DataColumn<ConfigOrphan>[]>(() => [
  {
    id: 'list',
    header: t('config.columns.list'),
    accessorFn: (orphan) => t(`config.lists.${orphan.List}`),
    enableSorting: false,
  },
  {
    id: 'itemId',
    header: t('config.columns.itemId'),
    accessorFn: (orphan) => orphan.ItemID,
    enableSorting: false,
    cell: (context) => wrapCell(context.row.original.ItemID),
  },
  {
    id: 'state',
    header: t('config.columns.origin'),
    accessorFn: () => 'orphan',
    enableSorting: false,
    cell: orphanBadge,
  },
])

const rowKey = (orphan: ConfigOrphan) => `${orphan.List}:${orphan.ItemID}`
</script>

<template>
  <section class="grid min-w-0 grid-cols-[minmax(0,1fr)] gap-2" aria-labelledby="config-orphans">
    <h2 id="config-orphans" class="text-base font-semibold">{{ t('config.orphans') }}</h2>
    <p class="text-sm text-muted-foreground">{{ t('config.orphansHint') }}</p>
    <DataTable
      caption-key="config.orphans"
      caption-hidden
      :columns="columns"
      :rows="orphans"
      :row-key="rowKey"
      empty-key="config.emptyOrphans"
    >
      <template #card="{ row }">
        <p class="font-medium [overflow-wrap:anywhere]">{{ row.ItemID }}</p>
        <div class="mt-2 flex flex-wrap gap-1">
          <StatusPill tone="muted" :label-key="`config.lists.${row.List}`" />
          <StatusPill data-testid="config-orphan" tone="fail" label-key="config.orphan" />
        </div>
      </template>
      <template #row-actions="{ row }">
        <div class="flex justify-end py-1">
          <RowActionButton
            :icon="Trash2"
            variant="destructive"
            :label="t('config.remove.action')"
            :accessible-name="t('config.remove.actionOf', { name: row.ItemID })"
            data-action="config item-delete"
            data-scope="row"
            :data-testid="`delete-orphan-${row.List}-${row.ItemID}`"
            :disabled="disabled"
            @click="emit('remove', row)"
          />
        </div>
      </template>
    </DataTable>
  </section>
</template>
