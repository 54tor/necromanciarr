import { createI18n, type Composer } from 'vue-i18n'
import en from '@/locales/en.json'
import fr from '@/locales/fr.json'

export type Language = 'fr' | 'en'
/** Le français est le schéma maître : `vue-tsc` refuse un `en.json` auquel il manque une clé. */
export type MessageSchema = typeof fr

const FALLBACK_LANGUAGE: Language = 'fr'

// Runtime seul (option `runtimeOnly` du plugin dans vite.config.ts) et messages précompilés par le plugin :
// aucun `eval` ni `new Function`, ce que la CSP du serveur (sans `unsafe-eval`) exige.
export const i18n = createI18n<[MessageSchema], Language>({
  legacy: false,
  locale: FALLBACK_LANGUAGE,
  fallbackLocale: FALLBACK_LANGUAGE,
  messages: { fr, en },
})

/** t traduit une clé dans la langue active (instance globale : utilisable hors composant et dans les gabarits). */
const composer = i18n.global as unknown as Composer
export const { t } = composer
const { te } = composer

/** setLanguage bascule la langue de l'interface (toute l'interface suit : la locale est réactive). */
export function setLanguage(language: Language): void {
  composer.locale.value = language
}

/** initialLanguage choisit `fr` ou `en` d'après les langues du navigateur (première reconnue), `fr` sinon. */
export function initialLanguage(navigatorLanguages: readonly string[]): Language {
  for (const tag of navigatorLanguages) {
    const primary = tag.toLowerCase().split('-')[0]
    if (primary === 'fr' || primary === 'en') return primary
  }
  return FALLBACK_LANGUAGE
}

/** currentLanguage rend la langue active de l'interface. */
export function currentLanguage(): Language {
  return composer.locale.value as Language
}

export type CodeGroup = 'kind' | 'state' | 'reason' | 'decision' | 'doctor'

/** codeLabel traduit un code machine ; un code inconnu (ou vide) est rendu brut, jamais vide. */
export function codeLabel(group: CodeGroup, code: string): string {
  const key = `codes.${group}.${code}`
  return te(key) ? t(key) : code
}
