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
