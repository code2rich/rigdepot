// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The console's message keys (locales/en/{catalog,check,web}.json), to recognise a key shown in place of
// its text: the console renders a key it cannot find as the key itself.

import catalog from '../../../locales/en/catalog.json' with { type: 'json' }
import check from '../../../locales/en/check.json' with { type: 'json' }
import web from '../../../locales/en/web.json' with { type: 'json' }

let keys: Set<string> | undefined

export function messageKeys(): Set<string> {
  if (!keys) {
    keys = new Set()
    for (const k of [...Object.keys(catalog), ...Object.keys(check), ...Object.keys(web)]) {
      keys.add(k)
      keys.add(k.replace(/_(zero|one|two|few|many|other)$/, ''))
    }
  }
  return keys
}
