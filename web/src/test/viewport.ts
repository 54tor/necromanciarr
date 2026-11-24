import { vi } from 'vitest'

/**
 * stubViewport simule matchMedia pour une largeur donnée : seules les requêtes `(min-width: Npx)` sont évaluées.
 * La fonction rendue change la largeur et prévient les écouteurs, comme un redimensionnement de fenêtre.
 */
export function stubViewport(width: number): (newWidth: number) => void {
  let currentWidth = width
  const listeners = new Map<(event: { matches: boolean }) => void, number | undefined>()
  vi.stubGlobal('matchMedia', (query: string) => {
    const minimum = /min-width:\s*(\d+)px/.exec(query)
    const matchesNow = () => (minimum ? currentWidth >= Number(minimum[1]) : false)
    return {
      get matches() {
        return matchesNow()
      },
      media: query,
      addEventListener: (_: string, listener: (event: { matches: boolean }) => void) =>
        listeners.set(listener, minimum ? Number(minimum[1]) : undefined),
      removeEventListener: (_: string, listener: (event: { matches: boolean }) => void) =>
        listeners.delete(listener),
      addListener: () => undefined,
      removeListener: () => undefined,
    }
  })
  return (newWidth: number) => {
    currentWidth = newWidth
    listeners.forEach((minimum, listener) =>
      listener({ matches: minimum === undefined ? false : currentWidth >= minimum }),
    )
  }
}
