import { request } from './client'
import type {
  ConfigSnapshot,
  DecisionResult,
  DoctorReport,
  ForceResult,
  GroupResult,
  Job,
  LinkFilters,
  LinkPage,
  Proposal,
  ProposalFilters,
  SetupCandidate,
  SetupTestResult,
  SourceRow,
  SourceTestResult,
  VolumeCommands,
  VolumeProbeResult,
  VolumeRow,
  StageName,
  Status,
} from './types'

const encoded = encodeURIComponent
const itemPath = (list: string, itemId: string) =>
  `/api/config/lists/${encoded(list)}/${encoded(itemId)}`

const post = <Result>(path: string, body: unknown = {}) => request<Result>('POST', path, body)

function queryOf(filters: ProposalFilters): string {
  const query = new URLSearchParams()
  if (filters.decision) query.set('decision', filters.decision)
  if (filters.kind) query.set('kind', filters.kind)
  if (filters.group) query.set('group', String(filters.group))
  if (filters.current_only) query.set('current_only', 'true')
  if (filters.source) query.set('source', filters.source)
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
  listSources: () => request<SourceRow[]>('GET', '/api/sources'),
  testSource: (name: string) =>
    post<SourceTestResult>(`/api/sources/${encodeURIComponent(name)}/test`, {}),
  listVolumes: () => request<VolumeRow[]>('GET', '/api/volumes'),
  probeVolume: (name: string) =>
    post<VolumeProbeResult>(`/api/volumes/${encodeURIComponent(name)}/probe`, {}),
  volumeCommands: (name: string) =>
    request<VolumeCommands>('GET', `/api/volumes/${encodeURIComponent(name)}/commands`),
  testCandidate: (candidate: SetupCandidate) => post<SetupTestResult>('/api/setup/test', candidate),
  applyFix: (fixId: string, target: string) =>
    post<Record<string, never>>(`/api/setup/fix/${encodeURIComponent(fixId)}`, { target }),
  getConfig: () => request<ConfigSnapshot>('GET', '/api/config'),
  putConfigSetting: (key: string, value: unknown) =>
    request<ConfigSnapshot>('PUT', `/api/config/settings/${encoded(key)}`, { value }),
  unsetConfigSetting: (key: string) =>
    request<ConfigSnapshot>('DELETE', `/api/config/settings/${encoded(key)}`),
  putConfigItem: (list: string, itemId: string, element: Record<string, unknown>) =>
    request<ConfigSnapshot>('PUT', itemPath(list, itemId), { element }),
  deleteConfigItem: (list: string, itemId: string) =>
    request<ConfigSnapshot>('DELETE', itemPath(list, itemId)),
  unsetConfigItem: (list: string, itemId: string) =>
    post<ConfigSnapshot>(`${itemPath(list, itemId)}/unset`),
  listJobs: () => request<Job[]>('GET', '/api/jobs'),
  getJob: (jobId: number) => request<Job>('GET', `/api/jobs/${jobId}`),
}
