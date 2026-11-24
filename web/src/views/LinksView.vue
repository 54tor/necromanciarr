<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api } from '@/api/endpoints'
import type { RepairDetails } from '@/api/types'
import MaintenanceActions from '@/components/MaintenanceActions.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useMaintenance } from '@/composables/useMaintenance'
import { formatBytes } from '@/lib/format'
import { DECISION_LABELS, REPAIR_METHOD_LABELS, REPAIR_STATUS_LABELS, labelOf } from '@/lib/labels'
import { FIELD_CLASS } from '@/lib/styles'

const { items, dryRun, busy, pending, load, canApply, applyOne, start } =
  useMaintenance<RepairDetails>('repair', api.repairApply)
const directory = ref('')

onMounted(load)
</script>

<template>
  <section aria-labelledby="links-title" class="grid gap-4">
    <h2 id="links-title" class="text-xl font-semibold">Liens</h2>
    <p class="text-sm text-muted-foreground">
      Liens cassés ou déplacés détectés, et la réparation proposée pour chacun.
    </p>
    <form
      class="flex flex-wrap items-end gap-3"
      @submit.prevent="start(() => api.repairScan(directory.trim()))"
    >
      <label class="grid min-w-64 flex-1 gap-1 text-sm">
        Dossier à explorer (facultatif)
        <input
          v-model="directory"
          type="text"
          autocomplete="off"
          :class="FIELD_CLASS"
          data-testid="scan-directory"
        />
      </label>
      <Button type="submit" data-action="repair scan" :disabled="busy || pending">
        Rechercher les liens à réparer
      </Button>
      <label class="flex items-center gap-2 pb-2 text-sm">
        <input
          v-model="dryRun"
          type="checkbox"
          class="size-4 accent-primary"
          data-testid="dry-run"
        />
        Simulation (aucun fichier modifié)
      </label>
    </form>

    <p v-if="items.length === 0" class="rounded-lg border bg-card p-6 text-muted-foreground">
      Aucun lien à réparer. Lancez une recherche.
    </p>
    <ul class="grid gap-3" aria-label="Liens à réparer">
      <li
        v-for="item in items"
        :key="item.id"
        class="grid gap-2 rounded-lg border bg-card p-4"
        :data-testid="`repair-${item.id}`"
      >
        <div class="flex flex-wrap items-center gap-2">
          <Badge
            :variant="item.details.status === 'repairable' ? 'outline' : 'destructive'"
            data-testid="status"
          >
            {{ labelOf(REPAIR_STATUS_LABELS, item.details.status) }}
          </Badge>
          <Badge variant="secondary">{{ DECISION_LABELS[item.decision] }}</Badge>
          <Badge
            v-if="item.details.status === 'ambiguous'"
            variant="outline"
            data-testid="ambiguous"
          >
            Plusieurs candidats : non applicable
          </Badge>
          <Badge v-if="item.details.method" variant="outline" data-testid="method">
            Méthode : {{ labelOf(REPAIR_METHOD_LABELS, item.details.method) }}
          </Badge>
        </div>
        <dl class="grid gap-1 text-sm">
          <div>
            <dt class="mr-1 inline font-medium">Lien :</dt>
            <dd class="inline break-all">{{ item.details.link_path }}</dd>
          </div>
          <div v-if="item.details.old_target">
            <dt class="mr-1 inline font-medium">Ancienne cible :</dt>
            <dd class="inline break-all">{{ item.details.old_target }}</dd>
          </div>
          <div v-if="item.details.new_target">
            <dt class="mr-1 inline font-medium">Nouvelle cible :</dt>
            <dd class="inline break-all">{{ item.details.new_target }}</dd>
            <template v-if="item.details.size"> ({{ formatBytes(item.details.size) }})</template>
          </div>
        </dl>
        <ul
          v-if="item.details.candidates?.length"
          class="break-all text-sm text-muted-foreground"
          aria-label="Cibles possibles"
        >
          <li v-for="path in item.details.candidates" :key="path">{{ path }}</li>
        </ul>
        <p v-for="warning in item.warnings" :key="warning" class="text-sm">
          Avertissement : {{ warning }}
        </p>
        <MaintenanceActions
          action="repair apply"
          :decision="item.decision"
          :applicable="canApply(item)"
          :busy="busy || pending"
          @apply="applyOne(item)"
        />
      </li>
    </ul>
  </section>
</template>
