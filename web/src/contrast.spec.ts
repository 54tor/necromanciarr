// @vitest-environment node
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const css = readFileSync(fileURLToPath(new URL('./styles/globals.css', import.meta.url)), 'utf8')

function token(name: string): string {
  const value = new RegExp(`${name}:\\s*(#[0-9a-fA-F]{6});`).exec(css)?.[1]
  if (!value) throw new Error(`jeton ${name} introuvable ou non hexadécimal`)
  return value
}

function linearChannel(channel8bit: number): number {
  const ratio = channel8bit / 255
  return ratio <= 0.04045 ? ratio / 12.92 : ((ratio + 0.055) / 1.055) ** 2.4
}

function luminance(hex: string): number {
  const [red, green, blue] = [1, 3, 5].map((start) =>
    linearChannel(parseInt(hex.slice(start, start + 2), 16)),
  ) as [number, number, number]
  return 0.2126 * red + 0.7152 * green + 0.0722 * blue
}

function contrastRatio(foreground: string, background: string): number {
  const [lighter, darker] = [luminance(foreground), luminance(background)].sort(
    (a, b) => b - a,
  ) as [number, number]
  return (lighter + 0.05) / (darker + 0.05)
}

const NORMAL_TEXT_MINIMUM = 4.5
const UI_COMPONENT_MINIMUM = 3

describe('WCAG AA contrast', () => {
  const textPairs: [string, string][] = [
    ['--foreground', '--background'],
    ['--muted-foreground', '--background'],
    ['--card-foreground', '--card'],
    ['--muted-foreground', '--card'],
    ['--primary-foreground', '--primary'],
    ['--secondary-foreground', '--secondary'],
    ['--primary', '--background'],
    ['--primary', '--card'],
    ['--destructive', '--background'],
    ['--destructive', '--card'],
    ['--muted-foreground', '--sidebar'],
    ['--sidebar-foreground', '--sidebar'],
    ['--sidebar-accent-foreground', '--sidebar-accent'],
    ['--sidebar-primary', '--sidebar'],
  ]

  it.each(textPairs)('text %s on %s reaches 4.5', (foregroundToken, backgroundToken) => {
    const gotValue = contrastRatio(token(foregroundToken), token(backgroundToken))

    expect(gotValue).toBeGreaterThanOrEqual(NORMAL_TEXT_MINIMUM)
  })

  // Les bordures décoratives entre surfaces voisines ne sont pas des composants porteurs de sens :
  // seuls le focus (--ring) et les champs de saisie (--input) doivent atteindre 3.
  const uiPairs: [string, string][] = [
    ['--ring', '--background'],
    ['--ring', '--card'],
    ['--input', '--background'],
    ['--sidebar-border', '--sidebar'],
  ]

  it.each(uiPairs)('UI component %s on %s reaches 3', (foregroundToken, backgroundToken) => {
    const gotValue = contrastRatio(token(foregroundToken), token(backgroundToken))

    expect(gotValue).toBeGreaterThanOrEqual(UI_COMPONENT_MINIMUM)
  })
})

describe('status pills meet AA', () => {
  const tones = ['ok', 'warn', 'fail', 'info', 'muted']

  it.each(tones)('pill %s text on its surface reaches 4.5', (tone) => {
    const gotValue = contrastRatio(token(`--state-${tone}`), token(`--state-${tone}-surface`))

    expect(gotValue).toBeGreaterThanOrEqual(NORMAL_TEXT_MINIMUM)
  })
})
