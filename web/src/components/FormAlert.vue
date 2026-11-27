<script setup lang="ts">
import { useAuth } from '@/composables/useAuth'
import { t } from '@/i18n'

defineOptions({ inheritAttrs: false })
defineProps<{ message: string; tone?: 'error' | 'success' }>()

const { retryAfterSeconds } = useAuth()
</script>

<template>
  <div aria-live="polite" class="grid gap-1 text-sm">
    <p
      v-if="message"
      :role="tone === 'success' ? 'status' : 'alert'"
      :class="tone === 'success' ? 'text-state-ok' : 'text-destructive'"
      v-bind="$attrs"
    >
      {{ message }}
    </p>
    <p v-if="retryAfterSeconds > 0" data-testid="throttle-countdown" class="text-destructive">
      {{ t('auth.throttled', { seconds: retryAfterSeconds }) }}
    </p>
  </div>
</template>
