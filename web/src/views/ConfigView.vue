<script setup lang="ts">
import { RefreshCw, RotateCw } from '@lucide/vue'
import { computed, onMounted, ref } from 'vue'
import type { ConfigItem } from '@/api/types'
import ConfigEditDialog from '@/components/config/ConfigEditDialog.vue'
import ConfigListSection from '@/components/config/ConfigListSection.vue'
import ConfigOrphansSection from '@/components/config/ConfigOrphansSection.vue'
import ConfigSettingsSection, {
  type SettingRow,
} from '@/components/config/ConfigSettingsSection.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import PageToolbar from '@/components/PageToolbar.vue'
import { Button } from '@/components/ui/button'
import { useConfig } from '@/composables/useConfig'
import { t } from '@/i18n'
import {
  CONFIG_LISTS,
  addFields,
  elementFields,
  idHintKey,
  itemIdDeriver,
  settingField,
  type FormField,
} from '@/lib/configFields'

interface Editor {
  title: string
  fields: FormField[]
  itemIdHintKey?: string
  deriveItemId?: (fields: readonly FormField[]) => string
  locked: boolean
  keepBlank?: boolean
  save: (values: Record<string, unknown>, itemId: string) => Promise<boolean>
}

interface Removal {
  list: string
  itemId: string
  descriptionKey: string
  restoresUpstream: boolean
}

const CROSS_SEED_ORIGIN = 'cross-seed'
const OVERRIDE_ORIGIN = 'override'

const config = useConfig()
const editorOpen = ref(false)
const editor = ref<Editor>()
const removalOpen = ref(false)
const removal = ref<Removal>()

const settingRows = computed<SettingRow[]>(() =>
  Object.entries(config.snapshot.value?.settings ?? {})
    .map(([key, setting]) => ({ key, ...setting }))
    .sort((left, right) => left.key.localeCompare(right.key)),
)
const itemsOf = (list: string) => config.snapshot.value?.lists?.[list] ?? []
const orphans = computed(() => config.snapshot.value?.orphans ?? [])
const isBusy = computed(() => config.pending.value)

function openEditor(next: Editor): void {
  editor.value = next
  editorOpen.value = true
}

function editSetting(row: SettingRow): void {
  openEditor({
    title: t('config.form.editTitle', { name: row.key }),
    fields: [settingField(row.key, row)],
    locked: row.locked,
    keepBlank: true,
    save: async (values) => {
      if (!(row.key in values)) return true
      return config.saveSetting(row.key, values[row.key])
    },
  })
}

async function saveElement(list: string, itemId: string, values: Record<string, unknown>) {
  return config.saveItem(list, itemId, values)
}

function editItem(list: string, item: ConfigItem): void {
  openEditor({
    title: t('config.form.editTitle', { name: item.itemId }),
    fields: elementFields(list, item.element),
    locked: false,
    save: (values) => saveElement(list, item.itemId, values),
  })
}

function addItem(list: string): void {
  openEditor({
    title: t('config.form.addTitle', { list: t(`config.lists.${list}`) }),
    fields: addFields(list),
    itemIdHintKey: idHintKey(list),
    deriveItemId: itemIdDeriver(list),
    locked: false,
    save: (values, itemId) => saveElement(list, itemId, values),
  })
}

function askRemoval(
  list: string,
  itemId: string,
  descriptionKey: string,
  restoresUpstream = false,
): void {
  removal.value = { list, itemId, descriptionKey, restoresUpstream }
  removalOpen.value = true
}

function askItemRemoval(list: string, item: ConfigItem): void {
  if (item.origin === OVERRIDE_ORIGIN) {
    askRemoval(list, item.itemId, 'config.remove.revert', true)
    return
  }
  const hidesUpstream = item.origin === CROSS_SEED_ORIGIN
  askRemoval(list, item.itemId, hidesUpstream ? 'config.remove.hide' : 'config.remove.delete')
}

async function confirmRemoval(): Promise<void> {
  if (!removal.value) return
  const { list, itemId, restoresUpstream } = removal.value
  if (restoresUpstream) {
    await config.unsetItem(list, itemId)
    return
  }
  await config.deleteItem(list, itemId)
}

async function submitEditor(values: Record<string, unknown>, itemId: string): Promise<boolean> {
  return (await editor.value?.save(values, itemId)) ?? false
}

onMounted(config.load)
</script>

<template>
  <section class="grid min-w-0 grid-cols-[minmax(0,1fr)] gap-6">
    <PageToolbar>
      <template #actions>
        <Button
          variant="outline"
          data-action="config get"
          data-scope="toolbar"
          data-testid="refresh"
          :disabled="isBusy"
          @click="config.load"
        >
          <RefreshCw aria-hidden="true" /> {{ t('config.refresh') }}
        </Button>
      </template>
    </PageToolbar>

    <p
      v-if="config.restartRequired.value"
      role="status"
      data-testid="config-restart"
      class="flex items-center gap-2 rounded-md bg-state-warn-surface p-3 text-sm text-state-warn"
    >
      <RotateCw class="size-4 shrink-0" aria-hidden="true" /> {{ t('config.restartRequired') }}
    </p>

    <ConfigSettingsSection
      :rows="settingRows"
      :disabled="isBusy"
      :loading="config.loading.value"
      :error="config.loadFailed.value ? t('config.loadFailed') : undefined"
      @edit="editSetting"
      @unset="(row) => config.unsetSetting(row.key)"
    />

    <ConfigListSection
      v-for="list in CONFIG_LISTS"
      :key="list"
      :list="list"
      :items="itemsOf(list)"
      :disabled="isBusy"
      @add="addItem(list)"
      @edit="(item) => editItem(list, item)"
      @unset="(item) => config.unsetItem(list, item.itemId)"
      @remove="(item) => askItemRemoval(list, item)"
    />

    <ConfigOrphansSection
      :orphans="orphans"
      :disabled="isBusy"
      @remove="(orphan) => askRemoval(orphan.List, orphan.ItemID, 'config.remove.orphan', true)"
    />

    <ConfigEditDialog
      v-if="editor"
      v-model:open="editorOpen"
      :title="editor.title"
      :fields="editor.fields"
      :item-id-hint-key="editor.itemIdHintKey"
      :derive-item-id="editor.deriveItemId"
      :locked="editor.locked"
      :keep-blank="editor.keepBlank"
      :save="submitEditor"
    />
    <ConfirmDialog
      v-model:open="removalOpen"
      :title="t('config.remove.title')"
      :description="removal ? t(removal.descriptionKey) : ''"
      :confirm-label="t('config.remove.confirm')"
      summary-key="config.remove.summary"
      :summary-params="{ name: removal?.itemId ?? '' }"
      @confirm="confirmRemoval"
    />
  </section>
</template>
