import type { ConfigSetting } from '@/api/types'

/** Listes éditables, dans l'ordre d'affichage (celui de config.ListNames côté Go). */
export const CONFIG_LISTS = [
  'radarr',
  'sonarr',
  'torrentClients',
  'linkDirs',
  'trackers',
  'torznab',
  'fsProviders',
] as const

// Copie de secretFieldsByList (internal/config/sealedfields.go) : ces champs ne reviennent jamais du serveur.
const SECRET_FIELDS: Readonly<Record<string, readonly string[]>> = {
  radarr: ['apiKey', 'password', 'url'],
  sonarr: ['apiKey', 'password', 'url'],
  torrentClients: ['password', 'url'],
  trackers: ['announce'],
  torznab: ['url'],
  fsProviders: ['rc', 'password', 'identityPassphrase'],
}

// Champs secrets à plusieurs valeurs (le serveur lit une liste) ; les autres secrets d'élément sont du texte.
const SECRET_LINE_FIELDS: Readonly<Record<string, readonly string[]>> = { trackers: ['announce'] }

/** Réglages secrets dont la valeur est un objet JSON (les autres sont du texte). */
const SECRET_JSON_SETTINGS = ['crossSeed']

/** Clé de l'aide sur l'identifiant d'élément, par liste : `config.idHint.<convention>`. */
const ID_CONVENTIONS: Readonly<Record<string, string>> = {
  radarr: 'url',
  sonarr: 'url',
  torznab: 'url',
  torrentClients: 'url',
  linkDirs: 'path',
  trackers: 'key',
  fsProviders: 'name',
}

/** Champ qui porte un élément entier en JSON, quand aucun élément de la liste ne sert de modèle. */
export const WHOLE_ELEMENT_FIELD = '*'

type FieldKind = 'text' | 'number' | 'boolean' | 'lines' | 'json'

export interface FormField {
  name: string
  kind: FieldKind
  secret: boolean
  /** Pour un secret : une valeur existe déjà en base (elle n'est jamais rendue). */
  isSet: boolean
  text: string
  checked: boolean
}

export type FormValues =
  { valid: true; values: Record<string, unknown> } | { valid: false; invalidField: string }

export const isSecretField = (list: string, name: string) =>
  SECRET_FIELDS[list]?.includes(name) ?? false

export const idHintKey = (list: string) => `config.idHint.${ID_CONVENTIONS[list] ?? 'name'}`

function kindOf(value: unknown): FieldKind {
  if (typeof value === 'string') return 'text'
  if (typeof value === 'number') return 'number'
  if (typeof value === 'boolean') return 'boolean'
  const isStringArray = Array.isArray(value) && value.every((entry) => typeof entry === 'string')
  return isStringArray ? 'lines' : 'json'
}

function textOf(kind: FieldKind, value: unknown): string {
  if (value === null || value === undefined) return ''
  if (kind === 'text') return String(value)
  if (kind === 'number') return String(value)
  if (kind === 'lines') return (value as string[]).join('\n')
  return JSON.stringify(value, null, 2)
}

function fieldOf(
  name: string,
  value: unknown,
  secret: boolean,
  kindWhenSecret: FieldKind,
  isSet: boolean,
): FormField {
  const kind = secret ? kindWhenSecret : kindOf(value)
  const text = secret ? '' : textOf(kind, value)
  return { name, kind, secret, isSet: secret && isSet, text, checked: value === true }
}

/** elementFields prépare le formulaire d'un élément ; `blank` vide les valeurs (modèle pour un ajout). */
export function elementFields(
  list: string,
  element: Record<string, unknown>,
  blank = false,
): FormField[] {
  return Object.entries(element).map(([name, value]) => {
    const kindWhenSecret = SECRET_LINE_FIELDS[list]?.includes(name) ? 'lines' : 'text'
    const field = fieldOf(name, value, isSecretField(list, name), kindWhenSecret, !blank)
    return blank ? { ...field, text: '', checked: false } : field
  })
}

/** addFields : les champs du premier élément de la liste, sinon l'élément entier en JSON. */
export function addFields(list: string, firstElement?: Record<string, unknown>): FormField[] {
  if (firstElement) return elementFields(list, firstElement, true)
  return [
    {
      name: WHOLE_ELEMENT_FIELD,
      kind: 'json',
      secret: false,
      isSet: false,
      text: '{}',
      checked: false,
    },
  ]
}

export function settingField(key: string, setting: ConfigSetting): FormField {
  const kindWhenSecret = SECRET_JSON_SETTINGS.includes(key) ? 'json' : 'text'
  return fieldOf(key, setting.value, setting.secret, kindWhenSecret, setting.set)
}

function parsedValue(field: FormField): unknown {
  if (field.kind === 'boolean') return field.checked
  if (field.kind === 'text') return field.text
  if (field.kind === 'lines')
    return field.text
      .split('\n')
      .map((line) => line.trim())
      .filter((line) => line !== '')
  if (field.kind === 'number') {
    const number = Number(field.text)
    if (Number.isNaN(number)) throw new SyntaxError(field.name)
    return number
  }
  return JSON.parse(field.text)
}

// Un secret laissé vide reste celui de la base : le champ n'est pas envoyé, ni vide ni masqué.
function isOmitted(field: FormField): boolean {
  const isBlank = field.text.trim() === ''
  if (field.secret) return isBlank
  return isBlank && (field.kind === 'number' || field.kind === 'json')
}

/** formValues lit le formulaire ; un champ illisible (nombre, JSON) est nommé au lieu d'être envoyé. */
export function formValues(fields: readonly FormField[]): FormValues {
  const values: Record<string, unknown> = {}
  for (const field of fields) {
    if (isOmitted(field)) continue
    try {
      values[field.name] = parsedValue(field)
    } catch {
      return { valid: false, invalidField: field.name }
    }
  }
  return { valid: true, values }
}

/** elementOf rend l'élément à envoyer : l'objet JSON entier s'il a été saisi ainsi. */
export function elementOf(values: Record<string, unknown>): Record<string, unknown> | undefined {
  if (!(WHOLE_ELEMENT_FIELD in values)) return values
  const whole = values[WHOLE_ELEMENT_FIELD]
  const isObject = typeof whole === 'object' && whole !== null && !Array.isArray(whole)
  return isObject ? (whole as Record<string, unknown>) : undefined
}

/** displayValue résume une valeur pour le tableau. */
export function displayValue(value: unknown): string {
  if (typeof value === 'string') return value
  return JSON.stringify(value) ?? ''
}
