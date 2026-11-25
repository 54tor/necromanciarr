import { afterEach, describe, expect, it } from 'vitest'
import { setLanguage } from './i18n'

describe('document language', () => {
  afterEach(() => setLanguage('fr'))

  it('setLanguage_updatesTheHtmlLangAttribute', () => {
    setLanguage('en')
    const gotEnglish = document.documentElement.lang
    setLanguage('fr')
    const gotFrench = document.documentElement.lang

    expect(gotEnglish).toBe('en')
    expect(gotFrench).toBe('fr')
  })
})
