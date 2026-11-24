import { ref } from 'vue'
import { api } from '@/api/endpoints'
import type { LinkItem, LinkPage, LinkState } from '@/api/types'
import { useApi } from './useApi'

/** Taille de page demandée au serveur : le tableau en déduit le décalage des pages voisines. */
export const LINKS_PAGE_SIZE = 200

/** Ordre d'affichage des états (filtre et effectifs). */
export const LINK_STATES: readonly LinkState[] = ['ok', 'diverged', 'lost', 'unreadable']

/** useRecordedLinks porte la page courante des liens enregistrés, son filtre d'état et son décalage serveur. */
export function useRecordedLinks() {
  const { call, pending } = useApi()
  const state = ref<LinkState | ''>('')
  const offset = ref(0)
  const items = ref<LinkItem[]>([])
  const page = ref<LinkPage>()
  const loadFailed = ref(false)

  // Numéro de la dernière requête : une réponse plus ancienne (filtre changé en cours de route) est ignorée.
  let latestRequest = 0

  /** load charge la page commençant au décalage donné ; en cas d'échec, le décalage et la page affichés restent. */
  async function load(targetOffset = offset.value): Promise<void> {
    const request = ++latestRequest
    const loaded = await call(() =>
      api.listLinks({ state: state.value, offset: targetOffset, limit: LINKS_PAGE_SIZE }),
    )
    if (request !== latestRequest) return
    loadFailed.value = loaded === undefined
    if (!loaded) return
    offset.value = targetOffset
    page.value = loaded
    items.value = loaded.items
  }

  /** reloadFirstPage revient au début : changement de filtre ou nouvel examen. */
  function reloadFirstPage(): Promise<void> {
    return load(0)
  }

  return { state, offset, items, page, pending, loadFailed, load, reloadFirstPage }
}
