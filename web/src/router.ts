import { onScopeDispose, ref } from 'vue'

export interface AppRoute {
  path: string
  /** Clé de libellé `nav.<navKey>` dans les fichiers de langue. */
  navKey: string
}

/** Pages de l'application, dans l'ordre d'affichage. */
export const ROUTES: readonly AppRoute[] = [
  { path: '/assistant', navKey: 'assistant' },
  { path: '/demarrage', navKey: 'demarrage' },
  { path: '/propositions', navKey: 'propositions' },
  { path: '/deduplication', navKey: 'deduplication' },
  { path: '/liens', navKey: 'liens' },
  { path: '/etapes', navKey: 'etapes' },
  { path: '/sources', navKey: 'sources' },
  { path: '/volumes', navKey: 'volumes' },
  { path: '/configuration', navKey: 'configuration' },
  { path: '/compte', navKey: 'compte' },
]

const DEFAULT_PATH = '/propositions'

/** pathFromHash lit `#/chemin` ; un chemin inconnu retombe sur la vue par défaut. */
export function pathFromHash(hash: string): string {
  const path = hash.replace(/^#/, '')
  return ROUTES.some((route) => route.path === path) ? path : DEFAULT_PATH
}

export const currentPath = ref(pathFromHash(window.location.hash))

/** navigate change d'onglet : le hash reste côté client, le serveur ne voit jamais le chemin. */
export function navigate(path: string): void {
  window.location.hash = path
  currentPath.value = pathFromHash(window.location.hash)
}

/** useHashRouter suit les changements de hash (bouton retour, lien) tant que le composant est monté. */
export function useHashRouter(): void {
  const onChange = () => {
    currentPath.value = pathFromHash(window.location.hash)
  }
  onChange()
  window.addEventListener('hashchange', onChange)
  onScopeDispose(() => window.removeEventListener('hashchange', onChange))
}
