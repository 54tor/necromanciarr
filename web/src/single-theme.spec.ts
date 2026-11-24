// @vitest-environment node
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const sourceRoot = fileURLToPath(new URL('.', import.meta.url))
const builtAssets = fileURLToPath(new URL('../../internal/web/ui/dist/assets', import.meta.url))
// Motifs assemblés pour que ce fichier ne se détecte pas lui-même.
const darkUtility = 'dar' + 'k:'
const darkClassSelector = '.dar' + 'k'

function listFiles(directory: string): string[] {
  return readdirSync(directory).flatMap((entry) => {
    const path = join(directory, entry)
    return statSync(path).isDirectory() ? listFiles(path) : [path]
  })
}

describe('single dark theme', () => {
  const sourceFiles = listFiles(sourceRoot).filter((path) => !path.endsWith('single-theme.spec.ts'))

  it.each(sourceFiles.map((path) => [path.slice(sourceRoot.length), path]))(
    '%s has no dark variant',
    (_name, path) => {
      const content = readFileSync(path as string, 'utf8')

      expect(content).not.toContain(darkUtility)
      expect(content).not.toContain(darkClassSelector)
    },
  )

  const builtCss = existsSync(builtAssets)
    ? readdirSync(builtAssets).filter((name) => name.endsWith('.css'))
    : []

  // Ne s'applique qu'après « make ui » ; ui-lint et ui-test tournent après la construction.
  it.skipIf(builtCss.length === 0)('built CSS has no prefers-color-scheme', () => {
    for (const name of builtCss) {
      const content = readFileSync(join(builtAssets, name), 'utf8')

      expect(content).not.toContain('prefers-color-scheme')
    }
  })

  // vue-sonner livre ses styles dans un fichier à part : sans l'import, les toasts s'afficheraient sans mise en forme.
  it.skipIf(builtCss.length === 0)('built CSS carries the toast styles', () => {
    const allCss = builtCss.map((name) => readFileSync(join(builtAssets, name), 'utf8')).join('')

    expect(allCss).toContain('data-sonner-toaster')
  })
})
