import type { DedupeDetails, MaintenanceProposal, Warning } from '@/api/types'
import { t, te } from '@/i18n'

/** Ancienne forme courte, encore lue côté serveur : `no_client_recheck`. */
const LEGACY_KEY_PREFIX = 'warning.'

/** Clé de locale d'un code d'avertissement, ou `undefined` si l'interface ne le connaît pas. */
function localeKeyOf(code: string): string | undefined {
  if (te(code)) return code
  const prefixed = `${LEGACY_KEY_PREFIX}${code}`
  return te(prefixed) ? prefixed : undefined
}

/** Texte d'une clé ou d'un texte stocké : traduit dans la langue de l'interface si connu, sinon tel quel. */
function renderStored(codeOrText: string): string {
  const key = localeKeyOf(codeOrText)
  return key === undefined ? codeOrText : t(key)
}

/** Avertissements `{code, text}` d'une proposition, sans doublon : le code connu suit la langue, sinon le texte serveur. */
export function warningTextsOf(warnings: Warning[]): string[] {
  const texts = warnings.map((warning) =>
    localeKeyOf(warning.code) === undefined ? warning.text : renderStored(warning.code),
  )
  return [...new Set(texts)]
}

/**
 * Avertissements d'une proposition dedupe, sans doublon : ceux de la proposition (`{code, text}`) d'abord, puis les clés
 * du plan absentes de la liste. Une clé connue suit la langue de l'interface ; une ligne ancienne ou inconnue est
 * rendue telle quelle.
 */
export function dedupeWarningTexts(item: MaintenanceProposal<DedupeDetails>): string[] {
  const texts = warningTextsOf(item.warnings)
  const knownCodes = new Set(item.warnings.map((warning) => warning.code))
  for (const stored of item.details.warnings ?? []) {
    const isAlreadyListed =
      knownCodes.has(stored) || knownCodes.has(`${LEGACY_KEY_PREFIX}${stored}`)
    if (!isAlreadyListed) texts.push(renderStored(stored))
  }
  return [...new Set(texts)]
}
