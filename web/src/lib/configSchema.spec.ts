import { describe, expect, it } from 'vitest'
import en from '@/locales/en.json'
import fr from '@/locales/fr.json'
import { addFields, itemIdDeriver, nsToDuration } from '@/lib/configFields'
import backendSecrets from '@/lib/backendSecrets.json'
import { ELEMENT_SCHEMAS, SETTING_SCHEMAS, schemaFieldNames } from '@/lib/configSchema'

// Fixture vérifiée par TestBackendSecretsFixture (internal/config) : un écart ferait afficher un secret en clair.
const BACKEND = backendSecrets as {
  elements: Record<string, string[]>
  settings: string[]
  settingKeys: string[]
  lists: string[]
}

describe('schéma de configuration', () => {
  it('traduit chaque champ en français et en anglais', () => {
    const missing = schemaFieldNames().filter((name) => {
      const key = name as keyof typeof fr.config.field
      return !(key in fr.config.field) || !(key in en.config.field)
    })

    expect(missing).toEqual([])
  })

  it("traduit l'aide du nom d'un fournisseur, qui n'accepte que minuscules, chiffres et tirets", () => {
    const name = ELEMENT_SCHEMAS.fsProviders!.find((spec) => spec.name === 'name')

    expect(name?.helpKey).toBe('fsProviderName')
    expect(fr.config.help.fsProviderName).toContain('Minuscules')
    expect(en.config.help.fsProviderName).toContain('Lower-case')
  })

  it("donne à l'URL une aide propre aux services et une autre aux indexeurs", () => {
    const urlHelpOf = (list: string) =>
      ELEMENT_SCHEMAS[list]!.find((spec) => spec.name === 'url')?.helpKey
    const valeurObtenue = ['radarr', 'sonarr', 'torrentClients', 'torznab'].map(urlHelpOf)

    expect(valeurObtenue).toEqual(['serviceUrl', 'serviceUrl', 'serviceUrl', 'indexerUrl'])
    expect(fr.config.help.serviceUrl).toContain('/proxy/')
    expect(en.config.help.serviceUrl).toContain('/proxy/')
    expect(fr.config.help.indexerUrl).toContain('sans requête')
    expect(en.config.help.indexerUrl).toContain('without any query')
    expect(en.config.help.indexerUrl).not.toContain('proxyKey')
  })

  it('couvre chaque liste du serveur, sans liste inconnue', () => {
    expect(Object.keys(ELEMENT_SCHEMAS).sort()).toEqual([...BACKEND.lists].sort())
  })

  it('couvre chaque réglage du serveur, sans réglage inconnu', () => {
    expect(Object.keys(SETTING_SCHEMAS).sort()).toEqual(BACKEND.settingKeys)
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
      elements: { ...BACKEND.elements, linkDirs: [] },
      settings: BACKEND.settings,
    })
  })
})

describe('réglages du démon cross-seed', () => {
  it('sépare une adresse en clair et une clé masquée, sans l’ancien réglage unique', () => {
    const gotValue = {
      url: SETTING_SCHEMAS.crossSeedUrl?.secret,
      apiKey: SETTING_SCHEMAS.crossSeedApiKey?.secret,
      legacy: 'crossSeed' in SETTING_SCHEMAS,
    }

    expect(gotValue).toEqual({ url: false, apiKey: true, legacy: false })
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
