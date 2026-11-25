<script setup lang="ts">
import { Copy, Database, HardDrive, Inbox, Link, ListChecks, Rocket } from '@lucide/vue'
import { computed, onMounted, ref, watch, type Component } from 'vue'
import { authToken, loginRequired } from '@/api/client'
import AppShell from '@/components/AppShell.vue'
import LoginDialog from '@/components/LoginDialog.vue'
import PageHeader from '@/components/PageHeader.vue'
import { Toaster } from '@/components/ui/sonner'
import type { NavItem } from '@/lib/nav'
import { useOnboarding } from '@/composables/useOnboarding'
import { ROUTES, currentPath, navigate, useHashRouter } from '@/router'
import DedupeView from '@/views/DedupeView.vue'
import LinksView from '@/views/LinksView.vue'
import OnboardingView from '@/views/OnboardingView.vue'
import ProposalsView from '@/views/ProposalsView.vue'
import SourcesView from '@/views/SourcesView.vue'
import StagesView from '@/views/StagesView.vue'
import VolumesView from '@/views/VolumesView.vue'

const VIEWS: Record<string, Component> = {
  '/demarrage': OnboardingView,
  '/propositions': ProposalsView,
  '/deduplication': DedupeView,
  '/liens': LinksView,
  '/etapes': StagesView,
  '/sources': SourcesView,
  '/volumes': VolumesView,
}

const NAV_ICONS: Record<string, Component> = {
  '/demarrage': Rocket,
  '/propositions': Inbox,
  '/deduplication': Copy,
  '/liens': Link,
  '/etapes': ListChecks,
  '/sources': Database,
  '/volumes': HardDrive,
}

useHashRouter()
const { needsOnboarding, loadAll, checks, status } = useOnboarding()
const currentView = computed(() => VIEWS[currentPath.value])
const currentRoute = computed(() => ROUTES.find((route) => route.path === currentPath.value))

// Compte de la pastille : propositions en attente, contrôles en échec, volumes sftp coupés.
const BADGES = computed<Record<string, number>>(() => ({
  '/propositions': status.value?.proposals.pending ?? 0,
  '/volumes': status.value?.volumes_broken ?? 0,
  '/demarrage': checks.value.filter((check) => check.status === 'fail').length,
}))
const navItems = computed<NavItem[]>(() =>
  ROUTES.map((route) => ({
    fragment: route.path,
    labelKey: `nav.${route.navKey}`,
    icon: NAV_ICONS[route.path] ?? Rocket,
    badge: BADGES.value[route.path],
  })),
)
// Changer la clé remonte la vue courante : après la saisie du jeton elle recharge ses propres données.
const viewKey = ref(0)
// « Étapes » reste accessible pendant la redirection : c'est là qu'on lance ce qui corrige un premier lancement.
const REDIRECTED_PATHS = ['/propositions', '/deduplication', '/liens']

// Tant qu'un contrôle échoue (ou au premier lancement), toute autre vue ramène à « Démarrage » sauf choix « passer ».
watch(
  [currentPath, needsOnboarding],
  () => {
    if (needsOnboarding.value && REDIRECTED_PATHS.includes(currentPath.value))
      navigate('/demarrage')
  },
  { immediate: true },
)

onMounted(loadAll)
// Après la saisie du jeton, on recharge le diagnostic qui avait échoué en 401.
watch(loginRequired, (required) => {
  if (required || !authToken.value) return
  viewKey.value += 1
  void loadAll()
})
</script>

<template>
  <AppShell :nav-items="navItems">
    <PageHeader v-if="currentRoute" :title-key="`nav.${currentRoute.navKey}`" />
    <component :is="currentView" :key="viewKey" />
    <LoginDialog />
    <Toaster position="bottom-right" theme="dark" />
  </AppShell>
</template>
