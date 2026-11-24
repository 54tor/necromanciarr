<script setup lang="ts">
import { t } from '@/i18n'

defineProps<{ texts: string[] }>()
const EMPTY_CELL = '—'
</script>

<template>
  <details v-if="texts.length > 0" class="py-1 text-xs whitespace-normal">
    <summary
      class="cursor-pointer rounded-sm focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
    >
      {{ t('dedupe.warningsCount', { count: texts.length }) }}
    </summary>
    <ul class="list-disc pl-4 [overflow-wrap:anywhere]">
      <li v-for="(text, position) in texts" :key="position">{{ text }}</li>
    </ul>
  </details>
  <span v-else>{{ EMPTY_CELL }}</span>
</template>
