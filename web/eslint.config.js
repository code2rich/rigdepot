// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

import js from '@eslint/js'
import tseslint from 'typescript-eslint'

export default tseslint.config(
  { ignores: ['dist/', 'node_modules/', 'e2e/.out/', '**/*.gen.ts'] },
  js.configs.recommended,
  ...tseslint.configs.strict,
  {
    rules: {
      // The console must not leave debugging output in people's browsers.
      'no-console': 'error',
      // Keys are written out in full so that tools/dev check can see which are used.
      'no-restricted-syntax': [
        'error',
        {
          selector: "CallExpression[callee.name='t'] > TemplateLiteral:first-child",
          message: 'Write message keys out in full: t("a.b"), not a template literal.',
        },
        {
          selector: "JSXAttribute[name.name='dangerouslySetInnerHTML']",
          message: 'No raw HTML: everything goes through components, which escape it.',
        },
      ],
    },
  },
)
