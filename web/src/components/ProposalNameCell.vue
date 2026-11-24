<script setup lang="ts">
import { ChevronDown, ChevronRight } from '@lucide/vue'
import type { Proposal } from '@/api/types'
import ProposalDetail from '@/components/ProposalDetail.vue'
import { t } from '@/i18n'

defineProps<{ proposal: Proposal; expanded: boolean }>()
const emit = defineEmits<{ toggle: [] }>()
</script>

<template>
  <div class="py-1 whitespace-normal">
    <div class="flex items-start gap-1">
      <button
        type="button"
        class="mt-0.5 shrink-0 rounded-sm focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
        :aria-expanded="expanded"
        :aria-controls="`proposal-detail-${proposal.id}`"
        :aria-label="
          t(expanded ? 'proposals.details.hide' : 'proposals.details.show', {
            name: proposal.candidate_name,
          })
        "
        @click="emit('toggle')"
      >
        <ChevronDown v-if="expanded" class="size-4" aria-hidden="true" />
        <ChevronRight v-else class="size-4" aria-hidden="true" />
      </button>
      <span
        class="font-medium [overflow-wrap:anywhere]"
        :class="proposal.current ? '' : 'text-muted-foreground'"
        >{{ proposal.candidate_name }}</span
      >
    </div>
    <div v-if="expanded" :id="`proposal-detail-${proposal.id}`" class="pl-5">
      <ProposalDetail :proposal="proposal" />
    </div>
  </div>
</template>
