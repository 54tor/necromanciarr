import { afterEach, describe, expect, it } from 'vitest'
import { setLanguage } from '@/i18n'
import { ApiError, messageForCode, messageForError } from './errors'

describe('Errors', () => {
  afterEach(() => setLanguage('fr'))

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

  it('Errors_followTheActiveLanguage', () => {
    setLanguage('en')

    expect(messageForCode('stage_running')).toBe('A stage is already running.')
    expect(messageForCode('brand_new')).toContain('An error occurred')
    expect(messageForCode('brand_new')).toContain('brand_new')
    expect(messageForError(new Error('boom'))).toContain('An error occurred')
  })

  it('Errors_everyKnownCodeHasAMessageInBothLanguages', () => {
    const knownCodes = [
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
    for (const language of ['fr', 'en'] as const) {
      setLanguage(language)
      for (const code of knownCodes) {
        expect(messageForCode(code), `${language} ${code}`).not.toMatch(/\(code/)
      }
    }
  })
})
