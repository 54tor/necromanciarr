<script setup lang="ts">
import { CircleCheck, CircleQuestionMark, OctagonX, TriangleAlert } from '@lucide/vue'
import type { Component } from 'vue'
import type { LinkState } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { LINK_STATE_LABELS } from '@/lib/labels'

const props = defineProps<{ state: LinkState }>()

// Pastille = icône + texte + couleur : l'état ne repose jamais sur la couleur seule.
const ICONS: Record<LinkState, Component> = {
  ok: CircleCheck,
  diverged: TriangleAlert,
  lost: OctagonX,
  unreadable: CircleQuestionMark,
}
const VARIANTS = {
  ok: 'outline',
  diverged: 'outline',
  lost: 'destructive',
  unreadable: 'secondary',
} as const
const TONES: Record<LinkState, string> = {
  ok: 'border-primary text-primary',
  diverged: 'border-chart-4 text-chart-4',
  lost: '',
  unreadable: '',
}
</script>

<template>
  <Badge :variant="VARIANTS[props.state]" :class="TONES[props.state]" :data-state="props.state">
    <component :is="ICONS[props.state]" aria-hidden="true" />
    {{ LINK_STATE_LABELS[props.state] }}
  </Badge>
</template>
