<script setup lang="ts">
import { RefreshCw } from '@lucide/vue'
import { onMounted, reactive, ref } from 'vue'
import { toast } from 'vue-sonner'
import { api } from '@/api/endpoints'
import type { Decision, Proposal, ProposalKind } from '@/api/types'
import ChooseDialog from '@/components/ChooseDialog.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import ProposalCard from '@/components/ProposalCard.vue'
import { Button } from '@/components/ui/button'
import { useApi } from '@/composables/useApi'
import { DECISION_LABELS, KIND_LABELS, REVIEW_KINDS } from '@/lib/labels'
import { FIELD_CLASS } from '@/lib/styles'

const { call, pending } = useApi()
const proposals = ref<Proposal[]>([])
const filters = reactive({
  kind: '' as ProposalKind | '',
  decision: '' as Decision | '',
  group: '',
  currentOnly: false,
})

const FORCE_WARNING =
  'Le client va réécrire des octets dans des fichiers liés à la bibliothèque. Cette action n’est pas annulable.'
const PARTIAL_PACK_WARNING =
  'Un pack partiel est incomplet : accepter ajoute un torrent dont des pièces manquent. Continuer ?'

const forceTarget = ref<Proposal | undefined>(undefined)
const partialTarget = ref<Proposal | undefined>(undefined)
const chooseTarget = ref<Proposal | undefined>(undefined)
const forceOpen = ref(false)
const partialOpen = ref(false)

async function load(): Promise<void> {
  const groupId = Number(filters.group)
  const list = await call(() =>
    api.listProposals({
      kind: filters.kind || undefined,
      decision: filters.decision || undefined,
      group: groupId > 0 ? groupId : undefined,
      current_only: filters.currentOnly || undefined,
    }),
  )
  // La déduplication et la réparation ont leurs propres vues.
  if (list) proposals.value = list.filter((proposal) => REVIEW_KINDS.includes(proposal.kind))
}

async function act(task: () => Promise<unknown>, success: string): Promise<void> {
  if ((await call(task)) !== undefined) toast.success(success)
  await load()
}

function accept(proposal: Proposal): void {
  if (proposal.kind !== 'partial_pack') {
    void act(() => api.accept(proposal.id), 'Proposition acceptée.')
    return
  }
  partialTarget.value = proposal
  partialOpen.value = true
}

function askForce(proposal: Proposal): void {
  forceTarget.value = proposal
  forceOpen.value = true
}

function choose(proposal: Proposal, candidateId: number): void {
  chooseTarget.value = undefined
  void act(() => api.choose(proposal.id, candidateId), 'Candidat choisi.')
}

onMounted(load)
</script>

<template>
  <section aria-labelledby="proposals-title" class="grid gap-4">
    <h2 id="proposals-title" class="text-xl font-semibold">Propositions</h2>

    <form
      class="grid gap-3 sm:grid-cols-2 lg:grid-cols-5"
      aria-label="Filtres"
      @submit.prevent="load"
    >
      <label class="grid gap-1 text-sm">
        Nature
        <select v-model="filters.kind" :class="FIELD_CLASS" data-testid="filter-kind">
          <option value="">Toutes</option>
          <option v-for="kind in REVIEW_KINDS" :key="kind" :value="kind">
            {{ KIND_LABELS[kind] }}
          </option>
        </select>
      </label>
      <label class="grid gap-1 text-sm">
        Décision
        <select v-model="filters.decision" :class="FIELD_CLASS" data-testid="filter-decision">
          <option value="">Toutes</option>
          <option v-for="(label, decision) in DECISION_LABELS" :key="decision" :value="decision">
            {{ label }}
          </option>
        </select>
      </label>
      <label class="grid gap-1 text-sm">
        Groupe
        <input
          v-model="filters.group"
          type="number"
          min="1"
          inputmode="numeric"
          :class="FIELD_CLASS"
          data-testid="filter-group"
        />
      </label>
      <label class="flex items-end gap-2 pb-2 text-sm">
        <input
          v-model="filters.currentOnly"
          type="checkbox"
          class="size-4 accent-primary"
          data-testid="filter-current"
        />
        Générations courantes seulement
      </label>
      <div class="flex items-end">
        <Button type="submit" data-action="proposals list" :disabled="pending">
          <RefreshCw aria-hidden="true" /> Actualiser
        </Button>
      </div>
    </form>

    <p
      v-if="proposals.length === 0 && !pending"
      class="rounded-lg border bg-card p-6 text-muted-foreground"
    >
      Aucune proposition. Lancez les étapes pour en produire.
    </p>
    <ul class="grid gap-3" aria-label="Propositions">
      <ProposalCard
        v-for="proposal in proposals"
        :key="proposal.id"
        :proposal="proposal"
        :disabled="pending"
        @accept="accept"
        @reject="(target) => act(() => api.reject(target.id), 'Proposition rejetée.')"
        @choose="(target) => (chooseTarget = target)"
        @force="askForce"
        @accept-group="(target) => act(() => api.acceptGroup(target.group_id), 'Groupe accepté.')"
      />
    </ul>

    <ChooseDialog :proposal="chooseTarget" @choose="choose" @close="chooseTarget = undefined" />
    <ConfirmDialog
      v-model:open="forceOpen"
      title="Forcer l’ajout du partiel ?"
      :description="FORCE_WARNING"
      confirm-label="Forcer"
      @confirm="forceTarget && act(() => api.force(forceTarget!.id), 'Ajout forcé.')"
    />
    <ConfirmDialog
      v-model:open="partialOpen"
      title="Accepter un pack partiel ?"
      :description="PARTIAL_PACK_WARNING"
      confirm-label="Accepter quand même"
      @confirm="
        partialTarget && act(() => api.accept(partialTarget!.id, true), 'Pack partiel accepté.')
      "
    />
  </section>
</template>
