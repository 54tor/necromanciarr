<script setup lang="ts">
import { t } from '@/i18n'

const productName = 'Necromanciarr'
defineProps<{ iconOnly?: boolean }>()
</script>

<template>
  <span class="flex items-center gap-2.5">
    <svg
      class="size-8 shrink-0 text-primary"
      viewBox="0 0 32 32"
      fill="none"
      stroke="currentColor"
      stroke-width="2"
      stroke-linecap="round"
      stroke-linejoin="round"
      role="img"
      :aria-label="t('nav.logo')"
    >
      <path d="M16 3a10 10 0 0 0-10 10v6l3 2v5h4v-3h6v3h4v-5l3-2v-6A10 10 0 0 0 16 3Z" />
      <circle cx="12" cy="14" r="1.8" />
      <circle cx="20" cy="14" r="1.8" />
    </svg>
    <span :class="iconOnly ? 'sr-only' : 'text-lg font-semibold tracking-tight'">{{
      productName
    }}</span>
  </span>
</template>
