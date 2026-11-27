<script setup lang="ts">
import {
  Copy,
  Database,
  HardDrive,
  Inbox,
  Link,
  ListChecks,
  Rocket,
  SlidersHorizontal,
  UserRound,
  WandSparkles,
} from '@lucide/vue'
import { computed, onMounted, ref, watch, type Component } from 'vue'
import { authToken } from '@/api/client'
import AppShell from '@/components/AppShell.vue'
import BootstrapTokenDialog from '@/components/BootstrapTokenDialog.vue'
import PageHeader from '@/components/PageHeader.vue'
import ProtectAccountPanel from '@/components/ProtectAccountPanel.vue'
import { Toaster } from '@/components/ui/sonner'
import type { NavItem } from '@/lib/nav'
import { useAuth } from '@/composables/useAuth'
import { useOnboarding } from '@/composables/useOnboarding'
import { useSetup } from '@/composables/useSetup'
import { t } from '@/i18n'
import { ROUTES, currentPath, navigate, useHashRouter } from '@/router'
import AccountSetupView from '@/views/AccountSetupView.vue'
import AccountView from '@/views/AccountView.vue'
import ConfigView from '@/views/ConfigView.vue'
import DedupeView from '@/views/DedupeView.vue'
import LinksView from '@/views/LinksView.vue'
import LoginView from '@/views/LoginView.vue'
import OnboardingView from '@/views/OnboardingView.vue'
import ProposalsView from '@/views/ProposalsView.vue'
import SourcesView from '@/views/SourcesView.vue'
import SetupView from '@/views/SetupView.vue'
import StagesView from '@/views/StagesView.vue'
import VolumesView from '@/views/VolumesView.vue'

const VIEWS: Record<string, Component> = {
  '/assistant': SetupView,
  '/demarrage': OnboardingView,
  '/propositions': ProposalsView,
  '/deduplication': DedupeView,
  '/liens': LinksView,
  '/etapes': StagesView,
  '/sources': SourcesView,
  '/volumes': VolumesView,
  '/configuration': ConfigView,
  '/compte': AccountView,
}

const NAV_ICONS: Record<string, Component> = {
  '/assistant': WandSparkles,
  '/demarrage': Rocket,
  '/propositions': Inbox,
  '/deduplication': Copy,
  '/liens': Link,
  '/etapes': ListChecks,
  '/sources': Database,
  '/volumes': HardDrive,
  '/configuration': SlidersHorizontal,
  '/compte': UserRound,
}

useHashRouter()
const auth = useAuth()
const authState = computed(() => auth.status.value?.state)
const { needsOnboarding, loadAll, checks, status } = useOnboarding()
const setup = useSetup()
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
// Changer la clé remonte la vue courante : après une connexion elle recharge ses propres données.
const viewKey = ref(0)
const sessionUnreadable = ref(false)
// « Étapes » reste accessible pendant la redirection : c'est là qu'on lance ce qui corrige un premier lancement.
const REDIRECTED_PATHS = ['/propositions', '/deduplication', '/liens']

// Tant qu'un contrôle échoue (ou au premier lancement), toute autre vue ramène à « Démarrage » sauf choix « passer ».
// Configuration vide : l'assistant passe avant « Démarrage ».
watch(
  [currentPath, needsOnboarding, setup.needsSetup],
  () => {
    const isRedirectable = REDIRECTED_PATHS.includes(currentPath.value)
    if (setup.needsSetup.value && (isRedirectable || currentPath.value === '/demarrage'))
      navigate('/assistant')
    else if (needsOnboarding.value && isRedirectable) navigate('/demarrage')
  },
  { immediate: true },
)

async function readSession(): Promise<void> {
  sessionUnreadable.value = !(await auth.load()).ok && auth.status.value === undefined
}

// Rien d'autre que l'état de la session n'est demandé avant une connexion ou la création du compte.
watch(authState, (state) => {
  if (state !== 'authenticated') return
  viewKey.value += 1
  void loadAll()
  void setup.load()
})
onMounted(readSession)
</script>

<template>
  <AppShell v-if="authState === 'authenticated'" :nav-items="navItems">
    <ProtectAccountPanel v-if="auth.accountJustCreated.value" />
    <PageHeader v-if="currentRoute" :title-key="`nav.${currentRoute.navKey}`" />
    <component :is="currentView" :key="viewKey" />
  </AppShell>
  <LoginView v-else-if="authState === 'logged_out'" />
  <template v-else-if="authState === 'no_account'">
    <AccountSetupView />
    <BootstrapTokenDialog :open="authToken === ''" />
  </template>
  <p v-else-if="sessionUnreadable" role="alert" class="p-4 text-destructive">
    {{ t('auth.errors.network') }}
  </p>
  <Toaster position="bottom-right" theme="dark" />
</template>
