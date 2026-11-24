<script setup lang="ts">
import { RefreshCw } from '@lucide/vue'
import { computed, h, onMounted } from 'vue'
import type { DoctorCheck, DoctorStatus } from '@/api/types'
import DataTable from '@/components/DataTable.vue'
import PageToolbar from '@/components/PageToolbar.vue'
import StatusPill from '@/components/StatusPill.vue'
import { Button } from '@/components/ui/button'
import { useOnboarding } from '@/composables/useOnboarding'
import { t } from '@/i18n'
import { groupByFamily } from '@/lib/doctorFamily'
import type { DataColumn } from '@/lib/dataTable'
import {
  CANDIDATE_LABELS,
  DECISION_LABELS,
  HELD_REASON_LABELS,
  KIND_LABELS,
  STEP_LABELS,
  labelOf,
} from '@/lib/labels'
import { warningText } from '@/lib/warnings'
import { navigate } from '@/router'

const { checks, status, worst, firstLaunch, pending, loadAll, rerunCheck, refreshStatus, skip } =
  useOnboarding()

onMounted(() => {
  if (checks.value.length === 0) void loadAll()
})

function skipOnboarding(): void {
  skip()
  navigate('/propositions')
}

const PILL_TONES = { ok: 'ok', warn: 'warn', fail: 'fail', skipped: 'muted' } as const
const EMPTY_CELL = '—'

function pillOf(checkStatus: DoctorStatus) {
  return h(StatusPill, { tone: PILL_TONES[checkStatus], labelKey: `codes.doctor.${checkStatus}` })
}

// Détail et conseil sont déjà rendus dans la langue du serveur : affichés tels quels, jamais retraduits.
function wrappedText(text: string) {
  return h(
    'span',
    { class: 'block max-w-prose py-1.5 whitespace-normal [overflow-wrap:anywhere]' },
    text || EMPTY_CELL,
  )
}

const columns = computed<DataColumn<DoctorCheck>[]>(() => [
  {
    id: 'title',
    header: t('onboarding.columns.check'),
    accessorFn: (check) => check.title,
    enableSorting: false,
    cell: (context) => wrappedText(context.row.original.title),
  },
  {
    id: 'status',
    header: t('onboarding.columns.status'),
    accessorFn: (check) => check.status,
    enableSorting: false,
    cell: (context) => pillOf(context.row.original.status),
  },
  {
    id: 'detail',
    header: t('onboarding.columns.detail'),
    accessorFn: (check) => check.detail,
    enableSorting: false,
    cell: (context) => wrappedText(context.row.original.detail),
  },
  {
    id: 'hint',
    header: t('onboarding.columns.hint'),
    accessorFn: (check) => check.hint ?? '',
    enableSorting: false,
    cell: (context) => wrappedText(context.row.original.hint ?? ''),
  },
  {
    id: 'duration',
    header: t('onboarding.columns.duration'),
    accessorFn: (check) => check.durationMs,
    enableSorting: false,
    cell: (context) => t('onboarding.durationMs', { ms: context.row.original.durationMs }),
  },
])

const families = computed(() => groupByFamily(checks.value))

const STATE_GROUPS = [
  { key: 'candidates', labels: CANDIDATE_LABELS },
  { key: 'proposals', labels: DECISION_LABELS },
  { key: 'steps', labels: STEP_LABELS },
] as const

function checkKey(check: DoctorCheck): string {
  return check.id
}
</script>

<template>
  <section
    class="grid gap-6 [&_table]:table-fixed [&_th:first-child]:w-[20%] [&_th:nth-child(2)]:w-36 [&_th:nth-child(3)]:w-[28%] [&_th:nth-child(5)]:w-20 [&_th:last-child]:w-28"
  >
    <PageToolbar>
      <template #actions>
        <Button data-action="doctor" data-scope="toolbar" :disabled="pending" @click="loadAll">
          <RefreshCw aria-hidden="true" /> {{ t('onboarding.runAll') }}
        </Button>
        <Button
          variant="outline"
          data-action="status"
          data-scope="toolbar"
          :disabled="pending"
          @click="refreshStatus"
        >
          {{ t('onboarding.state.refresh') }}
        </Button>
        <Button
          v-if="worst === 'fail' || firstLaunch"
          variant="link"
          data-scope="toolbar"
          data-testid="skip"
          @click="skipOnboarding"
        >
          {{ t('onboarding.skip') }}
        </Button>
      </template>
    </PageToolbar>

    <p v-if="firstLaunch && worst !== 'fail'" class="rounded-lg border bg-card p-4">
      {{ t('onboarding.firstLaunch') }}
    </p>
    <p
      v-if="worst === 'fail'"
      class="rounded-lg border border-destructive bg-card p-4"
      role="alert"
    >
      {{ t('onboarding.failBanner') }}
    </p>

    <p v-if="checks.length === 0 && !pending" class="text-muted-foreground">
      {{ t('onboarding.noChecks') }}
    </p>
    <DataTable
      v-for="group in families"
      :key="group.family"
      :caption-key="`onboarding.family.${group.family}`"
      :columns="columns"
      :rows="group.checks"
      :row-key="checkKey"
      empty-key="onboarding.noChecks"
    >
      <template #row-actions="{ row }">
        <Button
          size="sm"
          variant="outline"
          data-action="doctor run"
          data-scope="row"
          :aria-label="t('onboarding.rerunCheck', { title: row.title })"
          :disabled="pending"
          @click="rerunCheck(row.id)"
        >
          {{ t('onboarding.rerun') }}
        </Button>
      </template>
    </DataTable>

    <section class="grid gap-3 rounded-lg border bg-card p-4" aria-labelledby="state-title">
      <h2 id="state-title" class="font-medium">{{ t('onboarding.state.title') }}</h2>
      <p v-if="!status" class="text-sm text-muted-foreground">
        {{ t('onboarding.state.unavailable') }}
      </p>
      <dl v-else class="grid gap-2 text-sm sm:grid-cols-2">
        <div v-for="group in STATE_GROUPS" :key="group.key">
          <dt class="font-medium">{{ t(`onboarding.state.${group.key}`) }}</dt>
          <dd class="text-muted-foreground">
            <template v-if="Object.keys(status[group.key]).length === 0">
              {{ t('onboarding.state.none') }}
            </template>
            <span
              v-for="(count, state) in status[group.key]"
              :key="state"
              class="mr-3 whitespace-nowrap"
            >
              {{ t('common.labelValue', { label: labelOf(group.labels, state), value: count }) }}
            </span>
          </dd>
        </div>
        <div>
          <dt class="font-medium">{{ t('onboarding.state.crossSeed') }}</dt>
          <dd class="text-muted-foreground">{{ status.cross_seed_mode }}</dd>
        </div>
        <div v-if="(status.held ?? []).length > 0" class="sm:col-span-2" data-held>
          <dt class="font-medium">{{ t('onboarding.state.held') }}</dt>
          <dd class="text-muted-foreground">
            <p v-for="held in status.held" :key="held.candidate_id">
              {{
                t('onboarding.state.heldLine', {
                  name: held.candidate_name,
                  kind: labelOf(KIND_LABELS, held.kind),
                  missing: held.missing_pieces,
                  total: held.total_pieces,
                  reason: labelOf(HELD_REASON_LABELS, held.reason),
                })
              }}
            </p>
          </dd>
        </div>
        <div
          v-for="(warning, position) in status.warnings ?? []"
          :key="position"
          class="sm:col-span-2"
          role="status"
        >
          {{ t('onboarding.state.warning', { text: warningText(warning) }) }}
        </div>
      </dl>
    </section>
  </section>
</template>
