import { describe, expect, it } from 'vitest'
import { warningText } from './warnings'

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
