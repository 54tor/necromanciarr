<script setup lang="ts">
import { Button } from '@/components/ui/button'
import type { Decision } from '@/api/types'
import { t } from '@/i18n'

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
      {{ t(decision === 'accepted' ? 'maintenance.apply' : 'maintenance.acceptAndApply') }}
    </Button>
  </div>
</template>
