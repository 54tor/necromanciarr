<script setup lang="ts">
import { computed } from 'vue'
import type { Proposal } from '@/api/types'
import { t } from '@/i18n'
import { formatPercent } from '@/lib/format'

const props = defineProps<{ proposal: Proposal }>()

const details = computed(() => props.proposal.details)
const missingMedia = computed(() => details.value.missing_media_files ?? [])
const ambiguousFiles = computed(() => details.value.ambiguous_files ?? [])
const excludedFiles = computed(() => details.value.excluded_files ?? [])
const members = computed(() => details.value.candidates ?? [])
const hasContent = computed(
  () =>
    [missingMedia, ambiguousFiles, excludedFiles, members].some((list) => list.value.length > 0) ||
    props.proposal.warnings.length > 0,
)
</script>

<template>
  <div class="grid gap-2 py-2 text-sm font-normal whitespace-normal" data-testid="proposal-detail">
    <p v-if="!hasContent" class="text-muted-foreground">{{ t('proposals.details.none') }}</p>
    <section v-if="missingMedia.length > 0">
      <h4 class="font-medium">{{ t('proposals.details.missingMedia') }}</h4>
      <ul class="list-disc pl-5 [overflow-wrap:anywhere]">
        <li v-for="path in missingMedia" :key="path">{{ path }}</li>
      </ul>
    </section>
    <section v-if="ambiguousFiles.length > 0">
      <h4 class="font-medium">{{ t('proposals.details.ambiguous') }}</h4>
      <ul class="list-disc pl-5 [overflow-wrap:anywhere]">
        <li v-for="file in ambiguousFiles" :key="file.torrent_path">
          {{ file.torrent_path }}
          <span class="text-muted-foreground">
            {{ t('proposals.details.localFiles', { files: file.local_files.join(', ') }) }}
          </span>
        </li>
      </ul>
    </section>
    <section v-if="excludedFiles.length > 0">
      <h4 class="font-medium">{{ t('proposals.details.excluded') }}</h4>
      <ul class="list-disc pl-5 [overflow-wrap:anywhere]">
        <li v-for="file in excludedFiles" :key="file.torrent_path">
          {{ file.torrent_path }}
          <span class="text-muted-foreground">
            {{ file.label ?? file.reason }}
          </span>
        </li>
      </ul>
    </section>
    <section v-if="members.length > 0">
      <h4 class="font-medium">{{ t('proposals.details.members') }}</h4>
      <ul class="list-disc pl-5 [overflow-wrap:anywhere]">
        <li v-for="member in members" :key="member.candidate_id">
          {{
            t('proposals.details.member', {
              name: member.name,
              tracker: member.tracker,
              percent: formatPercent(member.percent),
            })
          }}
        </li>
      </ul>
    </section>
    <section v-if="proposal.warnings.length > 0">
      <h4 class="font-medium">{{ t('proposals.details.warnings') }}</h4>
      <ul class="list-disc pl-5 [overflow-wrap:anywhere]">
        <li v-for="(warning, position) in proposal.warnings" :key="`${warning.code}-${position}`">
          {{ warning.text }}
        </li>
      </ul>
    </section>
  </div>
</template>
