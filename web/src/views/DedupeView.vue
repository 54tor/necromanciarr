<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api } from '@/api/endpoints'
import type { DedupeDetails } from '@/api/types'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import MaintenanceActions from '@/components/MaintenanceActions.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useMaintenance } from '@/composables/useMaintenance'
import { formatBytes } from '@/lib/format'
import { uniqueWarnings, warningText } from '@/lib/warnings'
import { DECISION_LABELS, DEDUPE_METHOD_LABELS, LINK_KIND_LABELS, labelOf } from '@/lib/labels'

const {
  items,
  acceptedItems,
  dryRun,
  busy,
  pending,
  load,
  canApply,
  applyOne,
  applyAccepted,
  start,
} = useMaintenance<DedupeDetails>('dedupe', api.dedupeApply)

const confirmBulk = ref(false)

// En simulation rien n'est modifié : la confirmation ne sert que pour une application réelle.
function askBulk(): void {
  if (dryRun.value) void applyAccepted()
  else confirmBulk.value = true
}

onMounted(load)
</script>

<template>
  <section aria-labelledby="dedupe-title" class="grid gap-4">
    <h2 id="dedupe-title" class="text-xl font-semibold">Déduplication</h2>
    <p class="text-sm text-muted-foreground">
      Remplace un fichier de torrent par un lien vers le fichier identique de la bibliothèque.
    </p>
    <div class="flex flex-wrap items-center gap-3">
      <Button
        data-action="dedupe plan"
        :disabled="busy || pending"
        @click="start(() => api.dedupePlan())"
      >
        Calculer le plan
      </Button>
      <Button
        variant="secondary"
        data-action="dedupe apply"
        data-scope="bulk"
        data-testid="apply-all"
        :disabled="busy || pending"
        @click="askBulk"
      >
        Appliquer toutes les acceptées
      </Button>
      <label class="flex items-center gap-2 text-sm">
        <input
          v-model="dryRun"
          type="checkbox"
          class="size-4 accent-primary"
          data-testid="dry-run"
        />
        Simulation (aucun fichier modifié)
      </label>
    </div>

    <p v-if="items.length === 0" class="rounded-lg border bg-card p-6 text-muted-foreground">
      Aucun remplacement proposé. Calculez le plan.
    </p>
    <ul class="grid gap-3" aria-label="Remplacements proposés">
      <li
        v-for="item in items"
        :key="item.id"
        class="grid gap-2 rounded-lg border bg-card p-4"
        :data-testid="`dedupe-${item.id}`"
      >
        <div class="flex flex-wrap items-center gap-2">
          <Badge variant="secondary">{{ DECISION_LABELS[item.decision] }}</Badge>
          <Badge
            v-if="item.details.ambiguous?.length"
            variant="destructive"
            data-testid="ambiguous"
          >
            Ambigu : {{ item.details.ambiguous.length }} fichiers
          </Badge>
          <Badge v-if="!item.details.actionable" variant="outline">Non applicable</Badge>
        </div>
        <dl class="grid gap-1 text-sm">
          <div>
            <dt class="mr-1 inline font-medium">Fichier du torrent :</dt>
            <dd class="inline break-all">{{ item.details.torrent_path }}</dd>
          </div>
          <div v-if="item.details.library_path">
            <dt class="mr-1 inline font-medium">Fichier équivalent de la bibliothèque :</dt>
            <dd class="inline break-all">{{ item.details.library_path }}</dd>
          </div>
          <div>
            <dt class="mr-1 inline font-medium">Espace gagné :</dt>
            <dd class="inline">{{ formatBytes(item.details.saved_bytes) }}</dd>
            ·
            <dt class="mr-1 inline font-medium">Lien :</dt>
            <dd class="inline">
              {{ labelOf(LINK_KIND_LABELS, item.details.kind ?? '') || 'non défini' }}
            </dd>
            ·
            <dt class="mr-1 inline font-medium">Preuve :</dt>
            <dd class="inline">
              {{ labelOf(DEDUPE_METHOD_LABELS, item.details.method) }},
              {{ formatBytes(item.details.bytes_to_read) }} à lire
            </dd>
          </div>
        </dl>
        <ul
          v-if="item.details.ambiguous?.length"
          class="break-all text-sm text-muted-foreground"
          aria-label="Fichiers ambigus"
        >
          <li v-for="path in item.details.ambiguous" :key="path">{{ path }}</li>
        </ul>
        <p v-if="item.details.reason" class="text-sm text-muted-foreground">
          Raison : {{ item.details.reason }}
        </p>
        <p
          v-for="(warning, position) in uniqueWarnings([
            ...item.warnings,
            ...(item.details.warnings ?? []),
          ])"
          :key="position"
          class="text-sm"
        >
          Avertissement : {{ warningText(warning) }}
        </p>
        <MaintenanceActions
          action="dedupe apply"
          :decision="item.decision"
          :applicable="canApply(item)"
          :busy="busy || pending"
          @apply="applyOne(item)"
        />
      </li>
    </ul>
    <ConfirmDialog
      v-model:open="confirmBulk"
      title="Appliquer les remplacements acceptés ?"
      :description="`${acceptedItems.length} remplacement(s) vont modifier des fichiers de la bibliothèque, un par un. Décochez la simulation uniquement si vous êtes sûr.`"
      confirm-label="Appliquer pour de bon"
      @confirm="applyAccepted"
    />
  </section>
</template>
