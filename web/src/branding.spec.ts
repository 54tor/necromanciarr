// @vitest-environment node
import { readdirSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

describe('branding files', () => {
  it('keeps every file under public/branding below 100 KB', () => {
    const directory = fileURLToPath(new URL('../public/branding', import.meta.url))

    const gotOversized = readdirSync(directory).filter(
      (name) => statSync(join(directory, name)).size >= 100 * 1024,
    )

    expect(gotOversized).toEqual([])
  })
})
