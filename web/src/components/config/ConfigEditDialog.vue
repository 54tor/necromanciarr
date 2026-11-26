<script setup lang="ts">
import { ref, watch } from 'vue'
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
import { WHOLE_ELEMENT_FIELD, formValues, type FormField } from '@/lib/configFields'
import { FIELD_CLASS } from '@/lib/styles'
import { cn } from '@/lib/utils'

const props = defineProps<{
  title: string
  fields: readonly FormField[]
  /** Clé de l'aide sur l'identifiant : présente pour un ajout, qui demande l'identifiant. */
  itemIdHintKey?: string
  locked?: boolean
}>()
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ submit: [values: Record<string, unknown>, itemId: string] }>()

const TEXTAREA_CLASS = cn(FIELD_CLASS, 'h-auto min-h-24 py-2 font-mono')
const MULTILINE_KINDS = ['lines', 'json']

const editedFields = ref<FormField[]>([])
const itemId = ref('')
const invalidField = ref('')

watch(
  open,
  (isOpen) => {
    if (!isOpen) return
    editedFields.value = props.fields.map((field) => ({ ...field }))
    itemId.value = ''
    invalidField.value = ''
  },
  { immediate: true },
)

const labelOf = (name: string) =>
  name === WHOLE_ELEMENT_FIELD ? t('config.form.wholeElement') : name
const fieldLabel = (field: FormField) => labelOf(field.name)
const isMultiline = (field: FormField) => !field.secret && MULTILINE_KINDS.includes(field.kind)
const secretStateKey = (field: FormField) =>
  field.isSet ? 'config.secret.set' : 'config.secret.unset'

function submit(): void {
  const isAdding = props.itemIdHintKey !== undefined
  if (isAdding && itemId.value.trim() === '') {
    invalidField.value = t('config.form.itemId')
    return
  }
  const read = formValues(editedFields.value)
  if (!read.valid) {
    invalidField.value = labelOf(read.invalidField)
    return
  }
  open.value = false
  emit('submit', read.values, itemId.value.trim())
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-h-[90vh] overflow-y-auto">
      <DialogHeader>
        <DialogTitle>{{ title }}</DialogTitle>
        <DialogDescription>{{ t('config.form.description') }}</DialogDescription>
      </DialogHeader>
      <p
        v-if="locked"
        data-testid="config-locked-warning"
        class="rounded-md bg-state-warn-surface p-2 text-sm text-state-warn"
      >
        {{ t('config.lockedWarning') }}
      </p>
      <form class="grid gap-3" @submit.prevent="submit">
        <label v-if="itemIdHintKey" class="grid gap-1 text-sm">
          {{ t('config.form.itemId') }}
          <input
            v-model="itemId"
            data-testid="config-item-id"
            type="text"
            autocomplete="off"
            :class="FIELD_CLASS"
          />
          <span class="text-muted-foreground">{{ t(itemIdHintKey) }}</span>
        </label>
        <template v-for="field in editedFields" :key="field.name">
          <label v-if="field.kind === 'boolean'" class="flex items-center gap-2 text-sm">
            <input
              v-model="field.checked"
              :data-testid="`config-field-${field.name}`"
              type="checkbox"
              class="accent-primary"
            />
            {{ fieldLabel(field) }}
          </label>
          <label v-else class="grid gap-1 text-sm">
            <span class="font-mono [overflow-wrap:anywhere]">{{ fieldLabel(field) }}</span>
            <textarea
              v-if="isMultiline(field)"
              v-model="field.text"
              :data-testid="`config-field-${field.name}`"
              :class="TEXTAREA_CLASS"
              spellcheck="false"
            />
            <input
              v-else
              v-model="field.text"
              :data-testid="`config-field-${field.name}`"
              :type="field.secret ? 'password' : 'text'"
              :inputmode="field.kind === 'number' ? 'decimal' : undefined"
              autocomplete="off"
              :class="FIELD_CLASS"
            />
            <span v-if="field.kind === 'lines'" class="text-muted-foreground">
              {{ t('config.form.linesHint') }}
            </span>
            <span
              v-if="field.secret"
              :data-testid="`config-field-state-${field.name}`"
              class="text-muted-foreground"
            >
              {{ t('config.secret.state', { state: t(secretStateKey(field)) }) }}
            </span>
          </label>
        </template>
        <p v-if="invalidField" role="alert" class="text-sm text-state-fail">
          {{ t('config.form.invalid', { field: invalidField }) }}
        </p>
        <DialogFooter>
          <Button type="button" variant="outline" @click="open = false">
            {{ t('dialog.cancel') }}
          </Button>
          <Button type="submit" data-testid="config-form-submit">
            {{ t('config.form.save') }}
          </Button>
        </DialogFooter>
      </form>
    </DialogContent>
  </Dialog>
</template>
