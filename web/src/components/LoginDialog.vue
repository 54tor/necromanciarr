<script setup lang="ts">
import { ref } from 'vue'
import { loginRejected, loginRequired, setToken } from '@/api/client'
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

const token = ref('')

function submit(): void {
  const typed = token.value.trim()
  if (!typed) return
  setToken(typed)
  token.value = ''
}
</script>

<template>
  <!-- Modale non fermable : sans jeton valable l'API répond 401, il n'y a rien d'autre à faire. -->
  <Dialog :open="loginRequired">
    <DialogContent
      :show-close-button="false"
      @escape-key-down.prevent
      @pointer-down-outside.prevent
      @interact-outside.prevent
    >
      <DialogHeader>
        <DialogTitle>{{ t('views.login.title') }}</DialogTitle>
        <DialogDescription>
          {{ t('views.login.description') }}
        </DialogDescription>
      </DialogHeader>
      <p
        v-if="loginRejected"
        role="alert"
        class="text-sm text-destructive"
        data-testid="login-rejected"
      >
        {{ t('views.login.rejected') }}
      </p>
      <form class="grid gap-3" @submit.prevent="submit">
        <label for="auth-token" class="text-sm font-medium">{{ t('common.token') }}</label>
        <input
          id="auth-token"
          v-model="token"
          type="password"
          autocomplete="off"
          required
          :class="FIELD_CLASS"
        />
        <Button type="submit" data-testid="login-submit">{{ t('actions.signIn') }}</Button>
      </form>
    </DialogContent>
  </Dialog>
</template>
