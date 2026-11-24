// @vitest-environment node
import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { ESLint } from 'eslint'
import { describe, expect, it } from 'vitest'

const webRoot = fileURLToPath(new URL('..', import.meta.url))
const RULE = '@intlify/vue-i18n/no-raw-text'
const linter = new ESLint({ cwd: webRoot })

async function rawTextMessages(template: string, filePath = 'src/components/Brand.vue') {
  const [result] = await linter.lintText(`<template>${template}</template>`, { filePath })
  return result!.messages.filter((message) => message.ruleId === RULE)
}

function vueFilesUnder(directory: string): string[] {
  return readdirSync(`${webRoot}${directory}`, { withFileTypes: true }).flatMap((entry) => {
    const path = `${directory}/${entry.name}`
    if (entry.isDirectory()) return vueFilesUnder(path)
    return entry.name.endsWith('.vue') ? [path] : []
  })
}

const appVueFiles = vueFilesUnder('src').filter((path) => !path.startsWith('src/components/ui/'))

describe('raw-text lint applies to every file', () => {
  it('has no exception list left', () => {
    expect(existsSync(`${webRoot}lint-exceptions.json`)).toBe(false)
  })

  it.each(appVueFiles)('%s has the rule enabled', async (file) => {
    const config = await linter.calculateConfigForFile(file)

    const gotSeverity = config.rules?.[RULE]?.[0]

    expect(gotSeverity).toBe(2)
  })

  it.each(['src/views/Brand.vue', 'src/views/LinksView.vue', 'src/components/RecordedLinks.vue'])(
    'fails on a raw text node in %s',
    async (file) => {
      expect((await rawTextMessages('<p>Hello</p>', file)).length).toBeGreaterThan(0)
    },
  )

  it.each(['placeholder', 'aria-label', 'title', 'alt'])(
    'fails on a raw %s attribute',
    async (attribute) => {
      expect((await rawTextMessages(`<input ${attribute}="Hello" />`)).length).toBeGreaterThan(0)
    },
  )

  it('fails on raw ConfirmDialog description and confirm-label props', async () => {
    const messages = await rawTextMessages(
      '<ConfirmDialog description="Supprimer ?" confirm-label="Oui" />',
    )

    expect(messages.length).toBeGreaterThanOrEqual(2)
  })

  it('passes on a bound ConfirmDialog prop', async () => {
    expect(await rawTextMessages('<ConfirmDialog :description="t(\'nav.label\')" />')).toHaveLength(
      0,
    )
  })

  it('passes on a translated text', async () => {
    expect(await rawTextMessages('<p>{{ t("nav.label") }}</p>')).toHaveLength(0)
  })
})

const SCRIPT_FILES = vueFilesUnder('src')
  .concat(tsFilesUnder('src'))
  .filter((path) => !path.startsWith('src/components/ui/') && !path.startsWith('src/test/'))

function tsFilesUnder(directory: string): string[] {
  return readdirSync(`${webRoot}${directory}`, { withFileTypes: true }).flatMap((entry) => {
    const path = `${directory}/${entry.name}`
    if (entry.isDirectory()) return tsFilesUnder(path)
    return entry.name.endsWith('.ts') && !entry.name.endsWith('.spec.ts') ? [path] : []
  })
}

const IGNORE_MARKER = 'i18n-ignore:'
const NOTIFICATION_CALL =
  /\b(?:toast(?:\.(?:success|error|warning|info|message))?|alert|confirm)\s*\(/g
const LITERAL_START = /^\s*['"`]/
const OBJECT_LITERAL_TEXT = /\b(?:title|description)\s*:\s*['"`]/
const OBJECT_LOOKAHEAD = 160

/** Lignes (1-based) où une notification reçoit un texte en dur : littéral direct ou `title:`/`description:`. */
export function literalNotificationLines(source: string): number[] {
  const lines = source.split('\n')
  const hits = new Set<number>()
  for (const match of source.matchAll(NOTIFICATION_CALL)) {
    const argsStart = match.index + match[0].length
    const lineNumber = source.slice(0, match.index).split('\n').length
    const args = source.slice(argsStart, argsStart + OBJECT_LOOKAHEAD)
    const isLiteral =
      LITERAL_START.test(args) || OBJECT_LITERAL_TEXT.test(args.split(/\)\s*(?:\n|;|$)/)[0] ?? '')
    if (isLiteral && !lines[lineNumber - 1]!.includes(IGNORE_MARKER)) hits.add(lineNumber)
  }
  return [...hits]
}

describe('user-facing string literals in scripts', () => {
  it.each(SCRIPT_FILES)('%s passes a translated text to every notification', (file) => {
    const source = readFileSync(`${webRoot}${file}`, 'utf8')

    expect(literalNotificationLines(source)).toEqual([])
  })

  it.each([
    "toast.success('Fait.')",
    'toast.error("Echec")',
    'toast(`Fait`)',
    "alert('Attention')",
    "confirm('Sûr ?')",
    "toast.error({ title: 'Echec' })",
    "toast.success(t('a.b'), { description: 'Detail' })",
  ])('detects the planted literal %s', (planted) => {
    expect(literalNotificationLines(planted)).toEqual([1])
  })

  it('accepts a translated text and an ignore marker with its reason', () => {
    expect(literalNotificationLines("toast.success(t('jobs.done'))")).toEqual([])
    expect(literalNotificationLines("toast.info('ok') // i18n-ignore: technical")).toEqual([])
  })
})
