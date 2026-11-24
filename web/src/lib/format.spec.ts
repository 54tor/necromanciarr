import { describe, expect, it } from 'vitest'
import { setLanguage } from '@/i18n'
import { formatBytes, formatDateTime, formatPercent, formatRelative } from './format'

describe('format', () => {
  it('formatBytes_humanisesWithBinaryUnits', () => {
    const cases: [number, string][] = [
      [0, '0 o'],
      [1023, '1 023 o'],
      [1536, '1,5 Kio'],
      [3_000_000_000, '2,8 Gio'],
    ]
    for (const [bytes, expectedValue] of cases) {
      const gotValue = formatBytes(bytes).replace(/\s/g, ' ')
      expect(gotValue).toBe(expectedValue)
    }
  })

  it('formatPercent_keepsOneDecimal', () => {
    expect(formatPercent(99.54).replace(/\s/g, ' ')).toBe('99,5 %')
  })

  it('formatDateTime_keepsUnreadableValuesAsTheyAre', () => {
    expect(formatDateTime('pas une date')).toBe('pas une date')
    expect(formatDateTime('2026-01-01T10:00:00Z')).toContain('2026')
  })

  it('formatRelative_followsTheActiveLanguage', () => {
    const now = new Date('2026-03-10T10:00:00Z')

    expect(formatRelative('2026-03-07T10:00:00Z', now)).toBe('il y a 3 jours')
    setLanguage('en')
    expect(formatRelative('2026-03-07T10:00:00Z', now)).toBe('3 days ago')
    expect(formatRelative('2026-03-10T09:30:00Z', now)).toBe('30 minutes ago')
    expect(formatRelative('not a date', now)).toBe('not a date')
    setLanguage('fr')
  })
})
