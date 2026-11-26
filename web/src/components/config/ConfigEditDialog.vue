<script setup lang="ts">
import { ref, watch } from 'vue'
import ConfigFieldInput from '@/components/config/ConfigFieldInput.vue'
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
import { formValues, type FormField } from '@/lib/configFields'
import { FIELD_CLASS } from '@/lib/styles'

const props = defineProps<{
  title: string
  fields: readonly FormField[]
  /** Clé de l'aide sur l'identifiant : présente pour un ajout, qui demande l'identifiant. */
  itemIdHintKey?: string
  locked?: boolean
  /** Un réglage envoie aussi ses valeurs vides (texte "", liste []) ; un élément les omet. */
  keepBlank?: boolean
  /** Déduit l'identifiant des champs saisis tant que l'opérateur ne l'a pas modifié. */
  deriveItemId?: (fields: readonly FormField[]) => string
  /** Enregistre ; le dialogue ne se ferme qu'après un succès, la saisie reste sinon. */
  save: (values: Record<string, unknown>, itemId: string) => Promise<boolean>
}>()
const open = defineModel<boolean>('open', { required: true })
const saving = ref(false)

const editedFields = ref<FormField[]>([])
const itemId = ref('')
const isItemIdEdited = ref(false)
const invalidField = ref('')

watch(
  open,
  (isOpen) => {
    if (!isOpen) {
      editedFields.value = []
      itemId.value = ''
      return
    }
    editedFields.value = JSON.parse(JSON.stringify(props.fields)) as FormField[]
    itemId.value = ''
    isItemIdEdited.value = false
    invalidField.value = ''
  },
  { immediate: true },
)

watch(
  editedFields,
  (fields) => {
    if (!props.deriveItemId || isItemIdEdited.value) return
    itemId.value = props.deriveItemId(fields)
  },
  { deep: true },
)

async function submit(): Promise<void> {
  const isAdding = props.itemIdHintKey !== undefined
  if (isAdding && itemId.value.trim() === '') {
    invalidField.value = t('config.form.itemId')
    return
  }
  const read = formValues(editedFields.value, !props.keepBlank)
  if (!read.valid) {
    invalidField.value = t(`config.field.${read.invalidField}`)
    return
  }
  saving.value = true
  const isSaved = await props.save(read.values, itemId.value.trim())
  saving.value = false
  if (!isSaved) return
  editedFields.value = []
  itemId.value = ''
  open.value = false
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-h-[90vh] overflow-y-auto sm:max-w-[min(96vw,64rem)]">
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
            @input="isItemIdEdited = true"
          />
          <span class="text-muted-foreground">{{ t(itemIdHintKey) }}</span>
        </label>
        <ConfigFieldInput
          v-for="(field, fieldIndex) in editedFields"
          :key="field.name"
          v-model:field="editedFields[fieldIndex]!"
          :siblings="editedFields"
          :path="field.name"
        />
        <p v-if="invalidField" role="alert" class="text-sm text-state-fail">
          {{ t('config.form.invalid', { field: invalidField }) }}
        </p>
        <DialogFooter>
          <Button type="button" variant="outline" @click="open = false">
            {{ t('dialog.cancel') }}
          </Button>
          <Button type="submit" data-testid="config-form-submit" :disabled="saving">
            {{ t('config.form.save') }}
          </Button>
        </DialogFooter>
      </form>
    </DialogContent>
  </Dialog>
</template>
