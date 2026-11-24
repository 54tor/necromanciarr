import { currentLanguage } from '@/i18n'

const BYTE_UNITS = {
  fr: ['o', 'Kio', 'Mio', 'Gio', 'Tio'],
  en: ['B', 'KiB', 'MiB', 'GiB', 'TiB'],
} as const
const BYTES_PER_UNIT = 1024

/** formatBytes écrit une taille en octets avec une unité binaire de la langue active (1 536 -> « 1,5 Kio » / "1.5 KiB"). */
export function formatBytes(bytes: number): string {
  const language = currentLanguage()
  const units = BYTE_UNITS[language]
  let value = bytes
  let unitIndex = 0
  while (value >= BYTES_PER_UNIT && unitIndex < units.length - 1) {
    value /= BYTES_PER_UNIT
    unitIndex += 1
  }
  const digits = unitIndex === 0 || value >= 100 ? 0 : 1
  return `${value.toLocaleString(language, { maximumFractionDigits: digits })} ${units[unitIndex]}`
}

/** formatExactBytes écrit le nombre d'octets sans arrondi (« 1 000 000 o »), pour l'infobulle d'une taille humanisée. */
export function formatExactBytes(bytes: number): string {
  const language = currentLanguage()
  return `${bytes.toLocaleString(language)} ${BYTE_UNITS[language][0]}`
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
