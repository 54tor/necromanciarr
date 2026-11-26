<script setup lang="ts">
import { Lock } from '@lucide/vue'
import { computed, type Component } from 'vue'
import type { ConfigOrigin } from '@/api/types'
import StatusPill from '@/components/StatusPill.vue'

const props = defineProps<{ origin: ConfigOrigin }>()

type OriginTone = 'ok' | 'warn' | 'info' | 'muted'

// Un réglage dit `crossseed`, un élément de liste `cross-seed` : même libellé.
const ORIGIN_LABELS: Record<ConfigOrigin, string> = {
  default: 'default',
  crossseed: 'crossseed',
  'cross-seed': 'crossseed',
  override: 'override',
  added: 'added',
  env: 'env',
  flag: 'flag',
}
const ORIGIN_TONES: Record<ConfigOrigin, OriginTone> = {
  default: 'muted',
  crossseed: 'info',
  'cross-seed': 'info',
  override: 'ok',
  added: 'ok',
  env: 'warn',
  flag: 'warn',
}
const LOCKING_ORIGINS: ConfigOrigin[] = ['env', 'flag']

const labelKey = computed(() => `config.origin.${ORIGIN_LABELS[props.origin] ?? props.origin}`)
const tone = computed(() => ORIGIN_TONES[props.origin] ?? 'muted')
const icon = computed<Component | undefined>(() =>
  LOCKING_ORIGINS.includes(props.origin) ? Lock : undefined,
)
</script>

<template>
  <StatusPill data-testid="config-origin" :tone="tone" :label-key="labelKey" :icon="icon" />
</template>
