import { afterEach, describe, expect, it } from 'vitest'
import en from '@/locales/en.json'
import fr from '@/locales/fr.json'
import { readFileSync } from 'node:fs'
import { ROUTES } from '@/router'
import { codeLabel, initialLanguage, setLanguage } from '@/i18n'

type Tree = { [key: string]: string | Tree }

/** flatten aplatit un arbre de messages en `chemin.de.la.clé` -> texte. */
function flatten(tree: Tree, prefix = ''): Record<string, string> {
  const flat: Record<string, string> = {}
  for (const [key, value] of Object.entries(tree)) {
    const path = prefix ? `${prefix}.${key}` : key
    if (typeof value === 'string') flat[path] = value
    else Object.assign(flat, flatten(value, path))
  }
  return flat
}

function paramsOf(message: string): string[] {
  return [...message.matchAll(/\{(\w+)\}/g)].map((match) => match[1] ?? '').sort()
}

describe('locales', () => {
  afterEach(() => setLanguage('fr'))

  it('has the same keys and params in fr and en', () => {
    const frenchMessages = flatten(fr as Tree)
    const englishMessages = flatten(en as Tree)

    expect(Object.keys(englishMessages).sort()).toEqual(Object.keys(frenchMessages).sort())
    for (const [key, frenchMessage] of Object.entries(frenchMessages)) {
      expect(paramsOf(englishMessages[key] ?? ''), key).toEqual(paramsOf(frenchMessage))
    }
  })

  it('shows the raw code for an unknown code', () => {
    expect(codeLabel('kind', 'brand_new_kind')).toBe('brand_new_kind')
    expect(codeLabel('doctor', '')).toBe('')
  })

  it('translates a known code in the active language', () => {
    expect(codeLabel('decision', 'applied')).toBe('Appliquée')

    setLanguage('en')

    expect(codeLabel('decision', 'applied')).toBe('Applied')
  })

  it('picks fr then en from navigator languages and defaults to fr', () => {
    expect(initialLanguage(['de-DE', 'fr-CA', 'en-US'])).toBe('fr')
    expect(initialLanguage(['de-DE', 'en-GB', 'fr'])).toBe('en')
    expect(initialLanguage(['de-DE', 'ja'])).toBe('fr')
    expect(initialLanguage([])).toBe('fr')
  })

  it('labels every doctor code of the Go catalogue in both languages', () => {
    const goSource = readFileSync('../internal/doctor/codes.go', 'utf8')
    const goCodes = [...goSource.matchAll(/^\s*Code\w+\s*=\s*"([a-z_]+)"/gm)].map(
      (match) => match[1] ?? '',
    )
    const doctorKeys = {
      fr: Object.keys((fr as Tree & { codes: { doctor: Tree } }).codes.doctor),
      en: Object.keys((en as Tree & { codes: { doctor: Tree } }).codes.doctor),
    }

    expect(goCodes.length).toBeGreaterThan(50)
    for (const language of ['fr', 'en'] as const) {
      expect(
        goCodes.filter((code) => !doctorKeys[language].includes(code)),
        language,
      ).toEqual([])
    }
  })

  it('has a nav label for every route in both languages', () => {
    for (const messages of [flatten(fr as Tree), flatten(en as Tree)]) {
      for (const route of ROUTES) expect(messages[`nav.${route.navKey}`], route.navKey).toBeTruthy()
    }
  })
})
