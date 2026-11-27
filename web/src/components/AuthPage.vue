<script setup lang="ts">
import AppLogo from '@/components/AppLogo.vue'
import { t } from '@/i18n'

defineProps<{ titleKey: string; descriptionKey: string }>()
</script>

<template>
  <main
    class="grid min-h-screen w-full content-center gap-8 bg-background px-[clamp(16px,3vw,56px)] py-8 text-foreground lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)] lg:items-center"
  >
    <header class="grid content-start justify-items-start gap-4">
      <AppLogo variant="emblem" />
      <h1 class="text-2xl font-semibold tracking-tight">{{ t(titleKey) }}</h1>
      <p class="text-muted-foreground">{{ t(descriptionKey) }}</p>
    </header>
    <div class="grid min-w-0 gap-4 rounded-lg border bg-card p-[clamp(16px,2vw,32px)]">
      <slot />
    </div>
  </main>
</template>
