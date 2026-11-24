// Types des réponses JSON de l'API : les noms de champs sont ceux envoyés par le serveur (snake_case).

export type ProposalKind =
  'full' | 'partial_annex' | 'partial_pack' | 'cross_seed' | 'conflict' | 'dedupe' | 'repair'
export type Decision = 'pending' | 'accepted' | 'rejected' | 'applied' | 'failed' | 'superseded'
export type ProposalStep = 'linked' | 'seeded' | 'seeded_paused_incomplete' | 'forced'
export type CandidateState = 'matched' | 'fetched' | 'verified' | 'failed' | 'stale'

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
  warnings: string[]
  details: ProposalDetails
  maintenance?: DedupeDetails | RepairDetails
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
  warnings?: string[]
  ambiguous?: string[]
  reason?: string
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
  scan_directory?: string
}

export interface MaintenanceProposal<Details> {
  id: number
  decision: Decision
  warnings: string[]
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

export interface ForceResult {
  proposal_id: number
  forced: boolean
  warnings: string[]
}

export interface Status {
  candidates: Partial<Record<CandidateState, number>>
  proposals: Partial<Record<Decision, number>>
  steps: Partial<Record<ProposalStep, number>>
  cross_seed_mode: string
  warnings?: string[]
}

export type StageName = 'scan' | 'match' | 'verify' | 'link' | 'seed'

export interface StageResult {
  stage: StageName
  processed: number
  skipped: number
  failed: number
  reasons?: Record<string, number>
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
  warnings?: string[]
}

export type DoctorStatus = 'ok' | 'warn' | 'fail' | 'skipped'

export interface DoctorCheck {
  id: string
  title: string
  status: DoctorStatus
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
