<script setup lang="ts">
import { ref } from 'vue'
import { Button } from '@/components/ui/button'
import { t, te } from '@/i18n'
import { isVisible, newRow, type FormField } from '@/lib/configFields'
import { FIELD_CLASS } from '@/lib/styles'
import { cn } from '@/lib/utils'

const props = defineProps<{ siblings: readonly FormField[]; path: string }>()
const field = defineModel<FormField>('field', { required: true })

const TEXTAREA_CLASS = cn(FIELD_CLASS, 'h-auto min-h-24 py-2 font-mono')
// Un textarea n'a pas de type password : le masquage passe par text-security.
const MASKED_TEXTAREA_CLASS = cn(TEXTAREA_CLASS, '[-webkit-text-security:disc]')
const NUMERIC_KINDS = ['number', 'numbers']

const isRevealed = ref(false)

const label = (name: string) => t(`config.field.${name}`)
const helpOf = () => {
  const key = `config.help.${field.value.spec.helpKey ?? field.value.name}`
  return te(key) ? t(key) : ''
}
const testId = (suffix = '') => `config-field-${props.path}${suffix}`
const isMasked = () => field.value.secret && !isRevealed.value
const textareaClass = () => (isMasked() ? MASKED_TEXTAREA_CLASS : TEXTAREA_CLASS)
const secretState = () => (field.value.isSet ? t('config.secret.set') : t('config.secret.unset'))
const hasChildren = () => field.value.kind === 'group'

function addRow(): void {
  field.value.rows.push(newRow(field.value))
}

function removeRow(rowIndex: number): void {
  field.value.rows.splice(rowIndex, 1)
}
</script>

<template>
  <div v-if="isVisible(field, siblings)" class="grid gap-1 text-sm">
    <label v-if="field.kind === 'boolean'" class="flex items-center gap-2">
      <input
        v-model="field.checked"
        :data-testid="testId()"
        type="checkbox"
        class="accent-primary"
      />
      {{ label(field.name) }}
    </label>

    <fieldset
      v-else-if="field.kind === 'checks'"
      class="flex flex-wrap items-center gap-x-4 gap-y-1"
    >
      <legend class="mb-1 font-medium">{{ label(field.name) }}</legend>
      <label v-for="option in field.options" :key="option" class="flex items-center gap-2">
        <input
          v-model="field.selection"
          :data-testid="testId(`.${option}`)"
          :value="option"
          type="checkbox"
          class="accent-primary"
        />
        <span class="font-mono">{{ option }}</span>
      </label>
    </fieldset>

    <fieldset v-else-if="hasChildren()" class="grid gap-3 rounded-md border border-border p-3">
      <legend class="px-1 font-medium">{{ label(field.name) }}</legend>
      <div class="grid gap-3 [grid-template-columns:repeat(auto-fit,minmax(min(100%,16rem),1fr))]">
        <ConfigFieldInput
          v-for="(child, childIndex) in field.children"
          :key="child.name"
          v-model:field="field.children[childIndex]!"
          :siblings="field.children"
          :path="`${path}.${child.name}`"
        />
      </div>
    </fieldset>

    <fieldset
      v-else-if="field.kind === 'table' || field.kind === 'pairs'"
      class="grid gap-2 rounded-md border border-border p-3"
    >
      <legend class="px-1 font-medium">{{ label(field.name) }}</legend>
      <div
        v-for="(row, rowIndex) in field.rows"
        :key="rowIndex"
        class="grid items-end gap-2 [grid-template-columns:repeat(auto-fit,minmax(min(100%,14rem),1fr))_auto]"
      >
        <ConfigFieldInput
          v-for="(column, columnIndex) in row"
          :key="column.name"
          v-model:field="row[columnIndex]!"
          :siblings="row"
          :path="`${path}.${rowIndex}.${column.name}`"
        />
        <Button
          type="button"
          size="sm"
          variant="outline"
          :data-testid="testId(`.${rowIndex}.remove`)"
          :aria-label="t('config.form.removeRow')"
          @click="removeRow(rowIndex)"
        >
          {{ t('config.form.removeRow') }}
        </Button>
      </div>
      <Button
        type="button"
        size="sm"
        variant="outline"
        class="justify-self-start"
        :data-testid="testId('.add')"
        @click="addRow"
      >
        {{ t('config.form.addRow') }}
      </Button>
    </fieldset>

    <label v-else class="grid gap-1">
      <span class="font-medium">{{ label(field.name) }}</span>
      <select
        v-if="field.kind === 'select'"
        v-model="field.text"
        :data-testid="testId()"
        :class="FIELD_CLASS"
      >
        <option value="">{{ t('config.form.unset') }}</option>
        <option v-for="option in field.options" :key="option" :value="option">{{ option }}</option>
      </select>
      <textarea
        v-else-if="field.kind === 'lines'"
        v-model="field.text"
        :data-testid="testId()"
        :data-masked="isMasked() ? 'true' : undefined"
        :class="textareaClass()"
        :autocomplete="field.secret ? 'new-password' : 'off'"
        spellcheck="false"
      />
      <input
        v-else
        v-model="field.text"
        :data-testid="testId()"
        :type="field.secret ? 'password' : 'text'"
        :inputmode="NUMERIC_KINDS.includes(field.kind) ? 'decimal' : undefined"
        :autocomplete="field.secret ? 'new-password' : 'off'"
        :class="FIELD_CLASS"
      />
    </label>

    <Button
      v-if="field.kind === 'lines' && field.secret"
      type="button"
      size="xs"
      variant="outline"
      class="justify-self-start"
      :aria-pressed="!isMasked()"
      :data-testid="testId('-reveal')"
      @click="isRevealed = !isRevealed"
    >
      {{ isMasked() ? t('config.secret.show') : t('config.secret.hide') }}
    </Button>
    <span v-if="field.kind === 'lines'" class="text-muted-foreground">
      {{ t('config.form.linesHint') }}
    </span>
    <span v-if="field.secret" :data-testid="testId('-state')" class="text-muted-foreground">
      {{ t('config.secret.state', { state: secretState() }) }}
    </span>
    <span v-if="helpOf()" class="text-muted-foreground">{{ helpOf() }}</span>
  </div>
</template>
