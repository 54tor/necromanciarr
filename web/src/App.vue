<script setup lang="ts">
import { computed, onMounted, ref, watch, type Component } from 'vue'
import { authToken, loginRequired } from '@/api/client'
import LoginDialog from '@/components/LoginDialog.vue'
import { Toaster } from '@/components/ui/sonner'
import { t } from '@/i18n'
import { useOnboarding } from '@/composables/useOnboarding'
import { ROUTES, currentPath, navigate, useHashRouter } from '@/router'
import DedupeView from '@/views/DedupeView.vue'
import LinksView from '@/views/LinksView.vue'
import OnboardingView from '@/views/OnboardingView.vue'
import ProposalsView from '@/views/ProposalsView.vue'
import StagesView from '@/views/StagesView.vue'

const productName = 'Necromanciarr'
const VIEWS: Record<string, Component> = {
  '/demarrage': OnboardingView,
  '/propositions': ProposalsView,
  '/deduplication': DedupeView,
  '/liens': LinksView,
  '/etapes': StagesView,
}

useHashRouter()
const { needsOnboarding, loadAll } = useOnboarding()
const currentView = computed(() => VIEWS[currentPath.value])
// Changer la clé remonte la vue courante : après la saisie du jeton elle recharge ses propres données.
const viewKey = ref(0)
// « Étapes » reste accessible pendant la redirection : c'est là qu'on lance ce qui corrige un premier lancement.
const REDIRECTED_PATHS = ['/propositions', '/deduplication', '/liens']

// Tant qu'un contrôle échoue (ou au premier lancement), toute autre vue ramène à « Démarrage » sauf choix « passer ».
watch([currentPath, needsOnboarding], () => {
  if (needsOnboarding.value && REDIRECTED_PATHS.includes(currentPath.value)) navigate('/demarrage')
})

onMounted(loadAll)
// Après la saisie du jeton, on recharge le diagnostic qui avait échoué en 401.
watch(loginRequired, (required) => {
  if (required || !authToken.value) return
  viewKey.value += 1
  void loadAll()
})
</script>

<template>
  <div class="mx-auto flex min-h-screen max-w-5xl flex-col gap-6 p-4 sm:p-6">
    <header class="flex flex-wrap items-center justify-between gap-3">
      <div class="flex items-center gap-3">
        <svg
          class="size-9 text-primary"
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
        <h1 class="text-2xl font-semibold tracking-tight">{{ productName }}</h1>
      </div>
      <nav :aria-label="t('nav.label')">
        <ul class="flex flex-wrap gap-1">
          <li v-for="route in ROUTES" :key="route.path">
            <a
              :href="`#${route.path}`"
              :aria-current="currentPath === route.path ? 'page' : undefined"
              class="inline-block rounded-md px-3 py-1.5 text-sm font-medium text-muted-foreground outline-none hover:bg-accent hover:text-accent-foreground focus-visible:ring-3 focus-visible:ring-ring/50 aria-[current=page]:bg-secondary aria-[current=page]:text-foreground"
            >
              {{ t(`nav.${route.navKey}`) }}
            </a>
          </li>
        </ul>
      </nav>
    </header>
    <main class="grid flex-1 content-start gap-4">
      <component :is="currentView" :key="viewKey" />
    </main>
    <LoginDialog />
    <Toaster position="bottom-right" theme="dark" />
  </div>
</template>
