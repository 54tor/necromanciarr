<script setup lang="ts">
import { RefreshCw } from '@lucide/vue'
import { onMounted } from 'vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { Button } from '@/components/ui/button'
import { useOnboarding } from '@/composables/useOnboarding'
import { navigate } from '@/router'

const { checks, status, worst, firstLaunch, pending, loadAll, rerunCheck, refreshStatus, skip } =
  useOnboarding()

onMounted(() => {
  if (checks.value.length === 0) void loadAll()
})

function skipOnboarding(): void {
  skip()
  navigate('/propositions')
}

const STATE_GROUPS = [
  { key: 'candidates', label: 'Candidats' },
  { key: 'proposals', label: 'Propositions' },
] as const
</script>

<template>
  <section aria-labelledby="onboarding-title" class="grid gap-6">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <h2 id="onboarding-title" class="text-xl font-semibold">Démarrage</h2>
      <div class="flex flex-wrap gap-2">
        <Button data-action="doctor" :disabled="pending" @click="loadAll">
          <RefreshCw aria-hidden="true" /> Tout relancer
        </Button>
        <Button
          v-if="worst === 'fail' || firstLaunch"
          variant="link"
          data-testid="skip"
          @click="skipOnboarding"
        >
          Passer
        </Button>
      </div>
    </div>

    <p v-if="firstLaunch && worst !== 'fail'" class="rounded-lg border bg-card p-4">
      Première utilisation : aucun candidat ni proposition. Lancez les étapes dans l’onglet « Étapes
      ».
    </p>
    <p
      v-if="worst === 'fail'"
      class="rounded-lg border border-destructive bg-card p-4"
      role="alert"
    >
      Au moins un contrôle est en échec : corrigez-le avant de lancer les étapes.
    </p>

    <p v-if="checks.length === 0 && !pending" class="text-muted-foreground">
      Aucun contrôle chargé.
    </p>
    <ol class="grid gap-3" aria-label="Contrôles du diagnostic">
      <li
        v-for="check in checks"
        :key="check.id"
        class="grid gap-2 rounded-lg border bg-card p-4"
        :data-check="check.id"
      >
        <div class="flex flex-wrap items-center justify-between gap-2">
          <h3 class="font-medium">{{ check.title }}</h3>
          <div class="flex items-center gap-2">
            <StatusBadge :status="check.status" />
            <Button
              size="sm"
              variant="outline"
              data-action="doctor"
              :aria-label="`Relancer le contrôle : ${check.title}`"
              :disabled="pending"
              @click="rerunCheck(check.id)"
            >
              Relancer
            </Button>
          </div>
        </div>
        <p class="text-sm">{{ check.detail }}</p>
        <p v-if="check.hint" class="text-sm text-muted-foreground">Conseil : {{ check.hint }}</p>
      </li>
    </ol>

    <div class="grid gap-3 rounded-lg border bg-card p-4">
      <div class="flex items-center justify-between gap-2">
        <h3 class="font-medium">État</h3>
        <Button
          size="sm"
          variant="outline"
          data-action="status"
          :disabled="pending"
          @click="refreshStatus"
        >
          Actualiser
        </Button>
      </div>
      <p v-if="!status" class="text-sm text-muted-foreground">État indisponible.</p>
      <dl v-else class="grid gap-2 text-sm sm:grid-cols-2">
        <div v-for="group in STATE_GROUPS" :key="group.key">
          <dt class="font-medium">{{ group.label }}</dt>
          <dd class="text-muted-foreground">
            <template v-if="Object.keys(status[group.key]).length === 0">Aucun</template>
            <span
              v-for="(count, state) in status[group.key]"
              :key="state"
              class="mr-3 whitespace-nowrap"
            >
              {{ state }} : {{ count }}
            </span>
          </dd>
        </div>
        <div>
          <dt class="font-medium">Mode cross-seed</dt>
          <dd class="text-muted-foreground">{{ status.cross_seed_mode }}</dd>
        </div>
        <div
          v-for="warning in status.warnings ?? []"
          :key="warning"
          class="sm:col-span-2"
          role="status"
        >
          Avertissement : {{ warning }}
        </div>
      </dl>
    </div>
  </section>
</template>
