<script setup lang="ts">
import { ref } from 'vue'
import FormAlert from '@/components/FormAlert.vue'
import { Button } from '@/components/ui/button'
import { useAuth } from '@/composables/useAuth'
import { useFormRun } from '@/composables/useFormRun'
import { t } from '@/i18n'
import { FIELD_CLASS } from '@/lib/styles'

const auth = useAuth()
const form = useFormRun()
const current = ref('')
const replacement = ref('')
const confirmation = ref('')

async function submit(): Promise<void> {
  const typed = [current.value, replacement.value, confirmation.value] as const
  current.value = ''
  replacement.value = ''
  confirmation.value = ''
  await form.run(() => auth.changePassword(...typed), 'auth.account.password.done')
}
</script>

<template>
  <section class="grid content-start gap-3 rounded-lg border bg-card p-4">
    <h2 class="font-medium">{{ t('auth.account.password.title') }}</h2>
    <p class="text-sm text-muted-foreground">{{ t('auth.account.password.note') }}</p>
    <form data-testid="password-form" class="grid gap-3" novalidate @submit.prevent="submit">
      <input
        type="text"
        autocomplete="username"
        :value="auth.status.value?.username ?? ''"
        hidden
        readonly
      />
      <label class="grid gap-1 text-sm">
        <span class="font-medium">{{ t('auth.account.password.current') }}</span>
        <input
          v-model="current"
          data-testid="password-current"
          type="password"
          autocomplete="current-password"
          required
          :class="FIELD_CLASS"
        />
      </label>
      <label class="grid gap-1 text-sm">
        <span class="font-medium">{{ t('auth.account.password.new') }}</span>
        <input
          v-model="replacement"
          data-testid="password-new"
          type="password"
          autocomplete="new-password"
          required
          :class="FIELD_CLASS"
        />
      </label>
      <label class="grid gap-1 text-sm">
        <span class="font-medium">{{ t('auth.account.password.confirmation') }}</span>
        <input
          v-model="confirmation"
          data-testid="password-confirmation"
          type="password"
          autocomplete="new-password"
          required
          :class="FIELD_CLASS"
        />
      </label>
      <Button type="submit" class="justify-self-start" :disabled="form.busy.value">
        {{ t('auth.account.password.submit') }}
      </Button>
    </form>
    <FormAlert
      :message="form.message.value"
      :tone="form.succeeded.value ? 'success' : 'error'"
      data-testid="password-message"
    />
  </section>
</template>
