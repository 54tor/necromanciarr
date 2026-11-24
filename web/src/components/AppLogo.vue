<script setup lang="ts">
import { t } from '@/i18n'

const productName = 'Necromanciarr'
/** `full` : logo complet (barre latérale large) ; `emblem` : emblème seul, avec le nom à côté sauf en `iconOnly`. */
defineProps<{ variant?: 'full' | 'emblem'; iconOnly?: boolean }>()
</script>

<template>
  <img
    v-if="variant === 'full'"
    src="/branding/logo-complet.webp"
    :alt="t('nav.logoAlt')"
    width="144"
    height="144"
    decoding="async"
    class="mx-auto size-36"
  />
  <span v-else class="flex items-center gap-2.5">
    <img
      src="/branding/embleme.webp"
      :alt="iconOnly ? t('nav.logoAlt') : ''"
      width="32"
      height="32"
      decoding="async"
      class="size-8 shrink-0"
    />
    <span v-if="!iconOnly" class="text-lg font-semibold tracking-tight">{{ productName }}</span>
  </span>
</template>
