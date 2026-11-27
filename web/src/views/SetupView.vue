<script setup lang="ts">
import { computed, onMounted, onUnmounted } from 'vue'
import StatusPill from '@/components/StatusPill.vue'
import { Button } from '@/components/ui/button'
import { displayDetail, useSetup, type SetupStepId } from '@/composables/useSetup'
import { t } from '@/i18n'
import { FIELD_CLASS } from '@/lib/styles'
import { cn } from '@/lib/utils'
import { navigate } from '@/router'

type FormKey =
  | 'arrKind'
  | 'arrUrl'
  | 'arrApiKey'
  | 'clientUrl'
  | 'clientUsername'
  | 'clientPassword'
  | 'libraryRoots'
  | 'linkDir'
  | 'libraryApi'
  | 'libraryToken'
  | 'trackerKey'
  | 'trackerAnnounce'

interface FieldDefinition {
  name: FormKey
  control: 'text' | 'password' | 'textarea' | 'select'
  placeholder?: string
}

const DOCKER_RESTART_COMMAND = 'docker restart necromanciarr'
const BINARY_RESTART_COMMAND = 'necromanciarr serve'
const ARR_KINDS = ['radarr', 'sonarr'] as const
const ARR_PLACEHOLDERS = { radarr: 'http://radarr:7878', sonarr: 'http://sonarr:8989' }
const TEXTAREA_CLASS = cn(FIELD_CLASS, 'h-auto min-h-20 py-2 font-mono')

const setup = useSetup()
const { form } = setup

const FIELDS: Record<SetupStepId, () => FieldDefinition[]> = {
  arr: () => [
    { name: 'arrKind', control: 'select' },
    { name: 'arrUrl', control: 'text', placeholder: ARR_PLACEHOLDERS[form.arrKind] },
    { name: 'arrApiKey', control: 'password' },
  ],
  torrentClient: () => [
    { name: 'clientUrl', control: 'text', placeholder: 'http://qbittorrent:8080' },
    { name: 'clientUsername', control: 'text' },
    { name: 'clientPassword', control: 'password' },
  ],
  folders: () => [
    { name: 'libraryRoots', control: 'textarea', placeholder: '/data/library' },
    { name: 'linkDir', control: 'text', placeholder: '/data/links' },
  ],
  provider: () => [
    { name: 'libraryApi', control: 'text' },
    { name: 'libraryToken', control: 'password' },
    { name: 'trackerKey', control: 'text' },
    { name: 'trackerAnnounce', control: 'textarea' },
  ],
}

const fields = computed(() => FIELDS[setup.currentStep.value]())
const saveLabelKey = computed(() =>
  setup.currentStep.value === 'provider' ? 'setup.finish' : 'setup.save',
)
const resultTone = computed(() => {
  const status = setup.testResult.value?.status
  return status === 'ok' || status === 'warn' ? status : 'fail'
})

function skipSetup(): void {
  setup.skip()
  navigate('/propositions')
}

onMounted(setup.load)
onUnmounted(setup.clearSecrets)
</script>

<template>
  <section class="grid min-w-0 grid-cols-[minmax(0,1fr)] gap-6">
    <div
      v-if="setup.finished.value"
      data-testid="finished"
      class="grid gap-3 rounded-lg border bg-card p-4"
    >
      <h2 class="font-medium">{{ t('setup.finishedTitle') }}</h2>
      <template v-if="setup.restartRequired.value">
        <p class="font-medium">{{ t('setup.restartTitle') }}</p>
        <p class="text-sm text-muted-foreground">{{ t('setup.restartDocker') }}</p>
        <code class="rounded-md bg-muted p-2 font-mono text-sm">{{ DOCKER_RESTART_COMMAND }}</code>
        <p class="text-sm text-muted-foreground">{{ t('setup.restartBinary') }}</p>
        <code class="rounded-md bg-muted p-2 font-mono text-sm">{{ BINARY_RESTART_COMMAND }}</code>
      </template>
      <p v-else class="text-sm text-muted-foreground">{{ t('setup.noRestart') }}</p>
    </div>

    <form v-else class="grid gap-4 rounded-lg border bg-card p-4" @submit.prevent>
      <div class="flex flex-wrap items-center justify-between gap-3">
        <h2 class="font-medium">{{ t(`setup.steps.${setup.currentStep.value}`) }}</h2>
        <p data-testid="step-indicator" class="text-sm text-muted-foreground">
          {{ t('setup.stepOf', { current: setup.stepNumber.value, total: setup.steps.length }) }}
        </p>
      </div>

      <div class="grid gap-4 [grid-template-columns:repeat(auto-fit,minmax(min(100%,22rem),1fr))]">
        <label v-for="field in fields" :key="field.name" class="grid content-start gap-1 text-sm">
          <span class="font-medium">{{ t(`setup.fields.${field.name}`) }}</span>
          <select
            v-if="field.control === 'select'"
            v-model="form[field.name]"
            :data-testid="`field-${field.name}`"
            :class="FIELD_CLASS"
          >
            <option v-for="kind in ARR_KINDS" :key="kind" :value="kind">{{ kind }}</option>
          </select>
          <textarea
            v-else-if="field.control === 'textarea'"
            v-model="form[field.name]"
            :data-testid="`field-${field.name}`"
            :placeholder="field.placeholder"
            :class="TEXTAREA_CLASS"
            spellcheck="false"
          />
          <input
            v-else
            v-model="form[field.name]"
            :data-testid="`field-${field.name}`"
            :type="field.control"
            :placeholder="field.placeholder"
            :autocomplete="field.control === 'password' ? 'new-password' : 'off'"
            :class="FIELD_CLASS"
          />
          <span :data-testid="`help-${field.name}`" class="text-muted-foreground">
            {{ t(`setup.help.${field.name}`) }}
          </span>
        </label>
      </div>

      <div
        v-if="setup.testResult.value"
        data-testid="test-result"
        class="grid gap-1 rounded-md border p-3 text-sm"
        role="status"
      >
        <StatusPill
          :tone="resultTone"
          :label-key="`codes.doctor.${setup.testResult.value.status}`"
        />
        <p>{{ displayDetail(setup.testResult.value.detail) }}</p>
        <p v-if="setup.testResult.value.hint" class="text-muted-foreground">
          {{ setup.testResult.value.hint }}
        </p>
      </div>
      <p
        v-if="setup.trackerMissing.value"
        data-testid="tracker-required"
        class="text-sm text-destructive"
        role="alert"
      >
        {{ t('setup.trackerRequired') }}
      </p>

      <div class="flex flex-wrap gap-2">
        <Button
          v-if="setup.needsTest.value"
          type="button"
          variant="outline"
          data-testid="test"
          :disabled="setup.pending.value"
          @click="setup.runTest"
        >
          {{ t('setup.test') }}
        </Button>
        <template v-if="setup.arrSaved.value && setup.currentStep.value === 'arr'">
          <Button type="button" data-testid="continue" @click="setup.continueAfterArr">
            {{ t('setup.continue') }}
          </Button>
          <Button
            type="button"
            variant="outline"
            data-testid="add-another"
            @click="setup.addAnotherArr"
          >
            {{ t('setup.addAnother') }}
          </Button>
        </template>
        <Button
          v-else
          type="button"
          data-testid="save"
          :disabled="setup.pending.value || !setup.canSave.value"
          @click="setup.saveStep"
        >
          {{ t(saveLabelKey) }}
        </Button>
        <Button type="button" variant="link" data-testid="skip" @click="skipSetup">
          {{ t('setup.skip') }}
        </Button>
      </div>
    </form>
  </section>
</template>
