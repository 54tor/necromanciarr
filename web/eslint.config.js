import { readFileSync } from 'node:fs'
import js from '@eslint/js'
import pluginVueI18n from '@intlify/eslint-plugin-vue-i18n'
import pluginVue from 'eslint-plugin-vue'
import globals from 'globals'
import tseslint from 'typescript-eslint'

// Fichiers dont la migration vers vue-i18n appartient à D3d ; src/lint-exceptions.spec.ts échoue si l'un n'en a plus besoin.
const { files: rawTextExceptions } = JSON.parse(
  readFileSync(new URL('./lint-exceptions.json', import.meta.url), 'utf8'),
)

export default tseslint.config(
  { ignores: ['node_modules', 'coverage', '../internal/web/ui/dist'] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  ...pluginVue.configs['flat/recommended'],
  {
    files: ['**/*.vue'],
    languageOptions: { parserOptions: { parser: tseslint.parser } },
  },
  {
    // Aucun texte d'interface en dur : tout passe par src/locales/{fr,en}.json (plugin vue-i18n).
    files: ['src/**/*.vue', 'src/**/*.ts'],
    ignores: ['src/**/*.spec.ts', 'src/test/**', 'src/components/ui/**'],
    plugins: { '@intlify/vue-i18n': pluginVueI18n },
    settings: {
      'vue-i18n': { localeDir: './src/locales/*.json', messageSyntaxVersion: '^11.0.0' },
    },
    rules: {
      '@intlify/vue-i18n/no-raw-text': [
        'error',
        // Les attributs lus par l'utilisateur ou un lecteur d'écran sont des textes d'interface aussi.
        {
          attributes: {
            '/.+/': ['title', 'aria-label', 'aria-placeholder', 'aria-description', 'alt'],
            input: ['placeholder'],
            ConfirmDialog: ['description', 'confirm-label', 'confirmLabel'],
            textarea: ['placeholder'],
          },
        },
      ],
      '@intlify/vue-i18n/no-missing-keys': 'error',
    },
  },
  {
    files: rawTextExceptions,
    rules: { '@intlify/vue-i18n/no-raw-text': 'off' },
  },
  {
    // Prettier gère la mise en forme des gabarits : on coupe les règles de présentation en doublon.
    rules: {
      'vue/max-attributes-per-line': 'off',
      'vue/singleline-html-element-content-newline': 'off',
      'vue/html-self-closing': 'off',
      'vue/html-indent': 'off',
      'vue/html-closing-bracket-newline': 'off',
    },
  },
  {
    languageOptions: { globals: globals.browser },
  },
  {
    files: ['*.ts', '*.js'],
    languageOptions: { globals: globals.node },
  },
  {
    // Les composants shadcn sont générés : leurs noms d'une seule syllabe (Button, Table) sont voulus.
    files: ['src/components/ui/**/*.vue'],
    rules: { 'vue/multi-word-component-names': 'off', 'vue/require-default-prop': 'off' },
  },
)
