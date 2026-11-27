<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import FormAlert from '@/components/FormAlert.vue'
import { Button } from '@/components/ui/button'
import type { CredentialItem, CredentialKind } from '@/api/types'
import { useAuth } from '@/composables/useAuth'
import { useFormRun } from '@/composables/useFormRun'
import { t } from '@/i18n'
import { formatDateTime } from '@/lib/format'
import { FIELD_CLASS } from '@/lib/styles'

const KINDS: readonly CredentialKind[] = ['second_factor', 'passkey']

const auth = useAuth()
const form = useFormRun()
const name = ref('')
const kind = ref<CredentialKind>('second_factor')
const password = ref('')
const renaming = ref<CredentialItem>()
const newName = ref('')
const removing = ref<CredentialItem>()
const removalOpen = ref(false)
const removalPassword = ref('')

function lastUsedText(key: CredentialItem): string {
  if (!key.lastUsedAt) return t('auth.account.keys.neverUsed')
  return t('auth.account.keys.lastUsed', { date: formatDateTime(key.lastUsedAt) })
}

async function add(): Promise<void> {
  const typed = password.value
  password.value = ''
  const added = await form.run(
    () => auth.addKey(name.value, kind.value, typed),
    'auth.account.keys.added',
  )
  if (added) name.value = ''
}

function startRename(key: CredentialItem): void {
  renaming.value = key
  newName.value = key.name
}

async function saveName(): Promise<void> {
  const key = renaming.value
  if (!key) return
  if (await form.run(() => auth.renameKey(key.id, newName.value))) renaming.value = undefined
}

function askRemoval(key: CredentialItem): void {
  removing.value = key
  removalPassword.value = ''
  removalOpen.value = true
}

async function remove(): Promise<void> {
  const key = removing.value
  const typed = removalPassword.value
  removalPassword.value = ''
  if (key) await form.run(() => auth.removeKey(key.id, typed), 'auth.account.keys.removed')
}

watch(removalOpen, (open) => {
  if (!open) removalPassword.value = ''
})

onMounted(() => form.run(auth.loadCredentials))
</script>

<template>
  <section class="grid content-start gap-3 rounded-lg border bg-card p-4">
    <h2 class="font-medium">{{ t('auth.account.keys.title') }}</h2>
    <p v-if="auth.credentials.value.length === 0" class="text-sm text-muted-foreground">
      {{ t('auth.account.keys.empty') }}
    </p>
    <ul class="grid gap-3 [grid-template-columns:repeat(auto-fill,minmax(min(100%,18rem),1fr))]">
      <li
        v-for="key in auth.credentials.value"
        :key="key.id"
        data-testid="key-item"
        class="grid content-start gap-1 rounded-md border p-3 text-sm"
      >
        <form
          v-if="renaming?.id === key.id"
          data-testid="key-rename-form"
          class="grid gap-2"
          @submit.prevent="saveName"
        >
          <label class="grid gap-1">
            <span class="font-medium">{{ t('auth.account.keys.name') }}</span>
            <input
              v-model="newName"
              data-testid="key-rename-input"
              type="text"
              autocomplete="off"
              required
              :class="FIELD_CLASS"
            />
          </label>
          <div class="flex flex-wrap gap-2">
            <Button type="submit" size="sm">{{ t('auth.account.keys.save') }}</Button>
            <Button size="sm" variant="ghost" @click="renaming = undefined">
              {{ t('auth.account.keys.cancel') }}
            </Button>
          </div>
        </form>
        <span v-else class="font-medium [overflow-wrap:anywhere]">{{ key.name }}</span>
        <span>{{ t(`auth.account.keys.kinds.${key.kind}`) }}</span>
        <span class="text-muted-foreground">{{
          t('auth.account.keys.created', { date: formatDateTime(key.createdAt) })
        }}</span>
        <span class="text-muted-foreground">{{ lastUsedText(key) }}</span>
        <div class="mt-1 flex flex-wrap gap-2">
          <Button
            size="sm"
            variant="outline"
            :data-testid="`key-rename-${key.id}`"
            :aria-label="t('auth.account.keys.renameOf', { name: key.name })"
            @click="startRename(key)"
          >
            {{ t('auth.account.keys.rename') }}
          </Button>
          <Button
            size="sm"
            variant="destructive"
            :data-testid="`key-remove-${key.id}`"
            :aria-label="t('auth.account.keys.removeOf', { name: key.name })"
            @click="askRemoval(key)"
          >
            {{ t('auth.account.keys.remove') }}
          </Button>
        </div>
      </li>
    </ul>

    <form
      data-testid="key-add-form"
      class="grid gap-3 border-t pt-3 md:grid-cols-[repeat(3,minmax(0,1fr))]"
      novalidate
      @submit.prevent="add"
    >
      <h3 class="col-span-full text-sm font-medium">{{ t('auth.account.keys.addTitle') }}</h3>
      <label class="grid content-start gap-1 text-sm">
        <span class="font-medium">{{ t('auth.account.keys.name') }}</span>
        <input
          v-model="name"
          data-testid="key-name"
          type="text"
          autocomplete="off"
          required
          :class="FIELD_CLASS"
        />
      </label>
      <fieldset class="grid content-start gap-2 text-sm">
        <legend class="mb-1 font-medium">{{ t('auth.account.keys.kind') }}</legend>
        <label v-for="option in KINDS" :key="option" class="flex items-start gap-2">
          <input
            v-model="kind"
            :data-testid="`key-kind-${option}`"
            type="radio"
            name="key-kind"
            :value="option"
            class="mt-1"
          />
          <span class="grid">
            <span>{{ t(`auth.account.keys.kinds.${option}`) }}</span>
            <span class="text-muted-foreground">{{
              t(`auth.account.keys.kindHelp.${option}`)
            }}</span>
          </span>
        </label>
      </fieldset>
      <label class="grid content-start gap-1 text-sm">
        <span class="font-medium">{{ t('auth.account.keys.password') }}</span>
        <input
          v-model="password"
          data-testid="key-password"
          type="password"
          autocomplete="current-password"
          required
          :class="FIELD_CLASS"
        />
      </label>
      <Button
        type="submit"
        data-testid="key-add"
        class="col-span-full justify-self-start"
        :disabled="form.busy.value"
      >
        {{ t('auth.account.keys.add') }}
      </Button>
    </form>
    <FormAlert
      :message="form.message.value"
      :tone="form.succeeded.value ? 'success' : 'error'"
      data-testid="keys-message"
    />

    <ConfirmDialog
      v-model:open="removalOpen"
      :title="t('auth.account.keys.removeTitle')"
      :description="t('auth.account.keys.removeDescription')"
      :confirm-label="t('auth.account.keys.remove')"
      summary-key="auth.account.keys.removeSummary"
      :items="removing ? [removing.name] : []"
      :confirm-disabled="removalPassword === ''"
      @confirm="remove"
    >
      <label class="grid gap-1 text-sm">
        <span class="font-medium">{{ t('auth.account.keys.password') }}</span>
        <input
          v-model="removalPassword"
          data-testid="key-remove-password"
          type="password"
          autocomplete="current-password"
          required
          :class="FIELD_CLASS"
        />
      </label>
    </ConfirmDialog>
  </section>
</template>
