<script setup lang="ts">
import { computed, ref, watch } from 'vue'
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

const props = defineProps<{
  title: string
  description: string
  confirmLabel: string
  /** Clé de locale du résumé de l'effet, affiché sous la description. */
  summaryKey: string
  /** Éléments concernés par l'action, listés avant la confirmation. */
  items?: string[]
  /** Si fournie, la confirmation reste désactivée tant que cette phrase exacte n'est pas saisie. */
  confirmPhrase?: string
}>()
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ confirm: [] }>()

const typedPhrase = ref('')
const canConfirm = computed(
  () => props.confirmPhrase === undefined || typedPhrase.value === props.confirmPhrase,
)

// Une saisie ne survit pas à la fermeture : la prochaine ouverture repart de zéro.
watch(open, () => {
  typedPhrase.value = ''
})

function confirm(): void {
  if (!canConfirm.value) return
  open.value = false
  emit('confirm')
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent>
      <DialogHeader>
        <DialogTitle>{{ title }}</DialogTitle>
        <DialogDescription>{{ description }}</DialogDescription>
      </DialogHeader>
      <p data-testid="confirm-dialog-summary" class="text-sm">
        {{ t(summaryKey) }}
      </p>
      <div v-if="items?.length">
        <p class="text-sm font-medium">{{ t('dialog.items', { count: items.length }) }}</p>
        <ul
          data-testid="confirm-dialog-items"
          class="mt-1 max-h-40 list-disc overflow-y-auto pl-5 text-sm"
        >
          <li v-for="item in items" :key="item">{{ item }}</li>
        </ul>
      </div>
      <label v-if="confirmPhrase !== undefined" class="flex flex-col gap-1 text-sm">
        {{ t('dialog.confirmPhrase', { phrase: confirmPhrase }) }}
        <input
          v-model="typedPhrase"
          data-testid="confirm-dialog-phrase"
          type="text"
          autocomplete="off"
          class="rounded-md border border-input bg-background px-2 py-1 focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
        />
      </label>
      <slot />
      <DialogFooter>
        <Button variant="outline" data-testid="confirm-dialog-cancel" @click="open = false">
          {{ t('dialog.cancel') }}
        </Button>
        <Button
          variant="destructive"
          data-testid="confirm-dialog-confirm"
          :disabled="!canConfirm"
          @click="confirm"
        >
          {{ confirmLabel }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
