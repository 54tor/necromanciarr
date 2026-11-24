<script setup lang="ts">
import { CircleCheck, CircleMinus, OctagonX, TriangleAlert } from '@lucide/vue'
import type { Component } from 'vue'
import type { DoctorStatus } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { DOCTOR_LABELS } from '@/lib/labels'

const props = defineProps<{ status: DoctorStatus }>()

// Pastille = icône + texte + couleur : l'état ne repose jamais sur la couleur seule.
const ICONS: Record<DoctorStatus, Component> = {
  ok: CircleCheck,
  warn: TriangleAlert,
  fail: OctagonX,
  skipped: CircleMinus,
}
const VARIANTS = {
  ok: 'outline',
  warn: 'outline',
  fail: 'destructive',
  skipped: 'secondary',
} as const
const TONES: Record<DoctorStatus, string> = {
  ok: 'border-primary text-primary',
  warn: 'border-chart-4 text-chart-4',
  fail: '',
  skipped: '',
}
</script>

<template>
  <Badge :variant="VARIANTS[props.status]" :class="TONES[props.status]" :data-status="props.status">
    <component :is="ICONS[props.status]" aria-hidden="true" />
    {{ DOCTOR_LABELS[props.status] }}
  </Badge>
</template>
