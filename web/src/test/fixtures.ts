import type {
  DedupeDetails,
  DoctorReport,
  Job,
  LinkItem,
  LinkPage,
  Proposal,
  RepairDetails,
  Status,
} from '@/api/types'

export function proposal(overrides: Partial<Proposal> = {}): Proposal {
  return {
    id: 1,
    candidate_id: 10,
    candidate_name: 'Film.2020.1080p',
    candidate_size: 4_294_967_296,
    tracker: 'tracker.example.com',
    group_id: 5,
    kind: 'full',
    decision: 'pending',
    primary: true,
    generation: 1,
    current: true,
    warnings: [],
    details: { percent: 99.5, verified_pieces: 199, missing_pieces: 1, total_pieces: 200 },
    ...overrides,
  }
}

export function conflictProposal(): Proposal {
  return proposal({
    id: 7,
    kind: 'conflict',
    group_id: 9,
    candidate_id: 70,
    candidate_name: 'Serie.S01.A',
    details: {
      percent: 100,
      verified_pieces: 10,
      missing_pieces: 0,
      total_pieces: 10,
      candidates: [
        {
          candidate_id: 70,
          name: 'Serie.S01.A',
          tracker: 'a.example.com',
          state: 'verified',
          percent: 100,
          proposal_id: 7,
        },
        {
          candidate_id: 71,
          name: 'Serie.S01.B',
          tracker: 'b.example.com',
          state: 'verified',
          percent: 100,
          proposal_id: 8,
        },
      ],
    },
  })
}

export const doctorReport: DoctorReport = {
  worst: 'fail',
  checks: [
    {
      id: 'config',
      title: 'Configuration',
      status: 'ok',
      detail: 'Configuration valide.',
      durationMs: 1,
    },
    {
      id: 'qbit',
      title: 'Client qBittorrent',
      status: 'fail',
      detail: 'Connexion refusée.',
      hint: 'Vérifiez l’adresse du client.',
      durationMs: 12,
    },
    { id: 'links', title: 'Liens', status: 'warn', detail: 'Volumes distincts.', durationMs: 3 },
    {
      id: 'provider',
      title: 'Fournisseur de bibliothèque',
      status: 'skipped',
      detail: 'Non configuré.',
      durationMs: 0,
    },
  ],
}

export const okReport: DoctorReport = {
  worst: 'ok',
  checks: doctorReport.checks.map((check) => ({ ...check, status: 'ok' })),
}

export const busyStatus: Status = {
  candidates: { verified: 3 },
  proposals: { pending: 2 },
  steps: {},
  cross_seed_mode: 'disabled',
}

export function job(overrides: Partial<Job> = {}): Job {
  return {
    id: 1,
    action: 'run',
    state: 'running',
    started_at: '2026-01-01T10:00:00Z',
    ...overrides,
  }
}

export const dedupeDetails: DedupeDetails = {
  torrent_path: '/data/torrents/Film.mkv',
  library_path: '/data/library/Film.mkv',
  kind: 'hardlink',
  size: 3_000_000_000,
  saved_bytes: 3_000_000_000,
  bytes_to_read: 1_000_000,
  method: 'sampled',
  actionable: true,
}

export function maintenanceProposal(
  id: number,
  details: DedupeDetails | RepairDetails,
  decision: Proposal['decision'] = 'pending',
): Proposal {
  return proposal({ id, kind: 'dedupe', decision, maintenance: details })
}

export const repairDetails: RepairDetails = {
  status: 'repairable',
  link_path: '/data/library/Film.mkv',
  old_target: '/data/old/Film.mkv',
  new_target: '/data/new/Film.mkv',
  size: 1_000_000,
  method: 'fingerprint',
  relative: false,
  actionable: true,
}

export function linkItem(id: number, overrides: Partial<LinkItem> = {}): LinkItem {
  return {
    id,
    link_path: `/data/links/Film.${id}.mkv`,
    target_path: `/data/library/Film.${id}.mkv`,
    kind: 'symlink',
    state: 'ok',
    recorded_at: '2026-01-01T10:00:00Z',
    size: 1_000_000,
    compared: true,
    ...overrides,
  }
}

export function linkPage(items: LinkItem[], overrides: Partial<LinkPage> = {}): LinkPage {
  return {
    items,
    total: items.length,
    truncated: false,
    counts: { ok: 1, diverged: 1, lost: 1, unreadable: 1 },
    ...overrides,
  }
}
