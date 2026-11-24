<script setup lang="ts">
import { computed } from 'vue'
import type { DedupeDetails } from '@/api/types'
import StatusPill from '@/components/StatusPill.vue'
import { t } from '@/i18n'

const props = defineProps<{ details: DedupeDetails }>()

const ambiguousFiles = computed(() => props.details.ambiguous ?? [])
const reasonLabel = computed(() => props.details.label ?? props.details.reason)
</script>

<template>
  <div class="grid gap-1 py-1 whitespace-normal">
    <StatusPill
      :tone="details.actionable ? 'ok' : 'muted'"
      :label-key="details.actionable ? 'dedupe.applicable.yes' : 'dedupe.applicable.no'"
    />
    <p v-if="reasonLabel" class="text-xs text-muted-foreground [overflow-wrap:anywhere]">
      {{ reasonLabel }}
    </p>
    <details v-if="ambiguousFiles.length > 0" data-testid="ambiguous" class="text-xs">
      <summary
        class="cursor-pointer rounded-sm focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
      >
        {{ t('dedupe.applicable.ambiguous', { count: ambiguousFiles.length }) }}
      </summary>
      <ul
        :aria-label="t('dedupe.applicable.ambiguousFiles')"
        class="list-disc pl-4 [overflow-wrap:anywhere]"
      >
        <li v-for="path in ambiguousFiles" :key="path">{{ path }}</li>
      </ul>
    </details>
  </div>
</template>
