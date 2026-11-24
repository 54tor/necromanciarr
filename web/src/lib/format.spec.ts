import { describe, expect, it } from 'vitest'
import { formatBytes, formatPercent } from './format'

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
})
