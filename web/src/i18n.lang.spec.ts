import { afterEach, describe, expect, it } from 'vitest'
import { setLanguage, t } from './i18n'

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

  it.each([
    ['fr', 1, '1 avertissement'],
    ['fr', 3, '3 avertissements'],
    ['en', 1, '1 warning'],
    ['en', 3, '3 warnings'],
  ] as const)('pluralises warningsCount in %s for %i', (language, count, valueExpected) => {
    setLanguage(language)

    const valueObtained = t('dedupe.warningsCount', { count }, count)

    expect(valueObtained).toBe(valueExpected)
  })
})
