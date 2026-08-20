import js from '@eslint/js'
import globals from 'globals'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import jsxA11y from 'eslint-plugin-jsx-a11y'
import tseslint from 'typescript-eslint'
import { defineConfig, globalIgnores } from 'eslint/config'

export default defineConfig([
  globalIgnores(['dist', 'coverage', 'node_modules']),
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
      // label-has-associated-control is fully burned down → enforced as error so
      // regressions fail the build. The remaining clickable-<div> keyboard
      // findings (pipeline canvas + context menus) need a proper keyboard-nav
      // design, and no-autofocus fires on two intentional just-opened-field
      // focuses; both stay WARN until addressed, so `npm run lint` stays green
      // while new violations are still surfaced in-editor + CI.
      'jsx-a11y/no-static-element-interactions': 'warn',
      'jsx-a11y/click-events-have-key-events': 'warn',
      'jsx-a11y/no-autofocus': 'warn',
      // Honor the codebase's `_`-prefix convention for intentionally-unused
      // params/vars/catch bindings/destructured rest (e.g. `const { x: _, ...rest }`).
      '@typescript-eslint/no-unused-vars': ['error', {
        argsIgnorePattern: '^_',
        varsIgnorePattern: '^_',
        caughtErrorsIgnorePattern: '^_',
        destructuredArrayIgnorePattern: '^_',
      }],
    },
  },
])
