<script setup lang="ts">
import { CircleCheck, CircleQuestionMark, OctagonX, TriangleAlert } from '@lucide/vue'
import type { Component } from 'vue'
import type { LinkState } from '@/api/types'
import StatusPill from '@/components/StatusPill.vue'

defineProps<{ state: LinkState }>()

// Pastille = icône + texte + couleur : l'état ne repose jamais sur la couleur seule.
const ICONS: Record<LinkState, Component> = {
  ok: CircleCheck,
  diverged: TriangleAlert,
  lost: OctagonX,
  unreadable: CircleQuestionMark,
}
const TONES = { ok: 'ok', diverged: 'warn', lost: 'fail', unreadable: 'muted' } as const
</script>

<template>
  <StatusPill
    :tone="TONES[state]"
    :icon="ICONS[state]"
    :label-key="`codes.state.${state}`"
    :data-state="state"
  />
</template>
