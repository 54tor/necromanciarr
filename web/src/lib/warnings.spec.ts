import { describe, expect, it } from 'vitest'
import { uniqueWarnings, warningText } from './warnings'

describe('warningText', () => {
  it('renders the text of a structured warning', () => {
    expect(warningText({ code: 'warning.sources_changed', text: 'sources changed' })).toBe(
      'sources changed',
    )
  })
  it('renders a legacy string as it is', () => {
    expect(warningText('old text')).toBe('old text')
  })
})

describe('uniqueWarnings', () => {
  it('removes duplicates by text, whatever the shape', () => {
    const merged = uniqueWarnings([
      { code: 'warning.no_client_recheck', text: 'no recheck' },
      'no recheck',
      { code: 'legacy.text', text: 'other' },
    ])
    expect(merged.map(warningText)).toEqual(['no recheck', 'other'])
  })
})
