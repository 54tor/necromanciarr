<script setup lang="ts">
import { RefreshCw, Search } from '@lucide/vue'
import { onMounted, ref, watch } from 'vue'
import { api } from '@/api/endpoints'
import type { LinkItem, LinkPage, LinkState } from '@/api/types'
import LinkStateBadge from '@/components/LinkStateBadge.vue'
import { Button } from '@/components/ui/button'
import { useApi } from '@/composables/useApi'
import { lastFinished } from '@/composables/useJobs'
import { formatBytes } from '@/lib/format'
import { LINK_KIND_LABELS, LINK_STATE_LABELS, LINK_STATES, labelOf } from '@/lib/labels'
import { FIELD_CLASS } from '@/lib/styles'

defineProps<{ busy: boolean }>()
const emit = defineEmits<{ repair: [] }>()

const { call, pending } = useApi()
const state = ref<LinkState | ''>('')
const items = ref<LinkItem[]>([])
const page = ref<LinkPage>()

/** load recharge la première page ; more ajoute la page suivante à la liste affichée. */
async function load(): Promise<void> {
  const loaded = await call(() => api.listLinks({ state: state.value }))
  if (!loaded) return
  page.value = loaded
  items.value = loaded.items
}

async function more(): Promise<void> {
  const offset = page.value?.next
  if (!offset) return
  const loaded = await call(() => api.listLinks({ state: state.value, offset }))
  if (!loaded) return
  page.value = loaded
  items.value = [...items.value, ...loaded.items]
}

onMounted(load)
watch(state, () => void load())
watch(lastFinished, () => void load())
</script>

<template>
  <section aria-labelledby="recorded-links-title" class="grid gap-3">
    <h3 id="recorded-links-title" class="text-lg font-semibold">Liens enregistrés</h3>
    <form class="flex flex-wrap items-end gap-3" @submit.prevent="load">
      <label class="grid min-w-48 gap-1 text-sm">
        État
        <select v-model="state" :class="FIELD_CLASS" data-testid="link-state-filter">
          <option value="">Tous</option>
          <option v-for="option in LINK_STATES" :key="option" :value="option">
            {{ LINK_STATE_LABELS[option] }}
          </option>
        </select>
      </label>
      <Button type="submit" variant="outline" data-action="links list" :disabled="pending">
        <RefreshCw aria-hidden="true" /> Actualiser
      </Button>
    </form>

    <ul
      v-if="page"
      class="flex flex-wrap gap-2 text-sm"
      aria-label="Effectifs par état"
      data-testid="link-counts"
    >
      <li v-for="option in LINK_STATES" :key="option" class="flex items-center gap-1">
        <LinkStateBadge :state="option" />
        <span :data-testid="`count-${option}`">{{ page.counts[option] ?? 0 }}</span>
      </li>
    </ul>
    <p
      v-if="page?.truncated"
      role="status"
      class="rounded-lg border border-chart-4 p-3 text-sm"
      data-testid="links-truncated"
    >
      Examen interrompu faute de temps (montage lent ou figé) : la liste et les effectifs sont
      partiels.
    </p>

    <p
      v-if="page && items.length === 0"
      class="rounded-lg border bg-card p-6 text-muted-foreground"
      data-testid="links-empty"
    >
      Aucun lien enregistré{{ state ? ' dans cet état' : '' }}.
    </p>
    <ul class="grid gap-3 sm:grid-cols-2" aria-label="Liens enregistrés">
      <li
        v-for="link in items"
        :key="link.id"
        class="grid content-start gap-2 rounded-lg border bg-card p-4"
        :data-testid="`link-${link.id}`"
      >
        <div class="flex flex-wrap items-center gap-2">
          <LinkStateBadge :state="link.state" />
          <span class="text-sm text-muted-foreground">{{
            labelOf(LINK_KIND_LABELS, link.kind)
          }}</span>
          <span v-if="link.size" class="text-sm text-muted-foreground">{{
            formatBytes(link.size)
          }}</span>
        </div>
        <dl class="grid gap-1 text-sm">
          <div>
            <dt class="mr-1 inline font-medium">Lien :</dt>
            <dd class="inline break-all">{{ link.link_path }}</dd>
          </div>
          <div>
            <dt class="mr-1 inline font-medium">Cible :</dt>
            <dd class="inline break-all">{{ link.target_path }}</dd>
          </div>
          <div v-if="link.reason">
            <dt class="mr-1 inline font-medium">Motif :</dt>
            <dd class="inline break-all">{{ link.reason }}</dd>
          </div>
        </dl>
        <div v-if="link.state !== 'ok'">
          <Button
            size="sm"
            variant="outline"
            data-action="repair scan"
            data-testid="search-repair"
            :disabled="busy"
            @click="emit('repair')"
          >
            <Search aria-hidden="true" /> Chercher une réparation
          </Button>
        </div>
      </li>
    </ul>
    <div v-if="page?.next" class="flex items-center gap-3 text-sm">
      <Button variant="outline" data-testid="links-more" :disabled="pending" @click="more">
        Charger plus
      </Button>
      <span class="text-muted-foreground">{{ items.length }} sur {{ page.total }}</span>
    </div>
  </section>
</template>
