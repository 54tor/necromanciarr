<script setup lang="ts">
import { computed } from 'vue'
import type { RepairDetails } from '@/api/types'
import StatusPill from '@/components/StatusPill.vue'
import { codeLabel, t } from '@/i18n'

const props = defineProps<{ details: RepairDetails }>()

const candidates = computed(() => props.details.candidates ?? [])
const isAmbiguous = computed(() => props.details.status === 'ambiguous')
// Un lien introuvable ou ambigu s'explique par son statut ; un recontrôle client attendu par son motif.
const explanation = computed(() => {
  if (props.details.reason === 'needs_client_recheck')
    return t('links.repairs.applicable.needsClientRecheck')
  if (props.details.status === 'lost') return codeLabel('reason', 'lost')
  return ''
})
</script>

<template>
  <div data-testid="applicable" class="grid gap-1 py-1 whitespace-normal">
    <StatusPill
      :tone="details.actionable ? 'ok' : 'muted'"
      :label-key="
        details.actionable ? 'links.repairs.applicable.yes' : 'links.repairs.applicable.no'
      "
    />
    <p v-if="explanation" class="text-xs text-muted-foreground [overflow-wrap:anywhere]">
      {{ explanation }}
    </p>
    <details v-if="isAmbiguous && candidates.length > 0" class="text-xs">
      <summary
        class="cursor-pointer rounded-sm focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
      >
        {{ t('links.repairs.applicable.ambiguous', { count: candidates.length }) }}
      </summary>
      <ul
        :aria-label="t('links.repairs.applicable.candidates')"
        class="list-disc pl-4 [overflow-wrap:anywhere]"
      >
        <li v-for="path in candidates" :key="path">{{ path }}</li>
      </ul>
    </details>
    <p v-else-if="isAmbiguous" class="text-xs text-muted-foreground">
      {{ codeLabel('reason', 'ambiguous') }}
    </p>
  </div>
</template>
