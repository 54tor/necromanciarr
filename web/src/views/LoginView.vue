<script setup lang="ts">
import { computed, ref } from 'vue'
import AuthPage from '@/components/AuthPage.vue'
import FormAlert from '@/components/FormAlert.vue'
import { Button } from '@/components/ui/button'
import { useAuth } from '@/composables/useAuth'
import { useFormRun } from '@/composables/useFormRun'
import { t } from '@/i18n'
import { FIELD_CLASS } from '@/lib/styles'

const TOTP_FACTOR = 'totp'

const auth = useAuth()
const username = ref('')
const password = ref('')
const code = ref('')
const { message, busy, run, clear } = useFormRun()

const pending = auth.pendingLogin
const offersKey = computed(() => pending.value?.keyOptions !== undefined)
const offersTotp = computed(() => pending.value?.factors.includes(TOTP_FACTOR) ?? false)
const isLocked = computed(() => busy.value || auth.retryAfterSeconds.value > 0)

async function submitPassword(): Promise<void> {
  const typedPassword = password.value
  password.value = ''
  await run(() => auth.login(username.value, typedPassword))
}

async function submitCode(): Promise<void> {
  const typedCode = code.value
  code.value = ''
  await run(() => auth.finishWithTotp(typedCode))
}

function cancel(): void {
  auth.cancelLogin()
  clear()
}
</script>

<template>
  <AuthPage title-key="auth.login.title" description-key="auth.login.description">
    <template v-if="pending">
      <h2 class="font-medium">{{ t('auth.login.factorTitle') }}</h2>
      <p class="text-sm text-muted-foreground">{{ t('auth.login.factorHelp') }}</p>
      <Button
        v-if="offersKey"
        data-testid="factor-key"
        :disabled="isLocked"
        @click="run(auth.finishWithKey)"
      >
        {{ t('auth.login.useKey') }}
      </Button>
      <form
        v-if="offersTotp"
        data-testid="totp-step"
        class="grid gap-3"
        @submit.prevent="submitCode"
      >
        <label class="grid gap-1 text-sm">
          <span class="font-medium">{{ t('auth.login.totpCode') }}</span>
          <input
            v-model="code"
            data-testid="factor-totp-code"
            type="text"
            inputmode="numeric"
            autocomplete="one-time-code"
            maxlength="6"
            required
            :class="FIELD_CLASS"
          />
        </label>
        <Button type="submit" variant="secondary" :disabled="isLocked">
          {{ t('auth.login.totpSubmit') }}
        </Button>
      </form>
      <FormAlert :message="message" data-testid="login-error" />
      <Button variant="ghost" data-testid="factor-cancel" @click="cancel">
        {{ t('auth.login.cancel') }}
      </Button>
    </template>
    <template v-else>
      <form data-testid="password-step" class="grid gap-3" @submit.prevent="submitPassword">
        <label class="grid gap-1 text-sm">
          <span class="font-medium">{{ t('auth.login.username') }}</span>
          <input
            v-model="username"
            data-testid="login-username"
            type="text"
            autocomplete="username"
            required
            :class="FIELD_CLASS"
          />
        </label>
        <label class="grid gap-1 text-sm">
          <span class="font-medium">{{ t('auth.login.password') }}</span>
          <input
            v-model="password"
            data-testid="login-password"
            type="password"
            autocomplete="current-password"
            required
            :class="FIELD_CLASS"
          />
        </label>
        <Button type="submit" data-testid="login-submit" :disabled="isLocked">
          {{ t('auth.login.submit') }}
        </Button>
      </form>
      <FormAlert :message="message" data-testid="login-error" />
      <Button
        v-if="auth.status.value?.webauthnAvailable"
        variant="outline"
        data-testid="login-passkey"
        :disabled="isLocked"
        @click="run(auth.loginWithPasskey)"
      >
        {{ t('auth.login.passkey') }}
      </Button>
    </template>
  </AuthPage>
</template>
