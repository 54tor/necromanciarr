import { vi } from 'vitest'

/** stubViewport simule matchMedia pour une largeur donnée : seules les requêtes `(min-width: Npx)` sont évaluées. */
export function stubViewport(width: number): void {
  vi.stubGlobal('matchMedia', (query: string) => {
    const minimum = /min-width:\s*(\d+)px/.exec(query)
    return {
      matches: minimum ? width >= Number(minimum[1]) : false,
      media: query,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
      addListener: () => undefined,
      removeListener: () => undefined,
    }
  })
}
