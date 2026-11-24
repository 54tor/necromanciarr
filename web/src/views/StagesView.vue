<script setup lang="ts">
import { LoaderCircle, Play } from '@lucide/vue'
import { computed, onMounted } from 'vue'
import { api } from '@/api/endpoints'
import type { StageName, StageResult } from '@/api/types'
import { Button } from '@/components/ui/button'
import { useJobs } from '@/composables/useJobs'

const { jobs, runningJob, busy, start, refresh } = useJobs()

const STAGES: readonly {
  name: StageName
  title: string
  summary: string
  counters: readonly (keyof StageResult)[]
}[] = [
  {
    name: 'scan',
    title: 'Scan',
    summary: 'Inventaire des fichiers de la bibliothèque.',
    counters: ['processed', 'skipped', 'failed'],
  },
  {
    name: 'match',
    title: 'Correspondance',
    summary: 'Recherche des torrents candidats.',
    counters: [
      'candidates_created',
      'candidates_updated',
      'candidates_stale',
      'unidentified_files',
    ],
  },
  {
    name: 'verify',
    title: 'Vérification',
    summary: 'Contrôle des pièces contre les fichiers.',
    counters: ['verified', 'partial_annex', 'partial_pack', 'conflicts', 'proposals_created'],
  },
  {
    name: 'link',
    title: 'Liaison',
    summary: 'Création des liens des propositions acceptées.',
    counters: ['linked', 'failed'],
  },
  {
    name: 'seed',
    title: 'Ensemencement',
    summary: 'Ajout des torrents au client.',
    counters: ['seeded', 'seeded_paused_incomplete', 'failed'],
  },
]

const COUNTER_LABELS: Partial<Record<keyof StageResult, string>> = {
  processed: 'traités',
  skipped: 'ignorés',
  failed: 'échecs',
  candidates_created: 'candidats créés',
  candidates_updated: 'mis à jour',
  candidates_stale: 'périmés',
  unidentified_files: 'fichiers non identifiés',
  verified: 'complets',
  partial_annex: 'partiels (annexes)',
  partial_pack: 'packs partiels',
  conflicts: 'conflits',
  proposals_created: 'propositions créées',
  linked: 'liés',
  seeded: 'ensemencés',
  seeded_paused_incomplete: 'en pause (incomplets)',
}

// Dernier résultat connu par étape : le plus récent job « run » terminé qui porte cette étape.
const lastResults = computed(() => {
  const byStage = new Map<StageName, StageResult>()
  for (const job of jobs.value) {
    const result = job.result as StageResult | undefined
    if (job.action === 'run' && job.state === 'done' && result && !byStage.has(result.stage)) {
      byStage.set(result.stage, result)
    }
  }
  return byStage
})

function counterLines(stage: (typeof STAGES)[number]): { label: string; value: number }[] {
  const result = lastResults.value.get(stage.name)
  if (!result) return []
  return stage.counters.map((counter) => ({
    label: COUNTER_LABELS[counter] ?? counter,
    value: Number(result[counter]),
  }))
}

onMounted(refresh)
</script>

<template>
  <section aria-labelledby="stages-title" class="grid gap-4">
    <h2 id="stages-title" class="text-xl font-semibold">Étapes</h2>
    <p class="text-sm text-muted-foreground" aria-live="polite" data-testid="job-state">
      <template v-if="runningJob">
        <LoaderCircle
          class="mr-1 inline size-4 animate-spin motion-reduce:animate-none"
          aria-hidden="true"
        />
        Une étape est en cours ({{ runningJob.action }} n° {{ runningJob.id }}).
      </template>
      <template v-else>Aucune étape en cours.</template>
    </p>
    <ul class="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
      <li
        v-for="stage in STAGES"
        :key="stage.name"
        class="grid content-between gap-3 rounded-lg border bg-card p-4"
      >
        <div class="grid gap-1">
          <h3 class="font-medium">{{ stage.title }}</h3>
          <p class="text-sm text-muted-foreground">{{ stage.summary }}</p>
          <dl v-if="counterLines(stage).length" class="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-sm">
            <div v-for="line in counterLines(stage)" :key="line.label" class="flex gap-1">
              <dt class="text-muted-foreground">{{ line.label }} :</dt>
              <dd class="font-medium">{{ line.value }}</dd>
            </div>
          </dl>
          <p v-else class="text-sm text-muted-foreground">
            Aucun résultat depuis le démarrage du serveur.
          </p>
        </div>
        <Button
          data-action="run"
          :data-stage="stage.name"
          :disabled="busy"
          :aria-label="`Lancer l’étape ${stage.title}`"
          @click="start(() => api.runStage(stage.name))"
        >
          <Play aria-hidden="true" /> Lancer
        </Button>
      </li>
    </ul>
  </section>
</template>
