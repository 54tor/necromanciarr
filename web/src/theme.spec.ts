// @vitest-environment node
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const cssPath = fileURLToPath(new URL('./styles/globals.css', import.meta.url))
const css = readFileSync(cssPath, 'utf8')

function rootBlock(): string {
  const match = /:root\s*\{([^}]*)\}/.exec(css)
  return match?.[1] ?? ''
}

describe('theme', () => {
  const anthraciteTokens: Record<string, string> = {
    '--background': '#121416',
    '--card': '#1c1f22',
    '--secondary': '#2b3033',
    '--border': '#3a4044',
    '--foreground': '#e6e8ea',
    '--muted-foreground': '#9aa2a8',
    '--primary': '#4cc2a5',
    '--destructive': '#e5675d',
  }

  it.each(Object.entries(anthraciteTokens))('defines %s in :root as %s', (token, expectedValue) => {
    const gotValue = new RegExp(`${token}:\\s*([^;]+);`).exec(rootBlock())?.[1]?.trim()

    expect(gotValue).toBe(expectedValue)
  })

  it('declares a fixed dark color scheme', () => {
    expect(rootBlock()).toContain('color-scheme: dark')
  })

  it('has no prefers-color-scheme media query', () => {
    expect(css).not.toContain('prefers-color-scheme')
  })

  it('has no light theme variable or variant', () => {
    expect(css).not.toMatch(/light/i)
  })
})
