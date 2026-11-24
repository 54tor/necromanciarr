<script setup lang="ts">
import { CircleCheck, CircleMinus, CircleX, Info, TriangleAlert } from '@lucide/vue'
import { computed, type Component } from 'vue'
import { t } from '@/i18n'

type PillTone = 'ok' | 'warn' | 'fail' | 'info' | 'muted'

const props = defineProps<{ tone: PillTone; labelKey: string; icon?: Component }>()

const DEFAULT_ICONS: Record<PillTone, Component> = {
  ok: CircleCheck,
  warn: TriangleAlert,
  fail: CircleX,
  info: Info,
  muted: CircleMinus,
}
const TONE_CLASSES: Record<PillTone, string> = {
  ok: 'bg-state-ok-surface text-state-ok',
  warn: 'bg-state-warn-surface text-state-warn',
  fail: 'bg-state-fail-surface text-state-fail',
  info: 'bg-state-info-surface text-state-info',
  muted: 'bg-state-muted-surface text-state-muted',
}

const icon = computed(() => props.icon ?? DEFAULT_ICONS[props.tone])
</script>

<template>
  <span
    :data-tone="tone"
    class="inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium whitespace-nowrap"
    :class="TONE_CLASSES[tone]"
  >
    <component :is="icon" class="size-3.5 shrink-0" aria-hidden="true" />
    {{ t(labelKey) }}
  </span>
</template>
