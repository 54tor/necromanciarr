import { currentLanguage } from '@/i18n'

const BYTE_UNITS = ['o', 'Kio', 'Mio', 'Gio', 'Tio'] as const
const BYTES_PER_UNIT = 1024

/** formatBytes écrit une taille en octets avec une unité binaire française (1 536 -> « 1,5 Kio »). */
export function formatBytes(bytes: number): string {
  let value = bytes
  let unitIndex = 0
  while (value >= BYTES_PER_UNIT && unitIndex < BYTE_UNITS.length - 1) {
    value /= BYTES_PER_UNIT
    unitIndex += 1
  }
  const digits = unitIndex === 0 || value >= 100 ? 0 : 1
  return `${value.toLocaleString('fr-FR', { maximumFractionDigits: digits })} ${BYTE_UNITS[unitIndex]}`
}

/** formatPercent arrondit un pourcentage à une décimale (« 99,9 % »). */
export function formatPercent(percent: number): string {
  return `${percent.toLocaleString('fr-FR', { maximumFractionDigits: 1 })} %`
}

/** formatDateTime affiche un horodatage ISO dans la langue active ; une valeur illisible est rendue telle quelle. */
export function formatDateTime(iso: string): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return iso
  return date.toLocaleString(currentLanguage(), { dateStyle: 'short', timeStyle: 'medium' })
}

const RELATIVE_UNITS: readonly [Intl.RelativeTimeFormatUnit, number][] = [
  ['year', 31_536_000],
  ['month', 2_592_000],
  ['day', 86_400],
  ['hour', 3_600],
  ['minute', 60],
]

/** formatRelative écrit un instant par rapport à `now` (« il y a 3 jours »), dans la langue active ; illisible : tel quel. */
export function formatRelative(iso: string, now: Date = new Date()): string {
  const instant = new Date(iso)
  if (Number.isNaN(instant.getTime())) return iso
  const seconds = Math.round((instant.getTime() - now.getTime()) / 1000)
  const formatter = new Intl.RelativeTimeFormat(currentLanguage(), { numeric: 'auto' })
  const [unit, size] = RELATIVE_UNITS.find(([, unitSize]) => Math.abs(seconds) >= unitSize) ?? [
    'second',
    1,
  ]
  return formatter.format(Math.round(seconds / size), unit)
}
