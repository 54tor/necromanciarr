<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { Proposal } from '@/api/types'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { t } from '@/i18n'
import { formatPercent } from '@/lib/format'

const props = defineProps<{ proposal: Proposal | undefined }>()
const emit = defineEmits<{ choose: [proposal: Proposal, candidateId: number]; close: [] }>()

// Seuls les candidats de la proposition sont proposés : ceux de son litige, sinon son propre candidat.
const candidates = computed(() => {
  const proposal = props.proposal
  if (!proposal) return []
  const members = proposal.details.candidates ?? []
  if (members.length > 0) return members
  return [
    {
      candidate_id: proposal.candidate_id,
      name: proposal.candidate_name,
      tracker: proposal.tracker,
      percent: proposal.details.percent,
    },
  ]
})
const selectedId = ref<number | undefined>(undefined)
watch(
  () => props.proposal,
  () => (selectedId.value = undefined),
)

function confirm(): void {
  if (props.proposal && selectedId.value !== undefined)
    emit('choose', props.proposal, selectedId.value)
}
</script>

<template>
  <Dialog :open="proposal !== undefined" @update:open="(isOpen) => !isOpen && emit('close')">
    <DialogContent>
      <DialogHeader>
        <DialogTitle>{{ t('proposals.choose.title') }}</DialogTitle>
        <DialogDescription>{{ t('proposals.choose.description') }}</DialogDescription>
      </DialogHeader>
      <fieldset class="grid gap-2">
        <legend class="sr-only">{{ t('proposals.choose.legend') }}</legend>
        <label
          v-for="candidate in candidates"
          :key="candidate.candidate_id"
          class="flex cursor-pointer items-start gap-2 rounded-md border p-3 has-[:focus-visible]:ring-3 has-[:focus-visible]:ring-ring/50"
        >
          <input
            v-model="selectedId"
            type="radio"
            name="candidate"
            :value="candidate.candidate_id"
            class="mt-1 accent-primary"
          />
          <span class="min-w-0 break-words text-sm">
            {{
              t('proposals.choose.candidate', {
                name: candidate.name,
                tracker: candidate.tracker,
                percent: formatPercent(candidate.percent),
              })
            }}
          </span>
        </label>
      </fieldset>
      <DialogFooter>
        <Button variant="outline" @click="emit('close')">{{ t('dialog.cancel') }}</Button>
        <Button data-testid="choose-confirm" :disabled="selectedId === undefined" @click="confirm">
          {{ t('proposals.choose.confirm') }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
