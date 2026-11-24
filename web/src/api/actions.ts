// Copie de la table des routes du serveur (internal/web/routes.go) : action du registre, méthode et chemin.
// Les routes de suivi des jobs sont hors registre (justifiées côté Go) et n'ont pas de bouton propre.

export interface ApiAction {
  action: string
  method: 'GET' | 'POST'
  path: string
}

export const API_ACTIONS: readonly ApiAction[] = [
  { action: 'status', method: 'GET', path: '/api/status' },
  { action: 'proposals list', method: 'GET', path: '/api/proposals' },
  { action: 'proposals accept', method: 'POST', path: '/api/proposals/{id}/accept' },
  { action: 'proposals reject', method: 'POST', path: '/api/proposals/{id}/reject' },
  { action: 'proposals choose', method: 'POST', path: '/api/proposals/{id}/choose' },
  { action: 'proposals force', method: 'POST', path: '/api/proposals/{id}/force' },
  { action: 'proposals accept-group', method: 'POST', path: '/api/groups/{id}/accept' },
  { action: 'run', method: 'POST', path: '/api/stages/{stage}/run' },
  { action: 'dedupe plan', method: 'POST', path: '/api/dedupe/plan' },
  { action: 'dedupe apply', method: 'POST', path: '/api/dedupe/{id}/apply' },
  { action: 'repair scan', method: 'POST', path: '/api/repair/scan' },
  { action: 'repair apply', method: 'POST', path: '/api/repair/{id}/apply' },
  { action: 'doctor', method: 'GET', path: '/api/doctor' },
]

/**
 * Déclencheur d'interface de chaque action : la vue qui porte un élément `data-action="<action>"`.
 * `status` et `proposals list` se déclenchent par chargement ou actualisation (bouton « Actualiser »).
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
  doctor: 'OnboardingView',
}
