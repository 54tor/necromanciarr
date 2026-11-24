import type { DoctorCheck } from '@/api/types'

const DOCTOR_FAMILIES = [
  'configuration',
  'services',
  'storage',
  'sources',
  'volumes',
  'other',
] as const
export type DoctorFamily = (typeof DOCTOR_FAMILIES)[number]

/** Préfixes d'identifiant de contrôle par famille ; le premier qui correspond l'emporte. */
const FAMILY_PREFIXES: Readonly<Record<Exclude<DoctorFamily, 'other'>, readonly string[]>> = {
  configuration: ['config', 'auth', 'path-mappings'],
  services: ['arr:', 'qbit'],
  storage: ['library-roots', 'link', 'dedupe'],
  sources: ['library-api', 'provider', 'cross-seed', 'source', 'torznab'],
  volumes: ['fs-providers', 'volume', 'sftp'],
}

/** familyOf classe un contrôle d'après le préfixe de son identifiant ; un identifiant inconnu va dans « other ». */
export function familyOf(checkId: string): DoctorFamily {
  for (const [family, prefixes] of Object.entries(FAMILY_PREFIXES)) {
    if (prefixes.some((prefix) => checkId.startsWith(prefix))) return family as DoctorFamily
  }
  return 'other'
}

/** groupByFamily garde l'ordre du serveur dans chaque famille et omet les familles vides. */
export function groupByFamily(
  checks: readonly DoctorCheck[],
): { family: DoctorFamily; checks: DoctorCheck[] }[] {
  return DOCTOR_FAMILIES.map((family) => ({
    family,
    checks: checks.filter((check) => familyOf(check.id) === family),
  })).filter((group) => group.checks.length > 0)
}
