// @vitest-environment node
import { existsSync, readdirSync } from 'node:fs'
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
