<script setup lang="ts">
import { computed } from 'vue'
import type { Proposal } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Progress } from '@/components/ui/progress'
import { formatBytes, formatPercent } from '@/lib/format'
import { warningText } from '@/lib/warnings'
import { DECISION_LABELS, KIND_LABELS, STEP_LABELS } from '@/lib/labels'

const props = defineProps<{ proposal: Proposal; disabled: boolean }>()
const emit = defineEmits<{
  accept: [proposal: Proposal]
  reject: [proposal: Proposal]
  choose: [proposal: Proposal]
  force: [proposal: Proposal]
  acceptGroup: [proposal: Proposal]
}>()

const details = computed(() => props.proposal.details)
const isConflict = computed(() => props.proposal.kind === 'conflict')
const isActionable = computed(() => props.proposal.current && !props.disabled)
const isOpen = computed(() => ['pending', 'failed'].includes(props.proposal.decision))
// Forcer ne vaut que pour un partiel avec annexes manquantes déjà lié et ensemencé en pause.
const canForce = computed(
  () =>
    props.proposal.kind === 'partial_annex' && props.proposal.step === 'seeded_paused_incomplete',
)
const fileSummary = computed(() => {
  const counts: [number, string][] = [
    [details.value.missing_media_files?.length ?? 0, 'média(s) manquant(s)'],
    [details.value.ambiguous_files?.length ?? 0, 'fichier(s) ambigu(s)'],
    [details.value.excluded_files?.length ?? 0, 'fichier(s) écarté(s)'],
  ]
  return counts
    .filter(([count]) => count > 0)
    .map(([count, label]) => `${count} ${label}`)
    .join(' · ')
})
</script>

<template>
  <li class="grid gap-3 rounded-lg border bg-card p-4" :data-testid="`proposal-${proposal.id}`">
    <div class="flex flex-wrap items-start justify-between gap-2">
      <div class="min-w-0">
        <h3 class="break-words font-medium">{{ proposal.candidate_name }}</h3>
        <p class="text-sm text-muted-foreground">
          {{ proposal.tracker }} · {{ formatBytes(proposal.candidate_size) }} · groupe
          {{ proposal.group_id }}
          <template v-if="proposal.primary"> · principale</template>
        </p>
      </div>
      <div class="flex flex-wrap gap-1">
        <Badge variant="outline" data-testid="kind">{{ KIND_LABELS[proposal.kind] }}</Badge>
        <Badge variant="secondary">{{ DECISION_LABELS[proposal.decision] }}</Badge>
        <Badge v-if="proposal.step" variant="secondary">{{ STEP_LABELS[proposal.step] }}</Badge>
        <Badge v-if="!proposal.current" variant="destructive" data-testid="stale">Obsolète</Badge>
      </div>
    </div>

    <div class="grid gap-1">
      <Progress
        :model-value="details.percent"
        :aria-label="`Pourcentage vérifié : ${formatPercent(details.percent)}`"
      />
      <p class="text-sm" data-testid="verified">
        {{ formatPercent(details.percent) }} vérifié ·
        <span data-testid="missing">{{ details.missing_pieces }}</span> pièce(s) manquante(s) sur
        {{ details.total_pieces }}
      </p>
      <p v-if="fileSummary" class="text-sm text-muted-foreground">{{ fileSummary }}</p>
      <ul v-if="isConflict && details.candidates?.length" class="text-sm text-muted-foreground">
        <li v-for="member in details.candidates" :key="member.candidate_id">
          {{ member.name }} ({{ member.tracker }}, {{ formatPercent(member.percent) }})
        </li>
      </ul>
    </div>

    <ul v-if="proposal.warnings.length" class="grid gap-1 text-sm" aria-label="Avertissements">
      <li v-for="(warning, position) in proposal.warnings" :key="position">
        Avertissement : {{ warningText(warning) }}
      </li>
    </ul>

    <div class="flex flex-wrap gap-2">
      <Button
        size="sm"
        data-action="proposals accept"
        :disabled="!isActionable || !isOpen || isConflict"
        @click="emit('accept', proposal)"
      >
        Accepter
      </Button>
      <Button
        size="sm"
        variant="outline"
        data-action="proposals reject"
        :disabled="!isActionable || !isOpen"
        @click="emit('reject', proposal)"
      >
        Rejeter
      </Button>
      <Button
        v-if="isConflict"
        size="sm"
        variant="secondary"
        data-action="proposals choose"
        :disabled="!isActionable || !isOpen"
        @click="emit('choose', proposal)"
      >
        Choisir un candidat
      </Button>
      <Button
        v-if="proposal.primary"
        size="sm"
        variant="secondary"
        data-action="proposals accept-group"
        :disabled="!isActionable || !isOpen"
        @click="emit('acceptGroup', proposal)"
      >
        Accepter le groupe
      </Button>
      <Button
        size="sm"
        variant="destructive"
        data-action="proposals force"
        :disabled="!isActionable || !canForce"
        @click="emit('force', proposal)"
      >
        Forcer l’ajout
      </Button>
    </div>
  </li>
</template>
