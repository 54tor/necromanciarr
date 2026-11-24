<script setup lang="ts">
import { Search } from '@lucide/vue'
import { TabsContent, TabsList, TabsRoot, TabsTrigger } from 'reka-ui'
import { computed, onMounted, ref, watch } from 'vue'
import { api } from '@/api/endpoints'
import type { MaintenanceProposal, RepairDetails } from '@/api/types'
import PageToolbar from '@/components/PageToolbar.vue'
import RecordedLinks from '@/components/RecordedLinks.vue'
import RepairsPanel from '@/components/RepairsPanel.vue'
import { Button } from '@/components/ui/button'
import { lastFinished } from '@/composables/useJobs'
import { useMaintenance } from '@/composables/useMaintenance'
import { LINK_STATES, useRecordedLinks } from '@/composables/useRecordedLinks'
import { codeLabel, t } from '@/i18n'
import { FIELD_CLASS } from '@/lib/styles'
import { cn } from '@/lib/utils'

type TabId = 'links' | 'repairs'

const links = useRecordedLinks()
const { items, dryRun, busy, pending, loading, loadFailed, load, canApply, applyOne, start } =
  useMaintenance<RepairDetails>('repair', api.repairApply)

const directory = ref('')
const activeTab = ref<TabId>('links')
const isBlocked = computed(() => busy.value || pending.value)
const TOOLBAR_FIELD_CLASS = cn(FIELD_CLASS, 'w-auto')
const TAB_CLASS =
  'rounded-t-md border-b-2 border-transparent px-3 py-2 text-sm font-medium text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring data-[state=active]:border-primary data-[state=active]:text-foreground'

const scanRepairs = () => start(() => api.repairScan(directory.value.trim()))
const applyRepair = (item: MaintenanceProposal<RepairDetails>, dryRunChosen: boolean) =>
  void applyOne(item, dryRunChosen)

function allOf(): string {
  return t('common.labelValue', { label: t('links.filterState'), value: t('links.filterAll') })
}

watch(links.state, () => void links.reloadFirstPage())
watch(lastFinished, () => void links.load())

onMounted(() => {
  void links.load()
  void load()
})
</script>

<template>
  <section class="grid min-w-0 grid-cols-[minmax(0,1fr)] gap-4">
    <PageToolbar>
      <template #actions>
        <Button
          variant="outline"
          data-action="links list"
          data-scope="toolbar"
          data-testid="inspect"
          :disabled="links.pending.value"
          @click="links.reloadFirstPage()"
        >
          {{ t('links.inspect') }}
        </Button>
        <form
          class="flex flex-wrap items-center gap-2"
          data-testid="repair-scan-form"
          @submit.prevent="scanRepairs"
        >
          <input
            v-model="directory"
            type="text"
            autocomplete="off"
            :class="cn(TOOLBAR_FIELD_CLASS, 'min-w-56')"
            :aria-label="t('links.directory')"
            :placeholder="t('links.directory')"
            data-testid="scan-directory"
          />
          <Button
            type="submit"
            data-action="repair scan"
            data-scope="toolbar"
            data-testid="repair-scan"
            :disabled="isBlocked"
          >
            <Search aria-hidden="true" />
            {{ t('links.scan') }}
          </Button>
        </form>
        <label class="flex items-center gap-2 text-sm">
          <input
            v-model="dryRun"
            type="checkbox"
            class="size-4 accent-primary"
            data-testid="dry-run"
          />
          {{ t('links.dryRun') }}
        </label>
      </template>
      <template #filters>
        <select
          v-if="activeTab === 'links'"
          v-model="links.state.value"
          :class="TOOLBAR_FIELD_CLASS"
          :aria-label="t('links.filterState')"
          data-testid="link-state-filter"
        >
          <option value="">{{ allOf() }}</option>
          <option v-for="option in LINK_STATES" :key="option" :value="option">
            {{ codeLabel('state', option) }}
          </option>
        </select>
      </template>
    </PageToolbar>

    <TabsRoot v-model="activeTab" class="grid min-w-0 gap-3">
      <TabsList :aria-label="t('links.tabsLabel')" class="flex gap-1 border-b border-border">
        <TabsTrigger value="links" data-testid="tab-links" :class="TAB_CLASS">
          {{ t('links.tabs.links') }}
        </TabsTrigger>
        <TabsTrigger value="repairs" data-testid="tab-repairs" :class="TAB_CLASS">
          {{ t('links.tabs.repairs') }}
        </TabsTrigger>
      </TabsList>
      <TabsContent value="links" class="min-w-0 outline-none">
        <RecordedLinks
          :items="links.items.value"
          :page="links.page.value"
          :offset="links.offset.value"
          :loading="links.pending.value"
          :load-failed="links.loadFailed.value"
          :is-filtered="links.state.value !== ''"
          :busy="isBlocked"
          @page="links.goTo"
          @repair="scanRepairs"
        />
      </TabsContent>
      <TabsContent value="repairs" class="min-w-0 outline-none">
        <RepairsPanel
          :items="items"
          :loading="loading"
          :load-failed="loadFailed"
          :dry-run="dryRun"
          :blocked="isBlocked"
          :can-apply="canApply"
          @apply="applyRepair"
        />
      </TabsContent>
    </TabsRoot>
  </section>
</template>
