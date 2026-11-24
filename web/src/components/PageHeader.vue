<script setup lang="ts">
import { t } from '@/i18n'

defineProps<{ titleKey: string }>()
</script>

<template>
  <header class="grid gap-3 border-b border-border pb-3">
    <h1 class="text-xl font-semibold tracking-tight">{{ t(titleKey) }}</h1>
    <slot />
  </header>
</template>
