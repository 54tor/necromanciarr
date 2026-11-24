// @vitest-environment node
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { ESLint } from 'eslint'
import { describe, expect, it } from 'vitest'

const webRoot = fileURLToPath(new URL('..', import.meta.url))
const exceptions = JSON.parse(readFileSync(`${webRoot}lint-exceptions.json`, 'utf8')) as {
  owner: string
  since: string
  files: string[]
}

// Règle réactivée en dernier : si le fichier n'a plus de texte en dur, il n'a plus besoin de l'exception.
const strictLinter = new ESLint({
  cwd: webRoot,
  overrideConfig: { files: exceptions.files, rules: { '@intlify/vue-i18n/no-raw-text': 'error' } },
})

describe('raw-text lint exceptions', () => {
  it('are owned by D3d with a start date', () => {
    expect(exceptions.owner).toBe('D3d')
    expect(exceptions.since).toMatch(/^\d{4}-\d{2}-\d{2}$/)
  })

  it.each(exceptions.files)('%s still contains raw text', async (file) => {
    const [result] = await strictLinter.lintFiles([file])

    const rawTextErrors = result!.messages.filter(
      (message) => message.ruleId === '@intlify/vue-i18n/no-raw-text',
    )
    expect(rawTextErrors.length).toBeGreaterThan(0)
  })
})

describe('raw-text lint on an unlisted file', () => {
  const linter = new ESLint({ cwd: webRoot })

  async function rawTextMessages(template: string) {
    const [result] = await linter.lintText(`<template>${template}</template>`, {
      filePath: 'src/components/Unlisted.vue',
    })
    return result!.messages.filter((message) => message.ruleId === '@intlify/vue-i18n/no-raw-text')
  }

  it('fails on a raw text node', async () => {
    expect((await rawTextMessages('<p>Hello</p>')).length).toBeGreaterThan(0)
  })

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
