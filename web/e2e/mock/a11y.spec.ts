// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The automatic accessibility check (axe-core, WCAG 2.1 A and AA): contrast, labels, roles, landmarks, on
// every page in both themes, in Chinese and English. It does not find everything; the keyboard behaviour
// of the prototype is checked by keyboard.spec.ts.

import AxeBuilder from '@axe-core/playwright'
import { expect, test } from '@playwright/test'
import { ready } from '../lib/console'

const PAGES: [string, string][] = [
  ['home', '/?mock=admin'],
  ['catalog', '/catalog?mock=admin'],
  ['catalog drawer', '/catalog/jira?mock=admin'],
  ['connect', '/connect?mock=admin'],
  ['keys', '/keys?mock=admin'],
  ['feedback', '/feedback?mock=admin'],
  ['feedback item', '/feedback/42?mock=admin'],
  ['products', '/products?mock=admin'],
  ['resources', '/manage/resources?mock=admin'],
  ['resource drawer', '/manage/resources/jira?mock=admin'],
  ['users', '/admin/users?mock=admin'],
  ['tools', '/admin/tools?mock=admin'],
  ['audit', '/admin/audit?mock=admin'],
  ['settings', '/admin/settings?mock=admin'],
  ['login', '/login?mock=signed-out'],
  ['apply', '/apply?mock=pending'],
  ['setup', '/setup?mock=setup'],
  ['device', '/device?code=KQ7M-2XHP&mock=admin'],
  ['invite', '/invite/mock-link-0001?mock=signed-out'],
]

// axe reads the page the same way in both engines; once is enough.
test.skip(({ browserName }) => browserName !== 'chromium', 'runs in Chromium only')

for (const theme of ['light', 'dark'] as const) {
  for (const lang of ['zh-CN', 'en'] as const) {
    test(`axe · ${theme} · ${lang}`, async ({ page }) => {
      await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
      for (const [name, path] of PAGES) {
        await test.step(name, async () => {
          await page.goto(`${path}&lang=${lang}`)
          await ready(page)
          const result = await new AxeBuilder({ page })
            .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'])
            .analyze()
          const found = result.violations.map(
            (v) =>
              `${v.id} (${v.impact ?? ''}): ${v.help} — ${v.nodes
                .slice(0, 4)
                .map((n) => n.target.join(' '))
                .join(' | ')}${v.nodes.length > 4 ? ` … ${v.nodes.length} in all` : ''}`,
          )
          expect.soft(found, `${name} (${theme}, ${lang})`).toEqual([])
        })
      }
    })
  }
}
