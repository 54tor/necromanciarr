<script setup lang="ts">
import type { Component } from 'vue'
import { Button } from '@/components/ui/button'

/** Action de ligne : icône seule dans le tableau large (infobulle et nom accessible), icône et libellé dans les cartes. */
defineProps<{
  icon: Component
  label: string
  accessibleName: string
  variant?: 'default' | 'outline' | 'secondary' | 'destructive'
}>()
</script>

<template>
  <Button
    size="xs"
    :variant="variant"
    :title="label"
    :aria-label="accessibleName"
    class="md:w-6 md:px-0"
  >
    <component :is="icon" aria-hidden="true" />
    <span class="md:sr-only">{{ label }}</span>
  </Button>
</template>
