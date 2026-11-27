/** Types de champ du formulaire de configuration : un contrôle adapté par type de valeur. */
export type FieldKind =
  | 'text'
  | 'number'
  | 'boolean'
  | 'lines'
  | 'numbers'
  | 'select'
  | 'checks'
  | 'group'
  | 'table'
  | 'pairs'

export interface FieldSpec {
  name: string
  kind: FieldKind
  /** Champ scellé en base : jamais rendu par le serveur ni prérempli. */
  secret?: boolean
  options?: readonly string[]
  /** Sous-champs d'un groupe, ou colonnes d'un tableau. */
  fields?: readonly FieldSpec[]
  /** Clé de l'aide sous le champ, quand elle ne porte pas le nom du champ (`config.help.<clé>`). */
  helpKey?: string
  /** Tableau `pairs` : chaque ligne est écrite `colonne1<séparateur>colonne2` dans la liste de chaînes du serveur. */
  separator?: string
  /** Le champ ne s'applique qu'à certaines valeurs d'un champ frère (type d'un fournisseur). */
  showWhen?: { field: string; values: readonly string[] }
}

const LINK_KINDS = ['symlink', 'hardlink', 'reflink'] as const
const DEDUPE_VERIFY_MODES = ['auto', 'full', 'sampled', 'hash'] as const
const CROSS_SEED_MODES = ['auto', 'delegate', 'native', 'off'] as const
const LANGUAGES = ['fr', 'en'] as const
const SFTP_EXEC_MODES = ['auto', 'off', 'required'] as const

const text = (name: string, secret = false): FieldSpec => ({ name, kind: 'text', secret })
const number = (name: string): FieldSpec => ({ name, kind: 'number' })
const boolean = (name: string): FieldSpec => ({ name, kind: 'boolean' })
const lines = (name: string, secret = false): FieldSpec => ({ name, kind: 'lines', secret })
const select = (name: string, options: readonly string[]): FieldSpec => ({
  name,
  kind: 'select',
  options,
})

const withHelp = (spec: FieldSpec, helpKey: string): FieldSpec => ({ ...spec, helpKey })
const serviceUrl = withHelp(text('url'), 'serviceUrl')

const ARR_FIELDS: readonly FieldSpec[] = [
  serviceUrl,
  text('apiKey', true),
  text('user'),
  text('password', true),
  text('proxyKey', true),
]

const rcloneOnly = { field: 'type', values: ['rclone'] }
const sftpOnly = { field: 'type', values: ['sftp'] }
const onlyWhen = (spec: FieldSpec, showWhen: FieldSpec['showWhen']): FieldSpec => ({
  ...spec,
  showWhen,
})

/** Champs de chaque élément de liste (formes lues par les analyseurs côté Go). */
export const ELEMENT_SCHEMAS: Readonly<Record<string, readonly FieldSpec[]>> = {
  radarr: ARR_FIELDS,
  sonarr: ARR_FIELDS,
  torrentClients: [
    select('type', ['qbittorrent']),
    boolean('readOnly'),
    serviceUrl,
    text('user'),
    text('password', true),
    text('proxyKey', true),
  ],
  linkDirs: [text('path')],
  trackers: [text('key'), lines('announce', true), lines('aliases')],
  torznab: [
    text('name'),
    withHelp(text('url'), 'indexerUrl'),
    text('apiKey', true),
    text('tracker'),
    { name: 'categories', kind: 'numbers' },
    text('minInterval'),
    text('timeout'),
    number('maxQueriesPerRun'),
    number('maxDownloadsPerRun'),
    number('maxQueriesPerDay'),
    number('maxDownloadsPerDay'),
  ],
  fsProviders: [
    select('type', ['rclone', 'sftp']),
    { ...text('name'), helpKey: 'fsProviderName' },
    onlyWhen(
      {
        name: 'mounts',
        kind: 'pairs',
        separator: '=',
        fields: [text('localPath'), text('target')],
      },
      rcloneOnly,
    ),
    onlyWhen(text('rc', true), rcloneOnly),
    onlyWhen(text('binary'), rcloneOnly),
    text('timeout'),
    onlyWhen(text('address'), sftpOnly),
    onlyWhen(text('user'), sftpOnly),
    onlyWhen(text('identityFile'), sftpOnly),
    onlyWhen(text('knownHosts'), sftpOnly),
    onlyWhen(boolean('agent'), sftpOnly),
    onlyWhen(text('password', true), sftpOnly),
    onlyWhen(text('identityPassphrase', true), sftpOnly),
    onlyWhen(number('readBytesPerSecond'), sftpOnly),
    onlyWhen(number('maxConcurrentRequests'), sftpOnly),
    onlyWhen(text('keepalive'), sftpOnly),
    onlyWhen(select('exec', SFTP_EXEC_MODES), sftpOnly),
    onlyWhen(text('execHashTimeout'), sftpOnly),
  ],
}

/** Champ de chaque réglage ; la valeur du formulaire est celle du réglage entier. */
export const SETTING_SCHEMAS: Readonly<Record<string, FieldSpec>> = {
  listen: text('listen'),
  readBytesPerSecond: number('readBytesPerSecond'),
  tieBreakReadBytes: number('tieBreakReadBytes'),
  libraryApi: text('libraryApi', true),
  libraryToken: text('libraryToken', true),
  libraryRoots: lines('libraryRoots'),
  pathMappings: { name: 'pathMappings', kind: 'table', fields: [text('from'), text('to')] },
  prowlarr: text('prowlarr', true),
  sourceOrder: lines('sourceOrder'),
  dedupe: {
    name: 'dedupe',
    kind: 'group',
    fields: [
      lines('torrentRoots'),
      { name: 'kinds', kind: 'checks', options: LINK_KINDS },
      boolean('relativeSymlinks'),
      text('minFileAge'),
      boolean('recheckAfter'),
      select('verify', DEDUPE_VERIFY_MODES),
    ],
  },
  linkType: select('linkType', LINK_KINDS),
  skipRecheck: boolean('skipRecheck'),
  linkCategory: text('linkCategory'),
  crossSeedUrl: text('crossSeedUrl'),
  crossSeedApiKey: text('crossSeedApiKey', true),
  crossSeedMode: select('crossSeedMode', CROSS_SEED_MODES),
  seedTags: lines('seedTags'),
  partialMaxMissingPercent: number('partialMaxMissingPercent'),
  language: select('language', LANGUAGES),
}

/** Noms de champ de tous les schémas, pour contrôler que chacun a un libellé traduit. */
export function schemaFieldNames(): string[] {
  const names = new Set<string>()
  const collect = (spec: FieldSpec): void => {
    names.add(spec.name)
    spec.fields?.forEach(collect)
  }
  Object.values(ELEMENT_SCHEMAS).forEach((specs) => specs.forEach(collect))
  Object.values(SETTING_SCHEMAS).forEach(collect)
  return [...names].sort()
}
