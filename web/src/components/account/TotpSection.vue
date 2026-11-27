<script setup lang="ts">
import { computed, onUnmounted, ref } from 'vue'
import FormAlert from '@/components/FormAlert.vue'
import { Button } from '@/components/ui/button'
import { useAuth } from '@/composables/useAuth'
import { useFormRun } from '@/composables/useFormRun'
import { t } from '@/i18n'
import { FIELD_CLASS } from '@/lib/styles'

const auth = useAuth()
const form = useFormRun()
const password = ref('')
const code = ref('')

const enrolment = auth.totpEnrolment
const isEnabled = computed(() => auth.status.value?.secondFactors?.totp ?? false)
const qrSource = computed(() =>
  enrolment.value?.qrPngBase64 ? `data:image/png;base64,${enrolment.value.qrPngBase64}` : '',
)

function takePassword(): string {
  const typed = password.value
  password.value = ''
  return typed
}

async function begin(): Promise<void> {
  const typed = takePassword()
  await form.run(() => auth.beginTotp(typed))
}

async function confirm(): Promise<void> {
  const typed = code.value
  code.value = ''
  await form.run(() => auth.confirmTotp(typed), 'auth.account.totp.confirmed')
}

async function remove(): Promise<void> {
  const typed = takePassword()
  await form.run(() => auth.removeTotp(typed), 'auth.account.totp.removed')
}

function cancel(): void {
  auth.cancelTotp()
  form.clear()
}

onUnmounted(auth.cancelTotp)
</script>

<template>
  <section class="grid content-start gap-3 rounded-lg border bg-card p-4">
    <h2 class="font-medium">{{ t('auth.account.totp.title') }}</h2>
    <template v-if="enrolment">
      <p class="text-sm">{{ t('auth.account.totp.scan') }}</p>
      <div class="flex flex-wrap items-start gap-4">
        <img
          v-if="qrSource"
          data-testid="totp-qr"
          :src="qrSource"
          :alt="t('auth.account.totp.qrAlt')"
          width="192"
          height="192"
          class="size-48 rounded-md bg-white p-2"
        />
        <div class="grid min-w-0 flex-1 gap-2 text-sm">
          <span class="font-medium">{{ t('auth.account.totp.secret') }}</span>
          <code
            data-testid="totp-secret"
            class="rounded-md bg-muted p-2 font-mono [overflow-wrap:anywhere]"
            >{{ enrolment.secret }}</code
          >
          <a
            data-testid="totp-link"
            :href="enrolment.otpauthUri"
            class="text-primary underline-offset-4 hover:underline"
            >{{ t('auth.account.totp.link') }}</a
          >
        </div>
      </div>
      <p class="text-sm text-muted-foreground">{{ t('auth.account.totp.restartNote') }}</p>
      <form
        data-testid="totp-confirm-form"
        class="flex flex-wrap items-end gap-3"
        novalidate
        @submit.prevent="confirm"
      >
        <label class="grid gap-1 text-sm">
          <span class="font-medium">{{ t('auth.account.totp.code') }}</span>
          <input
            v-model="code"
            data-testid="totp-code"
            type="text"
            inputmode="numeric"
            autocomplete="one-time-code"
            maxlength="6"
            required
            :class="FIELD_CLASS"
          />
        </label>
        <Button type="submit" data-testid="totp-confirm" :disabled="form.busy.value">
          {{ t('auth.account.totp.confirm') }}
        </Button>
        <Button type="button" variant="ghost" data-testid="totp-cancel" @click="cancel">
          {{ t('auth.account.totp.cancel') }}
        </Button>
      </form>
    </template>
    <template v-else>
      <p class="text-sm text-muted-foreground">
        {{ isEnabled ? t('auth.account.totp.enabled') : t('auth.account.totp.disabled') }}
      </p>
      <form
        :data-testid="isEnabled ? 'totp-remove-form' : 'totp-begin-form'"
        class="flex flex-wrap items-end gap-3"
        novalidate
        @submit.prevent="isEnabled ? remove() : begin()"
      >
        <label class="grid gap-1 text-sm">
          <span class="font-medium">{{ t('auth.account.totp.password') }}</span>
          <input
            v-model="password"
            data-testid="totp-password"
            type="password"
            autocomplete="current-password"
            required
            :class="FIELD_CLASS"
          />
        </label>
        <Button
          type="submit"
          :variant="isEnabled ? 'destructive' : 'default'"
          :disabled="form.busy.value"
        >
          {{ isEnabled ? t('auth.account.totp.remove') : t('auth.account.totp.enable') }}
        </Button>
      </form>
    </template>
    <FormAlert
      :message="form.message.value"
      :tone="form.succeeded.value ? 'success' : 'error'"
      data-testid="totp-message"
    />
  </section>
</template>
