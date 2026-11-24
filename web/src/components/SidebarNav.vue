<script setup lang="ts">
import { t } from '@/i18n'
import type { NavItem } from '@/lib/nav'
import { currentPath } from '@/router'

defineProps<{ items: readonly NavItem[]; compact?: boolean }>()
</script>

<template>
  <nav :aria-label="t('nav.label')">
    <ul class="grid gap-1">
      <li v-for="item in items" :key="item.fragment">
        <a
          :href="`#${item.fragment}`"
          :aria-current="currentPath === item.fragment ? 'page' : undefined"
          class="relative flex items-center gap-3 rounded-md border-l-2 border-transparent px-3 py-2 text-sm font-medium text-muted-foreground outline-none hover:bg-sidebar-accent hover:text-sidebar-accent-foreground focus-visible:ring-2 focus-visible:ring-ring aria-[current=page]:border-primary aria-[current=page]:bg-sidebar-accent aria-[current=page]:text-sidebar-accent-foreground"
          :class="compact ? 'justify-center px-0' : ''"
        >
          <component :is="item.icon" class="size-5 shrink-0" aria-hidden="true" />
          <span data-testid="nav-label" :class="compact ? 'sr-only' : ''">{{
            t(item.labelKey)
          }}</span>
          <span
            v-if="item.badge"
            data-testid="nav-badge"
            class="rounded-full bg-primary px-1.5 text-xs font-semibold text-primary-foreground"
            :class="compact ? 'absolute top-0.5 right-0.5 min-w-4 text-center' : 'ml-auto'"
            >{{ item.badge }}</span
          >
        </a>
      </li>
    </ul>
  </nav>
</template>
