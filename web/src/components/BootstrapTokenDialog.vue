<script setup lang="ts">
import { ref } from 'vue'
import { setToken, tokenRejected } from '@/api/client'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { t } from '@/i18n'
import { FIELD_CLASS } from '@/lib/styles'

defineProps<{ open: boolean }>()

const token = ref('')

function submit(): void {
  const typed = token.value.trim()
  token.value = ''
  if (typed) setToken(typed)
}
</script>

<template>
  <!-- Modale non fermable : sans compte, seul le jeton d'amorçage permet de le créer. -->
  <Dialog :open="open">
    <DialogContent
      :show-close-button="false"
      @escape-key-down.prevent
      @pointer-down-outside.prevent
      @interact-outside.prevent
    >
      <DialogHeader>
        <DialogTitle>{{ t('auth.token.title') }}</DialogTitle>
        <DialogDescription>{{ t('auth.token.description') }}</DialogDescription>
      </DialogHeader>
      <p
        v-if="tokenRejected"
        role="alert"
        class="text-sm text-destructive"
        data-testid="token-rejected"
      >
        {{ t('auth.token.rejected') }}
      </p>
      <form class="grid gap-3" @submit.prevent="submit">
        <label for="auth-token" class="text-sm font-medium">{{ t('auth.token.label') }}</label>
        <input
          id="auth-token"
          v-model="token"
          type="password"
          autocomplete="off"
          required
          :class="FIELD_CLASS"
        />
        <Button type="submit" data-testid="token-submit">{{ t('auth.token.submit') }}</Button>
      </form>
    </DialogContent>
  </Dialog>
</template>
