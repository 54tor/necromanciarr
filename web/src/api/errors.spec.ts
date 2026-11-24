import { describe, expect, it } from 'vitest'
import { ApiError, messageForCode, messageForError } from './errors'

describe('Errors', () => {
  it('Errors_mapsKnownCodesToFrenchMessages', () => {
    const expectedCodes = [
      'usage',
      'confirmation_required',
      'unauthorized',
      'forbidden',
      'not_found',
      'method_not_allowed',
      'stale_proposal',
      'not_configured',
      'stage_running',
      'inspection_busy',
      'refused',
      'shutting_down',
      'failure',
      'network',
    ]

    for (const code of expectedCodes) {
      const gotMessage = messageForCode(code)
      expect(gotMessage, code).not.toContain('Une erreur est survenue')
      expect(gotMessage, code).toMatch(/[a-zé]/i)
    }
    expect(messageForCode('stage_running')).toBe('Une étape est déjà en cours.')
    expect(messageForCode('inspection_busy')).toContain('examen des liens')
  })

  it('Errors_neverShowsServerTextForAnUnknownCode', () => {
    const serverError = new ApiError(500, {
      code: 'brand_new',
      message: '/home/u/secret/path exploded',
    })

    const gotMessage = messageForError(serverError)

    expect(gotMessage).toContain('brand_new')
    expect(gotMessage).not.toContain('/home/u')
    expect(messageForError(new Error('boom'))).not.toContain('boom')
  })
})
