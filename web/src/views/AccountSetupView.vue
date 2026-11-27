<script setup lang="ts">
import { ref } from 'vue'
import AuthPage from '@/components/AuthPage.vue'
import FormAlert from '@/components/FormAlert.vue'
import { Button } from '@/components/ui/button'
import { useAuth } from '@/composables/useAuth'
import { useFormRun } from '@/composables/useFormRun'
import { t } from '@/i18n'
import { FIELD_CLASS } from '@/lib/styles'

const auth = useAuth()
const username = ref('')
const password = ref('')
const confirmation = ref('')
const { message, busy, run } = useFormRun()

async function submit(): Promise<void> {
  const typed = { password: password.value, confirmation: confirmation.value }
  password.value = ''
  confirmation.value = ''
  await run(() => auth.setup(username.value, typed.password, typed.confirmation))
}
</script>

<template>
  <AuthPage title-key="auth.setup.title" description-key="auth.setup.help">
    <form data-testid="setup-form" class="grid gap-3" novalidate @submit.prevent="submit">
      <label class="grid gap-1 text-sm">
        <span class="font-medium">{{ t('auth.setup.username') }}</span>
        <input
          v-model="username"
          data-testid="setup-username"
          type="text"
          autocomplete="username"
          required
          :class="FIELD_CLASS"
        />
      </label>
      <label class="grid gap-1 text-sm">
        <span class="font-medium">{{ t('auth.setup.password') }}</span>
        <input
          v-model="password"
          data-testid="setup-password"
          type="password"
          autocomplete="new-password"
          aria-describedby="setup-password-rule"
          required
          :class="FIELD_CLASS"
        />
        <span id="setup-password-rule" class="text-muted-foreground">{{
          t('auth.setup.passwordRule')
        }}</span>
      </label>
      <label class="grid gap-1 text-sm">
        <span class="font-medium">{{ t('auth.setup.confirmation') }}</span>
        <input
          v-model="confirmation"
          data-testid="setup-confirmation"
          type="password"
          autocomplete="new-password"
          required
          :class="FIELD_CLASS"
        />
      </label>
      <Button type="submit" data-testid="setup-submit" :disabled="busy">
        {{ t('auth.setup.submit') }}
      </Button>
    </form>
    <FormAlert :message="message" data-testid="setup-error" />
  </AuthPage>
</template>
