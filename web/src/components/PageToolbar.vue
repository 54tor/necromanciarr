<script setup lang="ts">
import { t } from '@/i18n'
</script>

<template>
  <div role="group" :aria-label="t('common.toolbar')" class="flex flex-wrap items-center gap-2">
    <div data-slot="toolbar-actions" class="flex flex-wrap items-center gap-2">
      <slot name="actions" />
    </div>
    <div data-slot="toolbar-filters" class="ml-auto flex flex-wrap items-center gap-2">
      <slot name="filters" />
    </div>
  </div>
</template>
