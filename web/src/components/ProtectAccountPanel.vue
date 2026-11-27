<script setup lang="ts">
import { Button } from '@/components/ui/button'
import { useAuth } from '@/composables/useAuth'
import { t } from '@/i18n'
import { navigate } from '@/router'

const { accountJustCreated } = useAuth()

function openAccount(): void {
  accountJustCreated.value = false
  navigate('/compte')
}
</script>

<template>
  <aside
    data-testid="protect-account"
    class="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-primary bg-card p-4"
  >
    <div class="grid gap-1">
      <h2 class="font-medium">{{ t('auth.protect.title') }}</h2>
      <p class="text-sm text-muted-foreground">{{ t('auth.protect.body') }}</p>
    </div>
    <div class="flex flex-wrap gap-2">
      <Button data-testid="protect-open" @click="openAccount">{{ t('auth.protect.open') }}</Button>
      <Button variant="ghost" data-testid="protect-later" @click="accountJustCreated = false">
        {{ t('auth.protect.later') }}
      </Button>
    </div>
  </aside>
</template>
