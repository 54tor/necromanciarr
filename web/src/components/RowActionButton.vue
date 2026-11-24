<script setup lang="ts">
import {
  TooltipContent,
  TooltipPortal,
  TooltipProvider,
  TooltipRoot,
  TooltipTrigger,
} from 'reka-ui'
import type { Component } from 'vue'
import { Button } from '@/components/ui/button'

defineOptions({ inheritAttrs: false })
/**
 * Action de ligne : icône seule dans le tableau large (info-bulle au survol et au focus clavier, nom accessible),
 * icône et libellé dans les cartes.
 */
defineProps<{
  icon: Component
  label: string
  accessibleName: string
  variant?: 'default' | 'outline' | 'secondary' | 'destructive'
}>()
</script>

<template>
  <TooltipProvider :delay-duration="300">
    <TooltipRoot>
      <TooltipTrigger as-child>
        <Button
          v-bind="$attrs"
          size="xs"
          :variant="variant"
          :aria-label="accessibleName"
          class="md:w-6 md:px-0"
        >
          <component :is="icon" aria-hidden="true" />
          <span class="md:sr-only">{{ label }}</span>
        </Button>
      </TooltipTrigger>
      <TooltipPortal>
        <TooltipContent
          side="top"
          :side-offset="4"
          class="z-50 rounded-md border border-border bg-card px-2 py-1 text-xs text-card-foreground shadow-md"
        >
          {{ label }}
        </TooltipContent>
      </TooltipPortal>
    </TooltipRoot>
  </TooltipProvider>
</template>
