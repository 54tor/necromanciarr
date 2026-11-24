<script setup lang="ts">
import { Button } from '@/components/ui/button'
import type { Decision } from '@/api/types'

defineProps<{ action: string; decision: Decision; applicable: boolean; busy: boolean }>()
const emit = defineEmits<{ apply: [] }>()
</script>

<template>
  <div class="flex flex-wrap gap-2">
    <Button
      size="sm"
      :data-action="action"
      data-scope="row"
      :disabled="!applicable || busy"
      @click="emit('apply')"
    >
      {{ decision === 'accepted' ? 'Appliquer' : 'Accepter et appliquer' }}
    </Button>
  </div>
</template>
