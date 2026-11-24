<script setup lang="ts">
import { LoaderCircle, Play } from '@lucide/vue'
import { computed, h, onMounted, ref } from 'vue'
import { api } from '@/api/endpoints'
import type { Job, StageName } from '@/api/types'
import DataTable from '@/components/DataTable.vue'
import PageToolbar from '@/components/PageToolbar.vue'
import StatusPill from '@/components/StatusPill.vue'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { useJobs } from '@/composables/useJobs'
import { t } from '@/i18n'
import type { DataColumn } from '@/lib/dataTable'
import { formatDateTime } from '@/lib/format'
import { counterLines, reasonLines, stageResultOf, summaryText } from '@/lib/stageSummary'
import { warningText } from '@/lib/warnings'

const { jobs, runningJob, busy, unreachable, start, refresh } = useJobs()

const STAGES: readonly StageName[] = ['scan', 'match', 'verify', 'link', 'seed']
const EMPTY_CELL = '—'
const STATE_TONES = { running: 'info', done: 'ok', failed: 'fail' } as const

function actionLabel(job: Job): string {
  const result = stageResultOf(job)
  return result ? t(`stages.names.${result.stage}`) : job.action
}

const columns = computed<DataColumn<Job>[]>(() => [
  {
    id: 'id',
    header: t('stages.columns.job'),
    accessorFn: (job) => job.id,
    cell: (context) => t('stages.jobNumber', { id: context.row.original.id }),
  },
  {
    id: 'action',
    header: t('stages.columns.action'),
    accessorFn: actionLabel,
    enableSorting: false,
  },
  {
    id: 'started_at',
    header: t('stages.columns.start'),
    accessorFn: (job) => Date.parse(job.started_at),
    cell: (context) => formatDateTime(context.row.original.started_at),
  },
  {
    id: 'finished_at',
    header: t('stages.columns.end'),
    accessorFn: (job) => job.finished_at ?? '',
    enableSorting: false,
    cell: (context) =>
      context.row.original.finished_at
        ? formatDateTime(context.row.original.finished_at)
        : EMPTY_CELL,
  },
  {
    id: 'state',
    header: t('stages.columns.state'),
    accessorFn: (job) => job.state,
    enableSorting: false,
    cell: (context) =>
      h(StatusPill, {
        tone: STATE_TONES[context.row.original.state],
        labelKey: `stages.state.${context.row.original.state}`,
      }),
  },
  {
    id: 'summary',
    header: t('stages.columns.summary'),
    accessorFn: summaryText,
    enableSorting: false,
    cell: (context) =>
      h(
        'span',
        { class: 'block max-w-prose py-1.5 whitespace-normal' },
        summaryText(context.row.original),
      ),
  },
])

const shownJob = ref<Job>()
const resultOpen = computed({
  get: () => shownJob.value !== undefined,
  set: (isOpen: boolean) => {
    if (!isOpen) shownJob.value = undefined
  },
})
const shownResult = computed(() => (shownJob.value ? stageResultOf(shownJob.value) : undefined))

function jobKey(job: Job): string {
  return String(job.id)
}

onMounted(refresh)
</script>

<template>
  <section class="grid gap-4">
    <PageToolbar>
      <template #actions>
        <Button
          v-for="stage in STAGES"
          :key="stage"
          data-action="run"
          data-scope="toolbar"
          :data-stage="stage"
          :disabled="busy"
          :aria-label="t('stages.runStage', { title: t(`stages.names.${stage}`) })"
          @click="start(() => api.runStage(stage))"
        >
          <Play aria-hidden="true" /> {{ t(`stages.names.${stage}`) }}
        </Button>
      </template>
    </PageToolbar>
    <p class="text-sm text-muted-foreground" aria-live="polite" data-testid="job-state">
      <template v-if="unreachable">{{ t('stages.unknown') }}</template>
      <template v-else-if="runningJob">
        <LoaderCircle
          class="mr-1 inline size-4 animate-spin motion-reduce:animate-none"
          aria-hidden="true"
        />
        {{ t('stages.running', { action: runningJob.action, id: runningJob.id }) }}
      </template>
      <template v-else>{{ t('stages.idle') }}</template>
    </p>
    <p
      v-if="unreachable"
      role="alert"
      class="rounded-lg border border-destructive bg-card p-3 text-sm"
      data-testid="unreachable"
    >
      {{ t('stages.unreachable') }}
    </p>

    <DataTable
      caption-key="stages.caption"
      caption-hidden
      :columns="columns"
      :rows="jobs"
      :row-key="jobKey"
      :initial-sort="{ id: 'started_at', desc: true }"
      empty-key="stages.empty"
    >
      <template #row-actions="{ row }">
        <Button
          size="sm"
          variant="outline"
          data-scope="row"
          data-testid="job-result"
          :aria-label="t('stages.resultOf', { id: row.id })"
          @click="shownJob = row"
        >
          {{ t('stages.result') }}
        </Button>
      </template>
    </DataTable>

    <Dialog v-model:open="resultOpen">
      <DialogContent v-if="shownJob">
        <DialogHeader>
          <DialogTitle>{{ t('stages.resultTitle', { id: shownJob.id }) }}</DialogTitle>
          <DialogDescription>
            {{
              shownResult
                ? t('stages.resultDescription', { stage: t(`stages.names.${shownResult.stage}`) })
                : t('stages.noResult')
            }}
          </DialogDescription>
        </DialogHeader>
        <template v-if="shownResult">
          <ul class="grid gap-1 text-sm">
            <li v-for="line in counterLines(shownResult)" :key="line.label">
              {{ t('common.labelValue', { label: line.label, value: line.value }) }}
            </li>
          </ul>
          <div v-if="reasonLines(shownResult).length > 0">
            <p class="text-sm font-medium">{{ t('stages.reasons') }}</p>
            <ul class="list-disc pl-5 text-sm">
              <li v-for="line in reasonLines(shownResult)" :key="line.label">
                {{ t('common.labelValue', { label: line.label, value: line.value }) }}
              </li>
            </ul>
          </div>
          <div v-if="(shownResult.warnings ?? []).length > 0">
            <p class="text-sm font-medium">{{ t('stages.warnings') }}</p>
            <ul class="list-disc pl-5 text-sm">
              <li v-for="(warning, position) in shownResult.warnings" :key="position">
                {{ warningText(warning) }}
              </li>
            </ul>
          </div>
        </template>
        <DialogFooter>
          <Button variant="outline" @click="resultOpen = false">{{ t('common.close') }}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </section>
</template>
