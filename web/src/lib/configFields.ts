import type { ConfigSetting } from '@/api/types'
import {
  ELEMENT_SCHEMAS,
  SETTING_SCHEMAS,
  type FieldKind,
  type FieldSpec,
} from '@/lib/configSchema'

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

const NANOSECONDS_PER_MILLISECOND = 1e6
const NANOSECONDS_PER_SECOND = 1e9
const NANOSECONDS_PER_MINUTE = 60 * NANOSECONDS_PER_SECOND
const NANOSECONDS_PER_HOUR = 60 * NANOSECONDS_PER_MINUTE
const DURATION_UNITS: readonly (readonly [string, number])[] = [
  ['h', NANOSECONDS_PER_HOUR],
  ['m', NANOSECONDS_PER_MINUTE],
  ['s', NANOSECONDS_PER_SECOND],
  ['ms', NANOSECONDS_PER_MILLISECOND],
]
const NUMBER_SEPARATORS = /[\s,]+/

export interface FormField {
  name: string
  kind: FieldKind
  secret: boolean
  /** Pour un secret : une valeur existe déjà en base (elle n'est jamais rendue). */
  isSet: boolean
  text: string
  checked: boolean
  selection: string[]
  options: readonly string[]
  spec: FieldSpec
  children: FormField[]
  rows: FormField[][]
}

export type FormValues =
  { valid: true; values: Record<string, unknown> } | { valid: false; invalidField: string }

export const isSecretField = (list: string, name: string) =>
  ELEMENT_SCHEMAS[list]?.some((spec) => spec.name === name && spec.secret === true) ?? false

export const idHintKey = (list: string) => `config.idHint.${ID_CONVENTIONS[list] ?? 'name'}`

const URL_AUTHORITY = /^([A-Za-z][A-Za-z0-9+.-]*):\/\/([^/?#]*)/

/**
 * endpointId rend l'identifiant que le serveur tire d'une URL (ElementID) : schéma en minuscules, `://`, hôte et port
 * tels que saisis, sans identifiants, chemin ni requête ; vide si l'URL n'a pas d'hôte.
 */
function endpointId(rawUrl: string): string {
  const parts = URL_AUTHORITY.exec(rawUrl.trim())
  if (!parts) return ''
  const [, scheme = '', authority = ''] = parts
  const host = authority.slice(authority.lastIndexOf('@') + 1)
  return host === '' ? '' : `${scheme.toLowerCase()}://${host}`
}

/** itemIdDeriver rend, pour une liste identifiée par l'URL, la déduction de l'identifiant depuis le champ `url`. */
export function itemIdDeriver(
  list: string,
): ((fields: readonly FormField[]) => string) | undefined {
  if (ID_CONVENTIONS[list] !== 'url') return undefined
  return (fields) => endpointId(fields.find((field) => field.name === 'url')?.text ?? '')
}

/** nsToDuration écrit une durée Go (`10m`) à partir des nanosecondes que rend la vue. */
export function nsToDuration(nanoseconds: number): string {
  if (!Number.isFinite(nanoseconds) || nanoseconds <= 0) return ''
  const unit = DURATION_UNITS.find(([, size]) => nanoseconds % size === 0)
  return unit ? `${nanoseconds / unit[1]}${unit[0]}` : `${nanoseconds}ns`
}

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value)

const asArray = (value: unknown): unknown[] => (Array.isArray(value) ? value : [])

function textOf(spec: FieldSpec, value: unknown): string {
  if (value === null || value === undefined) return ''
  if (spec.kind === 'lines') return asArray(value).join('\n')
  if (spec.kind === 'numbers') return asArray(value).join(', ')
  if (spec.kind === 'text' || spec.kind === 'select' || spec.kind === 'number') return String(value)
  return ''
}

function buildField(spec: FieldSpec, value: unknown, isSet: boolean): FormField {
  const isSecret = spec.secret === true
  const record = isRecord(value) ? value : {}
  return {
    name: spec.name,
    kind: spec.kind,
    secret: isSecret,
    isSet: isSecret && isSet,
    text: isSecret ? '' : textOf(spec, value),
    checked: value === true,
    selection: spec.kind === 'checks' ? asArray(value).map(String) : [],
    options: spec.options ?? [],
    spec,
    children: spec.kind === 'group' ? (spec.fields ?? []).map((sub) => buildSub(sub, record)) : [],
    rows: spec.kind === 'table' ? asArray(value).map((row) => buildRow(spec, row)) : [],
  }
}

const buildSub = (spec: FieldSpec, record: Record<string, unknown>): FormField =>
  buildField(spec, record[spec.name], false)

function buildRow(table: FieldSpec, row: unknown): FormField[] {
  const record = isRecord(row) ? row : {}
  return (table.fields ?? []).map((column) => buildSub(column, record))
}

/** newRow rend une ligne vide d'un champ tableau. */
export const newRow = (table: FormField): FormField[] => buildRow(table.spec, undefined)

/** elementFields prépare le formulaire d'un élément ; sans élément, les champs sont vides (ajout). */
export function elementFields(list: string, element?: Record<string, unknown>): FormField[] {
  return (ELEMENT_SCHEMAS[list] ?? []).map((spec) => {
    const isPresent = element !== undefined && spec.name in element
    return buildField(spec, element?.[spec.name], isPresent)
  })
}

export const addFields = (list: string): FormField[] => elementFields(list)

function settingValue(key: string, setting: ConfigSetting): unknown {
  const value = setting.value
  if (key !== 'dedupe' || !isRecord(value)) return value
  const minFileAge = typeof value.minFileAge === 'number' ? nsToDuration(value.minFileAge) : ''
  return { ...value, minFileAge }
}

export function settingField(key: string, setting: ConfigSetting): FormField {
  const spec = SETTING_SCHEMAS[key] ?? { name: key, kind: 'text' }
  return buildField(spec, settingValue(key, setting), setting.set)
}

/** isVisible : un champ réservé à certains types n'apparaît que pour eux. */
export function isVisible(field: FormField, siblings: readonly FormField[]): boolean {
  const condition = field.spec.showWhen
  if (!condition) return true
  const controller = siblings.find((sibling) => sibling.name === condition.field)
  return controller !== undefined && condition.values.includes(controller.text)
}

const isBlank = (field: FormField) => field.text.trim() === ''

function readNumbers(field: FormField): number[] {
  return field.text
    .split(NUMBER_SEPARATORS)
    .filter((entry) => entry !== '')
    .map((entry) => {
      const number = Number(entry)
      if (Number.isNaN(number)) throw new SyntaxError(field.name)
      return number
    })
}

function readLines(field: FormField): string[] {
  return field.text
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => line !== '')
}

function readNumber(field: FormField): number {
  const number = Number(field.text)
  if (Number.isNaN(number)) throw new SyntaxError(field.name)
  return number
}

function readField(field: FormField): unknown {
  switch (field.kind) {
    case 'boolean':
      return field.checked
    case 'checks':
      return [...field.selection]
    case 'lines':
      return readLines(field)
    case 'numbers':
      return readNumbers(field)
    case 'number':
      return readNumber(field)
    case 'group':
      return readFields(field.children, true)
    case 'table':
      return field.rows
        .map((row) => readFields(row, true))
        .filter((row) => Object.keys(row).length > 0)
    default:
      return field.text
  }
}

// Un secret laissé vide reste celui de la base : le champ n'est pas envoyé, ni vide ni masqué.
function isOmitted(field: FormField, omitBlank: boolean): boolean {
  if (field.secret) return isBlank(field)
  const isEmptyScalar = ['number', 'select'].includes(field.kind) && isBlank(field)
  if (isEmptyScalar) return true
  const isTextual = ['text', 'lines', 'numbers'].includes(field.kind)
  return omitBlank && isTextual && isBlank(field)
}

function readFields(fields: readonly FormField[], omitBlank: boolean): Record<string, unknown> {
  const values: Record<string, unknown> = {}
  for (const field of fields) {
    if (!isVisible(field, fields) || isOmitted(field, omitBlank)) continue
    values[field.name] = readField(field)
  }
  return values
}

/** formValues lit le formulaire ; un champ illisible (nombre) est nommé au lieu d'être envoyé. */
export function formValues(fields: readonly FormField[], omitBlank = true): FormValues {
  try {
    return { valid: true, values: readFields(fields, omitBlank) }
  } catch (error) {
    const invalidField = error instanceof SyntaxError ? error.message : ''
    return { valid: false, invalidField }
  }
}

/** displayValue résume une valeur pour le tableau. */
export function displayValue(value: unknown): string {
  if (typeof value === 'string') return value
  return JSON.stringify(value) ?? ''
}
