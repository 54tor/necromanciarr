<script setup lang="ts">
import { computed } from 'vue'
import KeysSection from '@/components/account/KeysSection.vue'
import PasswordSection from '@/components/account/PasswordSection.vue'
import SessionsSection from '@/components/account/SessionsSection.vue'
import TotpSection from '@/components/account/TotpSection.vue'
import { useAuth } from '@/composables/useAuth'
import { t } from '@/i18n'

const auth = useAuth()
const factors = computed(() => auth.status.value?.secondFactors)
</script>

<template>
  <section class="grid min-w-0 gap-4 xl:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
    <div
      data-testid="account-summary"
      class="col-span-full flex flex-wrap gap-x-6 gap-y-1 rounded-lg border bg-card p-4 text-sm"
    >
      <h2 class="w-full font-medium">{{ t('auth.account.summary.title') }}</h2>
      <span>{{
        t('auth.account.summary.username', { username: auth.status.value?.username ?? '' })
      }}</span>
      <span>{{ t('auth.account.summary.keys', { count: factors?.keys ?? 0 }) }}</span>
      <span>{{ t('auth.account.summary.passkeys', { count: factors?.passkeys ?? 0 }) }}</span>
      <span>{{
        factors?.totp ? t('auth.account.summary.totpOn') : t('auth.account.summary.totpOff')
      }}</span>
    </div>
    <PasswordSection />
    <TotpSection />
    <KeysSection v-if="auth.status.value?.webauthnAvailable" class="col-span-full" />
    <p
      v-else
      data-testid="keys-https"
      class="col-span-full rounded-lg border bg-card p-4 text-sm text-muted-foreground"
    >
      {{ t('auth.account.keys.httpsRequired') }}
    </p>
    <SessionsSection class="col-span-full" />
  </section>
</template>
