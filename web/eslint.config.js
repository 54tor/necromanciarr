import js from '@eslint/js'
import pluginVue from 'eslint-plugin-vue'
import globals from 'globals'
import tseslint from 'typescript-eslint'

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
