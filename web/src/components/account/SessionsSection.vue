<script setup lang="ts">
import { onMounted } from 'vue'
import FormAlert from '@/components/FormAlert.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useAuth } from '@/composables/useAuth'
import { useFormRun } from '@/composables/useFormRun'
import { t } from '@/i18n'
import { formatDateTime } from '@/lib/format'

const auth = useAuth()
const form = useFormRun()

onMounted(() => form.run(auth.loadSessions))
</script>

<template>
  <section class="grid content-start gap-3 rounded-lg border bg-card p-4">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <h2 class="font-medium">{{ t('auth.account.sessions.title') }}</h2>
      <div class="flex flex-wrap gap-2">
        <Button
          variant="outline"
          data-testid="logout"
          :disabled="form.busy.value"
          @click="form.run(auth.logout)"
        >
          {{ t('auth.account.sessions.logout') }}
        </Button>
        <Button
          variant="destructive"
          data-testid="logout-all"
          :disabled="form.busy.value"
          @click="form.run(auth.logoutAll)"
        >
          {{ t('auth.account.sessions.logoutAll') }}
        </Button>
      </div>
    </div>
    <ul class="grid gap-3 [grid-template-columns:repeat(auto-fill,minmax(min(100%,18rem),1fr))]">
      <li
        v-for="(session, position) in auth.sessions.value"
        :key="position"
        data-testid="session-item"
        class="grid gap-1 rounded-md border p-3 text-sm"
      >
        <Badge v-if="session.current" variant="secondary">
          {{ t('auth.account.sessions.current') }}
        </Badge>
        <span>{{ t('auth.account.sessions.address', { address: session.address }) }}</span>
        <span class="text-muted-foreground">{{
          t('auth.account.sessions.created', { date: formatDateTime(session.createdAt) })
        }}</span>
        <span class="text-muted-foreground">{{
          t('auth.account.sessions.lastSeen', { date: formatDateTime(session.lastSeenAt) })
        }}</span>
      </li>
    </ul>
    <FormAlert :message="form.message.value" />
  </section>
</template>
