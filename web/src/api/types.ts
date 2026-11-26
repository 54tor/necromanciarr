// Types des réponses JSON de l'API : les noms de champs sont ceux envoyés par le serveur (snake_case).

export type ProposalKind =
  'full' | 'partial_annex' | 'partial_pack' | 'cross_seed' | 'conflict' | 'dedupe' | 'repair'
export type Decision = 'pending' | 'accepted' | 'rejected' | 'applied' | 'failed' | 'superseded'
type ProposalStep = 'linked' | 'seeded' | 'seeded_paused_incomplete' | 'forced'
type CandidateState = 'matched' | 'fetched' | 'verified' | 'failed' | 'stale'

interface AmbiguousFile {
  torrent_path: string
  local_files: string[]
}

interface ExcludedFile {
  torrent_path: string
  reason: string
  /** Libellé traduit de `reason`. */
  label?: string
}

interface GroupMember {
  candidate_id: number
  name: string
  tracker: string
  state: CandidateState
  kind?: ProposalKind
  proposal_id?: number
  percent: number
}

interface ProposalDetails {
  percent: number
  verified_pieces: number
  missing_pieces: number
  total_pieces: number
  missing_media_files?: string[]
  ambiguous_files?: AmbiguousFile[]
  excluded_files?: ExcludedFile[]
  candidates?: GroupMember[]
  /** Calculés à chaque lecture depuis la section `trackers` ; absents d'un serveur plus ancien. */
  seedable?: boolean
  announce_source?: AnnounceSource
  /** Source du candidat : `provider` ou `torznab:<nom>`, jamais une URL ; absente d'un serveur plus ancien. */
  source?: string
}

/** Origine de l'annonce injectée au seed : configuration, `.torrent` d'indexeur, ou aucune (`no_announce`). */
type AnnounceSource = 'config' | 'indexer' | 'none'

export interface Proposal {
  id: number
  candidate_id: number
  candidate_name: string
  candidate_size: number
  tracker: string
  group_id: number
  kind: ProposalKind
  decision: Decision
  step?: ProposalStep
  primary: boolean
  generation: number
  current: boolean
  warnings: Warning[]
  /** Dernière activité de la proposition (RFC 3339 UTC) ; vide si inconnue. */
  updated_at: string
  details: ProposalDetails
  maintenance?: DedupeDetails | RepairDetails
}

/** Avertissement de proposition : `code` est la clé stable du message, `text` son rendu dans la langue. */
export interface Warning {
  code: string
  text: string
}

export interface DedupeDetails {
  torrent_path: string
  library_path?: string
  kind?: string
  size: number
  saved_bytes: number
  bytes_to_read: number
  method: string
  hash_algorithm?: string
  /** Clés stables des avertissements du plan (jamais traduites) ; le texte est dans les avertissements de la proposition. */
  warnings?: string[]
  ambiguous?: string[]
  reason?: string
  /** Libellé traduit de `reason`. */
  label?: string
  actionable: boolean
}

type RepairStatus = 'repairable' | 'ambiguous' | 'lost'

export interface RepairDetails {
  status: RepairStatus
  link_path: string
  old_target?: string
  new_target?: string
  size?: number
  method?: string
  candidates?: string[]
  relative: boolean
  actionable: boolean
  reason?: 'needs_client_recheck'
  scan_directory?: string
}

export type LinkState = 'ok' | 'diverged' | 'lost' | 'unreadable'

export interface LinkItem {
  id: number
  link_path: string
  target_path: string
  kind: string
  state: LinkState
  recorded_at: string
  size?: number
  /** Code stable du motif (`link_target_missing`…), jamais traduit. */
  reason?: string
  /** Libellé traduit de `reason`. */
  label?: string
  /** Fournisseur sftp du lien ; vide pour le disque local. */
  volume: string
  /** Faux quand rien n'a été comparé (reflink ou copie sans taille enregistrée). */
  compared: boolean
}

/** Page de `GET /api/links` : `next` est l'offset de la page suivante (absent à la fin). */
export interface LinkPage {
  items: LinkItem[]
  total: number
  next?: number
  truncated: boolean
  counts: Record<LinkState, number>
}

export interface LinkFilters {
  state?: LinkState | ''
  limit?: number
  offset?: number
}

export interface MaintenanceProposal<Details> {
  id: number
  decision: Decision
  warnings: Warning[]
  details: Details
}

export interface DecisionResult {
  proposal_id: number
  action: 'accepted' | 'rejected' | 'chosen'
}

export interface GroupResult {
  group_id: number
  accepted: number[]
}

interface IsolatedFile {
  link_path: string
  kind: 'reflink' | 'copy'
}

export interface ForceResult {
  proposal_id: number
  forced: boolean
  warnings: Warning[]
  replaced?: IsolatedFile[]
}

export interface Status {
  candidates: Partial<Record<CandidateState, number>>
  proposals: Partial<Record<Decision, number>>
  steps: Partial<Record<ProposalStep, number>>
  cross_seed_mode: string
  /** Langue des textes rendus par le serveur. */
  language: 'fr' | 'en'
  warnings?: Warning[]
  /** Torrents vérifiés non proposés (verdict mis de côté), avec la raison. */
  held?: HeldCandidate[]
  /** Volumes sftp sans session (état `broken`) ; absent d'un serveur plus ancien. */
  volumes_broken?: number
}

interface HeldCandidate {
  candidate_id: number
  candidate_name: string
  tracker: string
  kind: ProposalKind
  reason: string
  percent: number
  missing_pieces: number
  total_pieces: number
}

export type StageName = 'scan' | 'match' | 'verify' | 'link' | 'seed'

export interface StageResult {
  stage: StageName
  processed: number
  skipped: number
  failed: number
  reasons?: Record<string, number>
  /** Libellés traduits des motifs, dans la langue du serveur. */
  reason_labels?: Record<string, string>
  unidentified_files: number
  unidentified_samples?: string[]
  candidates_created: number
  candidates_updated: number
  candidates_unchanged: number
  candidates_stale: number
  verified: number
  partial_annex: number
  partial_pack: number
  conflicts: number
  resumed: number
  proposals_created: number
  linked: number
  seeded: number
  seeded_paused_incomplete: number
  warnings?: Warning[]
}

export type DoctorStatus = 'ok' | 'warn' | 'fail' | 'skipped'

export interface DoctorCheck {
  id: string
  title: string
  status: DoctorStatus
  /** Code stable du résultat (identique dans les deux langues). */
  code: string
  detail: string
  hint?: string
  durationMs: number
}

export interface DoctorReport {
  checks: DoctorCheck[]
  worst: DoctorStatus
}

export interface ApiErrorBody {
  code: string
  message: string
  /** Raison, sans valeur, d'un refus d'écriture de configuration. */
  detail?: string
}

type JobState = 'running' | 'done' | 'failed'

export interface Job {
  id: number
  action: string
  state: JobState
  started_at: string
  finished_at?: string
  result?: unknown
  error?: ApiErrorBody
}

export interface ProposalFilters {
  decision?: Decision
  kind?: ProposalKind
  group?: number
  current_only?: boolean
  source?: string
}

/** Usage d'un indexeur sur 24 h face à son plafond du jour. */
export interface SourceUsage {
  used: number
  cap: number
}

export type SourceState = 'ok' | 'blocked' | 'exhausted' | 'disabled'

/** Ligne de `GET /api/sources` : `order` 0 = hors chaîne ; usage `null` pour le fournisseur ; `last_error` est un code. */
export interface SourceRow {
  order: number
  source: string
  type: 'provider' | 'torznab'
  tracker: string
  state: SourceState
  queries: SourceUsage | null
  downloads: SourceUsage | null
  last_error: string
  /** Capacités de l'indexeur en cache dans le processus du serveur (24 h). */
  caps_cached: boolean
}

/** Résultat de `POST /api/sources/{name}/test` : `error` est un code de raison stable. */
export interface SourceTestResult {
  source: string
  result: 'ok' | 'failed'
  error?: string
}

export type VolumeState = 'ok' | 'broken' | 'unknown'

/** Dernière sonde d'un volume : `identity` est l'identité distante des fichiers ; `reasons` des codes. */
export interface VolumeCapabilities {
  symlink: boolean
  hardlink: boolean
  atomic_replace: boolean
  server_copy: boolean
  identity: boolean
  hashes: string[]
  reasons: Record<string, string>
}

/** Ligne de `GET /api/volumes` : `volume` vide pour le disque local ; `address` hôte:port seul. */
export interface VolumeRow {
  volume: string
  type: 'local' | 'sftp'
  address: string
  state: VolumeState
  capabilities: VolumeCapabilities | null
  probed_at: string | null
}

/** Résultat de `POST /api/volumes/{name}/probe` : `error` est un code de raison stable. */
export interface VolumeProbeResult {
  volume: string
  result: 'ok' | 'failed'
  error?: string
  capabilities?: VolumeCapabilities
  probed_at?: string
}

/** Réponse de `GET /api/volumes/{name}/commands` : lignes exactes, tabulations comprises. */
export interface VolumeCommands {
  volume: string
  lines: string[]
  guard_script: string
}

/** Origine d'une valeur de configuration ; un élément de liste dit `cross-seed`, un réglage `crossseed`. */
export type ConfigOrigin =
  'default' | 'crossseed' | 'cross-seed' | 'override' | 'added' | 'env' | 'flag'

/** Réglage de `GET /api/config` : `value` nul pour un secret, `set` dit s'il existe. */
export interface ConfigSetting {
  value: unknown
  origin: ConfigOrigin
  locked: boolean
  secret: boolean
  set: boolean
}

/** Élément de liste résolu ; un champ secret présent vaut null. */
export interface ConfigItem {
  itemId: string
  element: Record<string, unknown>
  origin: ConfigOrigin
  drift: boolean
}

/** Surcharge dont l'élément cross-seed a disparu (noms Go sans balise JSON). */
export interface ConfigOrphan {
  List: string
  ItemID: string
}

/** Réponse de `GET /api/config` et de chaque écriture : la configuration relue, sans aucun secret. */
export interface ConfigSnapshot {
  settings: Record<string, ConfigSetting> | null
  lists: Record<string, ConfigItem[] | null> | null
  orphans: ConfigOrphan[] | null
  restartRequired: boolean
}
