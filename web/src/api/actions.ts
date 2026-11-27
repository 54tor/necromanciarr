// Copie de la table des routes du serveur (internal/web/routes.go). `action` est le nom du registre ; vide pour les
// routes de suivi des jobs, hors registre. Le test `actions.spec.ts` la compare au fichier d'or du test Go.

export interface ApiRoute {
  method: 'GET' | 'POST' | 'PUT' | 'DELETE'
  path: string
  action: string
  mutating: boolean
}

export const API_ROUTES: readonly ApiRoute[] = [
  { method: 'GET', path: '/api/status', action: 'status', mutating: false },
  { method: 'GET', path: '/api/proposals', action: 'proposals list', mutating: false },
  {
    method: 'POST',
    path: '/api/proposals/{id}/accept',
    action: 'proposals accept',
    mutating: true,
  },
  {
    method: 'POST',
    path: '/api/proposals/{id}/reject',
    action: 'proposals reject',
    mutating: true,
  },
  {
    method: 'POST',
    path: '/api/proposals/{id}/choose',
    action: 'proposals choose',
    mutating: true,
  },
  { method: 'POST', path: '/api/proposals/{id}/force', action: 'proposals force', mutating: true },
  {
    method: 'POST',
    path: '/api/groups/{id}/accept',
    action: 'proposals accept-group',
    mutating: true,
  },
  { method: 'POST', path: '/api/stages/{stage}/run', action: 'run', mutating: true },
  { method: 'POST', path: '/api/dedupe/plan', action: 'dedupe plan', mutating: true },
  { method: 'POST', path: '/api/dedupe/{id}/apply', action: 'dedupe apply', mutating: true },
  { method: 'POST', path: '/api/repair/scan', action: 'repair scan', mutating: true },
  { method: 'POST', path: '/api/repair/{id}/apply', action: 'repair apply', mutating: true },
  { method: 'GET', path: '/api/links', action: 'links list', mutating: false },
  { method: 'POST', path: '/api/doctor/run', action: 'doctor', mutating: true },
  { method: 'POST', path: '/api/doctor/checks/{checkId}', action: 'doctor', mutating: true },
  { method: 'GET', path: '/api/sources', action: 'sources list', mutating: false },
  { method: 'POST', path: '/api/sources/{name}/test', action: 'sources test', mutating: true },
  { method: 'GET', path: '/api/volumes', action: 'volumes list', mutating: false },
  { method: 'POST', path: '/api/volumes/{name}/probe', action: 'volumes probe', mutating: true },
  {
    method: 'GET',
    path: '/api/volumes/{name}/commands',
    action: 'volumes commands',
    mutating: false,
  },
  { method: 'GET', path: '/api/config', action: 'config get', mutating: false },
  { method: 'PUT', path: '/api/config/settings/{key}', action: 'config set', mutating: true },
  { method: 'DELETE', path: '/api/config/settings/{key}', action: 'config unset', mutating: true },
  {
    method: 'PUT',
    path: '/api/config/lists/{list}/{itemId}',
    action: 'config item-set',
    mutating: true,
  },
  {
    method: 'DELETE',
    path: '/api/config/lists/{list}/{itemId}',
    action: 'config item-delete',
    mutating: true,
  },
  {
    method: 'POST',
    path: '/api/config/lists/{list}/{itemId}/unset',
    action: 'config item-unset',
    mutating: true,
  },
  { method: 'POST', path: '/api/setup/test', action: '', mutating: false },
  { method: 'GET', path: '/api/jobs', action: '', mutating: false },
  { method: 'GET', path: '/api/jobs/{id}', action: '', mutating: false },
]

/** Actions du registre (une par nom, sans les routes de suivi des jobs). */
export const API_ACTIONS: readonly string[] = [
  ...new Set(API_ROUTES.map((route) => route.action).filter((action) => action !== '')),
]

/**
 * Vue qui porte le déclencheur de chaque action : un élément `data-action="<action>"`. Les actions d'application
 * exigent aussi un déclencheur par ligne (`data-scope="row"`), distinct du déclencheur en masse.
 */
export const UI_TRIGGERS: Readonly<Record<string, string>> = {
  status: 'OnboardingView',
  'proposals list': 'ProposalsView',
  'proposals accept': 'ProposalsView',
  'proposals reject': 'ProposalsView',
  'proposals choose': 'ProposalsView',
  'proposals force': 'ProposalsView',
  'proposals accept-group': 'ProposalsView',
  run: 'StagesView',
  'dedupe plan': 'DedupeView',
  'dedupe apply': 'DedupeView',
  'repair scan': 'LinksView',
  'repair apply': 'LinksView',
  'links list': 'LinksView',
  doctor: 'OnboardingView',
  'sources list': 'SourcesView',
  'sources test': 'SourcesView',
  'volumes list': 'VolumesView',
  'volumes probe': 'VolumesView',
  'volumes commands': 'VolumesView',
  'config get': 'ConfigView',
  'config set': 'ConfigView',
  'config unset': 'ConfigView',
  'config item-set': 'ConfigView',
  'config item-delete': 'ConfigView',
  'config item-unset': 'ConfigView',
}

/** Actions dont le déclencheur existe en version par ligne. */
export const ROW_SCOPED_ACTIONS: readonly string[] = [
  'proposals accept',
  'proposals reject',
  'proposals choose',
  'proposals accept-group',
  'proposals force',
  'dedupe apply',
  'repair apply',
  'doctor',
  'sources test',
  'volumes probe',
  'volumes commands',
  'config set',
  'config item-set',
  'config item-delete',
  'config item-unset',
]
