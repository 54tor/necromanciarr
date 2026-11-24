import type { Warning } from '@/api/types'

/** Avertissement tel que l'API le renvoie : objet {code, text}, ou chaîne d'un ancien serveur. */
export type WarningInput = Warning | string

/** Texte affichable d'un avertissement, quelle que soit sa forme. */
export function warningText(warning: WarningInput): string {
  return typeof warning === 'string' ? warning : warning.text
}

/** Retire les doublons par texte, en gardant l'ordre. */
export function uniqueWarnings(warnings: WarningInput[]): WarningInput[] {
  const seen = new Set<string>()
  return warnings.filter((warning) => {
    const text = warningText(warning)
    if (seen.has(text)) return false
    seen.add(text)
    return true
  })
}
