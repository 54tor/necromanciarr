import { describe, expect, it } from 'vitest'
import en from '@/locales/en.json'
import fr from '@/locales/fr.json'
import { addFields, itemIdDeriver, nsToDuration } from '@/lib/configFields'
import { ELEMENT_SCHEMAS, SETTING_SCHEMAS, schemaFieldNames } from '@/lib/configSchema'

// Même contenu que secretFieldsByList (internal/config/sealedfields.go) : un écart ferait afficher un secret en clair.
const SECRET_FIELDS_OF_THE_BACKEND: Record<string, string[]> = {
  radarr: ['apiKey', 'password', 'url'],
  sonarr: ['apiKey', 'password', 'url'],
  torrentClients: ['password', 'url'],
  trackers: ['announce'],
  torznab: ['url'],
  fsProviders: ['identityPassphrase', 'password', 'rc'],
}
const SECRET_SETTINGS_OF_THE_BACKEND = ['crossSeed', 'libraryApi', 'prowlarr']
const SETTING_KEYS_OF_THE_BACKEND = [
  'crossSeed',
  'crossSeedMode',
  'dedupe',
  'language',
  'libraryApi',
  'libraryRoots',
  'linkCategory',
  'linkType',
  'listen',
  'partialMaxMissingPercent',
  'pathMappings',
  'prowlarr',
  'readBytesPerSecond',
  'seedTags',
  'skipRecheck',
  'sourceOrder',
  'tieBreakReadBytes',
]

describe('schéma de configuration', () => {
  it('traduit chaque champ en français et en anglais', () => {
    const missing = schemaFieldNames().filter((name) => {
      const key = name as keyof typeof fr.config.field
      return !(key in fr.config.field) || !(key in en.config.field)
    })

    expect(missing).toEqual([])
  })

  it('couvre chaque réglage du serveur, sans réglage inconnu', () => {
    expect(Object.keys(SETTING_SCHEMAS).sort()).toEqual(SETTING_KEYS_OF_THE_BACKEND)
  })

  it('marque secrets exactement les champs scellés par le serveur', () => {
    const secretsOfElements = Object.fromEntries(
      Object.entries(ELEMENT_SCHEMAS).map(([list, specs]) => [
        list,
        specs
          .filter((spec) => spec.secret)
          .map((spec) => spec.name)
          .sort(),
      ]),
    )
    const secretSettings = Object.values(SETTING_SCHEMAS)
      .filter((spec) => spec.secret)
      .map((spec) => spec.name)
      .sort()

    expect({ elements: secretsOfElements, settings: secretSettings }).toEqual({
      elements: { ...SECRET_FIELDS_OF_THE_BACKEND, linkDirs: [] },
      settings: SECRET_SETTINGS_OF_THE_BACKEND,
    })
  })
})

describe('nsToDuration', () => {
  it.each([
    [600_000_000_000, '10m'],
    [7_200_000_000_000, '2h'],
    [30_000_000_000, '30s'],
    [500_000_000, '500ms'],
    [1_500_000_000, '1500ms'],
    [0, ''],
  ])('écrit %d ns en %s', (nanoseconds, valeurAttendue) => {
    expect(nsToDuration(nanoseconds)).toBe(valeurAttendue)
  })
})

describe('itemIdDeriver', () => {
  const derive = (rawUrl: string) => {
    const fields = addFields('radarr')
    fields.find((field) => field.name === 'url')!.text = rawUrl
    return itemIdDeriver('radarr')!(fields)
  }

  it.each([
    ['HTTP://Radarr.Example.com:80/x?apikey=k', 'http://radarr.example.com'],
    ['https://example.com:443', 'https://example.com'],
    ['https://example.com:8443/path', 'https://example.com:8443'],
    ['http://alice:pw@example.com:7878', 'http://example.com:7878'],
    ['http://[::1]:7878/a', 'http://[::1]:7878'],
    ['http://[::1]:80', 'http://[::1]'],
    ['pas une url', ''],
  ])('déduit l’identifiant de %s', (rawUrl, valeurAttendue) => {
    expect(derive(rawUrl)).toBe(valeurAttendue)
  })

  it("ne déduit rien pour une liste qui n'est pas identifiée par l'URL", () => {
    expect(itemIdDeriver('linkDirs')).toBeUndefined()
  })
})
