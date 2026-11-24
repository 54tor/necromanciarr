import { request } from './client'
import type {
  DecisionResult,
  DoctorReport,
  ForceResult,
  GroupResult,
  Job,
  LinkFilters,
  LinkPage,
  Proposal,
  ProposalFilters,
  StageName,
  Status,
} from './types'

const post = <Result>(path: string, body: unknown = {}) => request<Result>('POST', path, body)

function queryOf(filters: ProposalFilters): string {
  const query = new URLSearchParams()
  if (filters.decision) query.set('decision', filters.decision)
  if (filters.kind) query.set('kind', filters.kind)
  if (filters.group) query.set('group', String(filters.group))
  if (filters.current_only) query.set('current_only', 'true')
  const text = query.toString()
  return text ? `?${text}` : ''
}

function linkQueryOf(filters: LinkFilters): string {
  const query = new URLSearchParams()
  if (filters.state) query.set('state', filters.state)
  if (filters.limit) query.set('limit', String(filters.limit))
  if (filters.offset) query.set('offset', String(filters.offset))
  const text = query.toString()
  return text ? `?${text}` : ''
}

export const api = {
  status: () => request<Status>('GET', '/api/status'),
  listProposals: (filters: ProposalFilters = {}) =>
    request<Proposal[]>('GET', `/api/proposals${queryOf(filters)}`),
  accept: (proposalId: number, allowPartialPack = false) =>
    post<DecisionResult>(`/api/proposals/${proposalId}/accept`, {
      allow_partial_pack: allowPartialPack,
    }),
  reject: (proposalId: number) => post<DecisionResult>(`/api/proposals/${proposalId}/reject`),
  choose: (proposalId: number, candidateId: number) =>
    post<DecisionResult>(`/api/proposals/${proposalId}/choose`, { candidate_id: candidateId }),
  force: (proposalId: number) =>
    post<ForceResult>(`/api/proposals/${proposalId}/force`, { confirm: true }),
  acceptGroup: (groupId: number) => post<GroupResult>(`/api/groups/${groupId}/accept`),
  runStage: (stage: StageName) => post<Job>(`/api/stages/${stage}/run`),
  dedupePlan: () => post<Job>('/api/dedupe/plan'),
  dedupeApply: (proposalId: number, dryRun: boolean, acceptPending: boolean) =>
    post<Job>(`/api/dedupe/${proposalId}/apply`, {
      dry_run: dryRun,
      accept_pending: acceptPending,
    }),
  repairScan: (directory: string) => post<Job>('/api/repair/scan', { directory }),
  repairApply: (proposalId: number, dryRun: boolean, acceptPending: boolean) =>
    post<Job>(`/api/repair/${proposalId}/apply`, {
      dry_run: dryRun,
      accept_pending: acceptPending,
    }),
  listLinks: (filters: LinkFilters = {}) =>
    request<LinkPage>('GET', `/api/links${linkQueryOf(filters)}`),
  doctor: () => post<DoctorReport>('/api/doctor/run', {}),
  doctorCheck: (checkId: string) =>
    post<DoctorReport>(`/api/doctor/checks/${encodeURIComponent(checkId)}`, {}),
  listJobs: () => request<Job[]>('GET', '/api/jobs'),
  getJob: (jobId: number) => request<Job>('GET', `/api/jobs/${jobId}`),
}
