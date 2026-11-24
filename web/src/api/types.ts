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
}

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
}
