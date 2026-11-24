<script setup lang="ts">
import { Menu, X } from '@lucide/vue'
import { useMediaQuery } from '@vueuse/core'
import {
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogOverlay,
  DialogPortal,
  DialogRoot,
  DialogTitle,
  DialogTrigger,
} from 'reka-ui'
import { computed, ref, useTemplateRef, watch } from 'vue'
import AppLogo from '@/components/AppLogo.vue'
import SidebarNav from '@/components/SidebarNav.vue'
import { t } from '@/i18n'
import type { NavItem } from '@/lib/nav'
import { currentPath } from '@/router'

defineProps<{ navItems: readonly NavItem[] }>()

// Seuils : barre complète dès 1024 px, icônes de 768 à 1023 px, tiroir en dessous.
const isWide = useMediaQuery('(min-width: 1024px)')
const isTablet = useMediaQuery('(min-width: 768px)')
type ShellMode = 'wide' | 'compact' | 'drawer'
const mode = computed<ShellMode>(() => {
  if (isWide.value) return 'wide'
  return isTablet.value ? 'compact' : 'drawer'
})

const drawerOpen = ref(false)
watch(currentPath, () => {
  drawerOpen.value = false
})
// Le tiroir n'a pas de sens une fois la barre latérale affichée.
watch(mode, (current) => {
  if (current !== 'drawer') drawerOpen.value = false
})

const mainContent = useTemplateRef<HTMLElement>('mainContent')
// Un href « #main-content » changerait le hash du routeur : on déplace seulement le focus.
function skipToContent(): void {
  mainContent.value?.focus()
}

const reducedMotion = 'motion-reduce:animate-none motion-reduce:transition-none'
</script>

<template>
  <div class="min-h-screen bg-background text-foreground">
    <a
      href="#main-content"
      data-testid="skip-link"
      class="sr-only z-[60] rounded-md bg-primary px-3 py-2 text-sm font-medium text-primary-foreground focus:not-sr-only focus:fixed focus:top-2 focus:left-2 focus-visible:ring-2 focus-visible:ring-ring"
      @click.prevent="skipToContent"
      >{{ t('nav.skip') }}</a
    >
    <div class="flex min-h-screen">
      <aside
        v-if="mode !== 'drawer'"
        :data-mode="mode"
        class="sticky top-0 flex h-screen shrink-0 flex-col gap-4 border-r border-sidebar-border bg-sidebar p-2 text-sidebar-foreground"
        :class="mode === 'wide' ? 'w-60' : 'w-16'"
      >
        <div class="px-2 pt-2" :class="mode === 'compact' ? 'flex justify-center px-0' : ''">
          <AppLogo
            :variant="mode === 'compact' ? 'emblem' : 'full'"
            :icon-only="mode === 'compact'"
          />
        </div>
        <SidebarNav :items="navItems" :compact="mode === 'compact'" />
      </aside>
      <div class="flex min-w-0 flex-1 flex-col">
        <DialogRoot v-if="mode === 'drawer'" v-model:open="drawerOpen">
          <header
            class="sticky top-0 z-40 flex items-center gap-3 border-b border-sidebar-border bg-sidebar px-3 py-2"
          >
            <DialogTrigger
              data-testid="drawer-open"
              class="rounded-md p-2 text-foreground outline-none hover:bg-sidebar-accent focus-visible:ring-2 focus-visible:ring-ring"
            >
              <Menu class="size-5" aria-hidden="true" />
              <span class="sr-only">{{ t('nav.openMenu') }}</span>
            </DialogTrigger>
            <AppLogo variant="emblem" />
          </header>
          <DialogPortal>
            <DialogOverlay
              class="fixed inset-0 z-50 bg-black/60 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:animate-in data-[state=open]:fade-in-0"
              :class="reducedMotion"
            />
            <DialogContent
              class="fixed inset-y-0 left-0 z-50 flex w-72 max-w-[85vw] flex-col gap-4 border-r border-sidebar-border bg-sidebar p-3 text-sidebar-foreground shadow-lg duration-200 data-[state=closed]:animate-out data-[state=closed]:slide-out-to-left data-[state=open]:animate-in data-[state=open]:slide-in-from-left"
              :class="reducedMotion"
            >
              <div class="flex items-center justify-between">
                <AppLogo variant="emblem" />
                <DialogClose
                  data-testid="drawer-close"
                  class="rounded-md p-2 outline-none hover:bg-sidebar-accent focus-visible:ring-2 focus-visible:ring-ring"
                >
                  <X class="size-5" aria-hidden="true" />
                  <span class="sr-only">{{ t('nav.closeMenu') }}</span>
                </DialogClose>
              </div>
              <DialogTitle class="sr-only">{{ t('nav.label') }}</DialogTitle>
              <DialogDescription class="sr-only">{{ t('nav.drawerHint') }}</DialogDescription>
              <SidebarNav :items="navItems" />
            </DialogContent>
          </DialogPortal>
        </DialogRoot>
        <main
          id="main-content"
          ref="mainContent"
          tabindex="-1"
          class="mx-auto grid w-full max-w-6xl flex-1 content-start gap-4 p-4 outline-none sm:p-6"
        >
          <slot />
        </main>
      </div>
    </div>
  </div>
</template>
