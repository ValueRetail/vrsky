import js from '@eslint/js'
import globals from 'globals'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import jsxA11y from 'eslint-plugin-jsx-a11y'
import tseslint from 'typescript-eslint'
import { defineConfig, globalIgnores } from 'eslint/config'

export default defineConfig([
  globalIgnores(['dist']),
  {
    files: ['**/*.{ts,tsx}'],
    extends: [
      js.configs.recommended,
      tseslint.configs.recommended,
      reactHooks.configs.flat.recommended,
      reactRefresh.configs.vite,
      jsxA11y.flatConfigs.recommended,
    ],
    languageOptions: {
      ecmaVersion: 2020,
      globals: globals.browser,
    },
    rules: {
      // Our config editors pass a `role="consumer" | "producer"` PROP to custom
      // components (node kind), which is not the DOM ARIA attribute. ignoreNonDOM
      // stops the rule from flagging those custom-component props.
      'jsx-a11y/aria-role': ['error', { ignoreNonDOM: true }],
      // Adopting jsx-a11y on an existing UI: the systematic sources (shared
      // StyledInput/StyledSelect) are fixed; the remaining raw-label + clickable-
      // div findings are surfaced as WARNINGS so `npm run lint` stays green while
      // they're burned down incrementally (then ratcheted back to error). New
      // code still gets the feedback in-editor + in CI logs.
      'jsx-a11y/label-has-associated-control': 'warn',
      'jsx-a11y/no-static-element-interactions': 'warn',
      'jsx-a11y/click-events-have-key-events': 'warn',
      'jsx-a11y/no-autofocus': 'warn',
    },
  },
])
