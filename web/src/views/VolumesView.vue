<script setup lang="ts">
import { Copy, Radar, RefreshCw, Terminal } from '@lucide/vue'
import { computed, h, onMounted, ref } from 'vue'
import { toast } from 'vue-sonner'
import { api } from '@/api/endpoints'
import type {
  VolumeCapabilities,
  VolumeCommands,
  VolumeProbeResult,
  VolumeRow,
  VolumeState,
} from '@/api/types'
import DataTable from '@/components/DataTable.vue'
import PageToolbar from '@/components/PageToolbar.vue'
import RowActionButton from '@/components/RowActionButton.vue'
import StatusPill from '@/components/StatusPill.vue'
import { Button } from '@/components/ui/button'
import { useApi } from '@/composables/useApi'
import { codeLabel, t } from '@/i18n'
import type { DataColumn } from '@/lib/dataTable'
import { formatDateTime } from '@/lib/format'
import { FIELD_CLASS } from '@/lib/styles'
import { EMPTY_CELL } from '@/lib/tableCells'
import { cn } from '@/lib/utils'

const { call, pending } = useApi()
const rows = ref<VolumeRow[]>([])
const loading = ref(false)
const loadFailed = ref(false)
const chosenVolume = ref('')
const probingVolume = ref('')
const lastResult = ref<VolumeProbeResult>()
const shownCommands = ref<VolumeCommands>()

const STATE_TONES: Record<VolumeState, 'ok' | 'fail' | 'muted'> = {
  ok: 'ok',
  broken: 'fail',
  unknown: 'muted',
}
// Ordre d'affichage des pastilles, celui de la vue de la spécification.
const CAPABILITY_NAMES = [
  'symlink',
  'hardlink',
  'atomic_replace',
  'server_copy',
  'identity',
] as const satisfies readonly (keyof VolumeCapabilities)[]
const TOOLBAR_FIELD_CLASS = cn(FIELD_CLASS, 'w-auto')

const probeableVolumes = computed(() =>
  rows.value.filter((row) => row.type === 'sftp').map((row) => row.volume),
)
const isProbing = computed(() => probingVolume.value !== '')

function volumeName(row: VolumeRow): string {
  return row.volume === '' ? t('volumes.local') : row.volume
}

function probedAtText(row: VolumeRow): string {
  if (row.type === 'local') return EMPTY_CELL
  return row.probed_at ? formatDateTime(row.probed_at) : t('volumes.never')
}

function resultText(result: VolumeProbeResult): string {
  if (result.result === 'ok') return t('volumes.passed', { volume: result.volume })
  return t('volumes.failed', {
    volume: result.volume,
    reason: codeLabel('reason', result.error ?? ''),
  })
}

// Chaque pastille porte son texte (fonction et oui/non) : la couleur n'est jamais la seule information.
function capabilityPills(row: VolumeRow) {
  if (row.type === 'local') return EMPTY_CELL
  const capabilities = row.capabilities
  if (!capabilities) return t('volumes.notProbed')
  return h(
    'div',
    { class: 'flex flex-wrap gap-1 py-1' },
    CAPABILITY_NAMES.map((name) =>
      h(StatusPill, {
        tone: capabilities[name] ? 'ok' : 'muted',
        labelKey: `volumes.capability.${name}.${capabilities[name] ? 'yes' : 'no'}`,
        'data-capability': name,
      }),
    ),
  )
}

const columns = computed<DataColumn<VolumeRow>[]>(() => [
  {
    id: 'volume',
    header: t('volumes.columns.volume'),
    accessorFn: volumeName,
    enableSorting: false,
  },
  {
    id: 'type',
    header: t('volumes.columns.type'),
    accessorFn: (row) => t(`volumes.types.${row.type}`),
    enableSorting: false,
  },
  {
    id: 'address',
    header: t('volumes.columns.address'),
    accessorFn: (row) => row.address || EMPTY_CELL,
    enableSorting: false,
  },
  {
    id: 'state',
    header: t('volumes.columns.state'),
    accessorFn: (row) => row.state,
    enableSorting: false,
    cell: (context) =>
      h(StatusPill, {
        tone: STATE_TONES[context.row.original.state] ?? 'muted',
        labelKey: `volumes.state.${context.row.original.state}`,
      }),
  },
  {
    id: 'capabilities',
    header: t('volumes.columns.capabilities'),
    accessorFn: (row) => row.volume,
    enableSorting: false,
    cell: (context) => capabilityPills(context.row.original),
  },
  {
    id: 'probed_at',
    header: t('volumes.columns.probedAt'),
    accessorFn: probedAtText,
    enableSorting: false,
  },
])

function rowKey(row: VolumeRow): string {
  return `${row.type}:${row.volume}`
}

async function load(): Promise<void> {
  loading.value = true
  const listed = await call(() => api.listVolumes())
  loading.value = false
  loadFailed.value = listed === undefined
  if (listed) rows.value = listed
}

async function probeVolume(volume: string): Promise<void> {
  if (volume === '' || isProbing.value) return
  probingVolume.value = volume
  lastResult.value = undefined
  const result = await call(() => api.probeVolume(volume))
  probingVolume.value = ''
  if (result) lastResult.value = result
  await load()
}

async function showCommands(volume: string): Promise<void> {
  const commands = await call(() => api.volumeCommands(volume))
  if (commands) shownCommands.value = commands
}

// Presse-papiers absent (page hors HTTPS, navigateur qui refuse) : l'opérateur copie le bloc à la main.
async function copyCommands(commands: VolumeCommands): Promise<void> {
  try {
    await navigator.clipboard.writeText(commands.lines.join('\n') + '\n')
  } catch {
    toast.error(t('volumes.copyFailed'))
    return
  }
  toast.success(t('volumes.copied'))
}

onMounted(load)
</script>

<template>
  <section class="grid min-w-0 grid-cols-[minmax(0,1fr)] gap-4">
    <PageToolbar>
      <template #actions>
        <select
          v-model="chosenVolume"
          :class="TOOLBAR_FIELD_CLASS"
          :aria-label="t('volumes.choose')"
          data-testid="probe-choice"
        >
          <option value="" disabled>{{ t('volumes.choose') }}</option>
          <option v-for="volume in probeableVolumes" :key="volume" :value="volume">
            {{ volume }}
          </option>
        </select>
        <Button
          data-action="volumes probe"
          data-scope="toolbar"
          data-testid="probe-chosen"
          :disabled="chosenVolume === '' || isProbing"
          @click="probeVolume(chosenVolume)"
        >
          <Radar aria-hidden="true" /> {{ t('volumes.probe') }}
        </Button>
        <Button
          variant="outline"
          data-action="volumes list"
          data-scope="toolbar"
          data-testid="refresh"
          :disabled="pending"
          @click="load"
        >
          <RefreshCw aria-hidden="true" /> {{ t('volumes.refresh') }}
        </Button>
      </template>
    </PageToolbar>

    <p class="text-sm" aria-live="polite" data-testid="probe-result">
      <template v-if="isProbing">{{ t('volumes.probing', { volume: probingVolume }) }}</template>
      <template v-else-if="lastResult">{{ resultText(lastResult) }}</template>
    </p>

    <DataTable
      caption-key="volumes.caption"
      caption-hidden
      :columns="columns"
      :rows="rows"
      :row-key="rowKey"
      :loading="loading"
      :error="loadFailed ? t('volumes.loadFailed') : undefined"
      empty-key="volumes.empty"
    >
      <template #card="{ row }">
        <p class="font-medium [overflow-wrap:anywhere]">{{ volumeName(row) }}</p>
        <dl class="mt-2 grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-sm">
          <dt class="text-muted-foreground">{{ t('volumes.columns.type') }}</dt>
          <dd>{{ t(`volumes.types.${row.type}`) }}</dd>
          <dt class="text-muted-foreground">{{ t('volumes.columns.address') }}</dt>
          <dd class="[overflow-wrap:anywhere]">{{ row.address || EMPTY_CELL }}</dd>
          <dt class="text-muted-foreground">{{ t('volumes.columns.state') }}</dt>
          <dd>
            <StatusPill
              :tone="STATE_TONES[row.state] ?? 'muted'"
              :label-key="`volumes.state.${row.state}`"
            />
          </dd>
          <dt class="text-muted-foreground">{{ t('volumes.columns.capabilities') }}</dt>
          <dd>
            <component :is="() => capabilityPills(row)" />
          </dd>
          <dt class="text-muted-foreground">{{ t('volumes.columns.probedAt') }}</dt>
          <dd>{{ probedAtText(row) }}</dd>
        </dl>
      </template>
      <template #row-actions="{ row }">
        <div v-if="row.type === 'sftp'" class="flex justify-end gap-1 py-1">
          <RowActionButton
            :icon="Radar"
            variant="outline"
            :label="t('volumes.probeRow')"
            :accessible-name="t('volumes.probeRowOf', { volume: row.volume })"
            data-action="volumes probe"
            data-scope="row"
            :data-testid="`probe-${row.volume}`"
            :disabled="isProbing"
            @click="probeVolume(row.volume)"
          />
          <RowActionButton
            :icon="Terminal"
            variant="outline"
            :label="t('volumes.commandsRow')"
            :accessible-name="t('volumes.commandsRowOf', { volume: row.volume })"
            data-action="volumes commands"
            data-scope="row"
            :data-testid="`commands-${row.volume}`"
            @click="showCommands(row.volume)"
          />
        </div>
      </template>
    </DataTable>

    <section
      v-if="shownCommands"
      class="grid gap-2 rounded-md border p-4"
      aria-labelledby="volume-commands-title"
      data-testid="commands"
    >
      <h2 id="volume-commands-title" class="text-sm font-medium">
        {{ t('volumes.commandsTitle', { volume: shownCommands.volume }) }}
      </h2>
      <p class="text-sm text-muted-foreground">
        {{ t('volumes.commandsHint', { script: shownCommands.guard_script }) }}
      </p>
      <pre
        class="overflow-x-auto rounded bg-muted p-3 text-xs [tab-size:8]"
        data-testid="commands-lines"
        >{{ shownCommands.lines.join('\n') }}</pre>
      <div>
        <Button
          variant="outline"
          size="sm"
          data-testid="copy-commands"
          @click="copyCommands(shownCommands)"
        >
          <Copy aria-hidden="true" /> {{ t('volumes.copy') }}
        </Button>
      </div>
    </section>
  </section>
</template>
