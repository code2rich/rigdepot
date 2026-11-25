// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Every page against the development stand-in: three roles (and the pages before signing in) × light and
// dark × 390 and 1440 px × Chinese, English and the pseudo-language. One test per combination opens each of
// that role's pages in turn and checks it (lib/console.ts); the failures of all pages are reported together.

import { expect, test } from '@playwright/test'
import { BRAND } from '../../src/brand.gen'
import {
  LANGS,
  cutMessages,
  THEMES,
  WIDTHS,
  keyboardReachesPage,
  noMessageKeys,
  noSidewaysScroll,
  ready,
  recordData,
  shot,
  untranslated,
  watchErrors,
} from '../lib/console'

interface Visit {
  /** A name for the screenshot and the failure messages. */
  name: string
  path: string
  /** ?mock= for the visit (who is signed in, or the state of the server). */
  mock: string
  /** Inside the shell: a page header and the skip link. */
  shell: boolean
}

const me = ['/', '/catalog', '/catalog/jira', '/connect', '/keys', '/feedback', '/feedback/42']
const manage = ['/insights', '/products', '/manage/resources', '/manage/resources/jira']
const system = ['/admin/users', '/admin/tools', '/admin/audit', '/admin/settings']

const inShell = (mock: string, paths: string[]): Visit[] =>
  paths.map((path) => ({
    name: (path === '/' ? 'home' : path.slice(1).replaceAll('/', '-')) + '',
    path,
    mock,
    shell: true,
  }))

const GROUPS: Record<string, Visit[]> = {
  admin: inShell('admin', [...me, ...manage, ...system]),
  owner: inShell('owner', [...me, ...manage]),
  member: inShell('member', me),
  public: [
    { name: 'login', path: '/login', mock: 'signed-out', shell: false },
    { name: 'apply', path: '/apply', mock: 'pending', shell: false },
    { name: 'setup', path: '/setup', mock: 'setup', shell: false },
    { name: 'device', path: '/device?code=KQ7M-2XHP', mock: 'admin', shell: false },
    { name: 'invite', path: '/invite/mock-link-0001', mock: 'signed-out', shell: false },
  ],
}

/** The names that come from brand.json and stay as they are in every language. */
const brandWords = [BRAND.name, BRAND.cli, BRAND.server, ...BRAND.server.split('-')]

for (const [group, visits] of Object.entries(GROUPS)) {
  for (const lang of LANGS) {
    for (const theme of THEMES) {
      for (const width of WIDTHS) {
        test(`${group} · ${lang} · ${theme} · ${width}px`, async ({ page }, info) => {
          await page.setViewportSize({ width, height: width < 600 ? 844 : 900 })
          await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
          const errors = watchErrors(page)
          await recordData(page)
          for (const v of visits) {
            await test.step(v.name, async () => {
              errors.length = 0
              const sep = v.path.includes('?') ? '&' : '?'
              await page.goto(`${v.path}${sep}mock=${v.mock}&lang=${lang}`)
              await ready(page)
              const where = `${v.name} (${group}, ${lang}, ${theme}, ${width}px)`

              // It opened, in the language and theme asked for.
              await expect.soft(page.locator('html'), where).toHaveAttribute('lang', lang)
              const dark = await page.evaluate(() => getComputedStyle(document.body).backgroundColor)
              expect
                .soft(isDark(dark), `${where}: the background ${dark} follows the ${theme} theme`)
                .toBe(theme === 'dark')
              if (v.shell) {
                await expect.soft(page.locator('main h1').first(), where).toBeVisible()
              } else {
                await expect.soft(page.locator('h1, h2').first(), where).toBeVisible()
              }

              await soft(where, () => noSidewaysScroll(page))
              await soft(where, () => noMessageKeys(page))
              if (lang === 'en-XA') {
                const plain = await untranslated(page, brandWords)
                expect.soft(plain, `${where}: plain English under the pseudo-language`).toEqual([])
                expect.soft(await cutMessages(page), `${where}: text cut off`).toEqual([])
              }
              await shot(page, info, `${group}-${v.name}-${lang}-${theme}-${width}`)
              // The keyboard: once per page, at one size, in one language (the order does not change).
              if (lang === 'en' && theme === 'light' && width === 1440) {
                await soft(where, () => keyboardReachesPage(page, 12, v.shell))
              }
              expect.soft(errors, `${where}: errors in the browser`).toEqual([])
            })
          }
        })
      }
    }
  }
}

/** soft runs a check and records its failure without stopping the test, like expect.soft. */
async function soft(where: string, check: () => Promise<void>) {
  try {
    await check()
  } catch (err) {
    expect.soft(String(err instanceof Error ? err.message : err), where).toBe('')
  }
}

/** isDark tells a dark background from a light one by its luminance. */
function isDark(rgb: string): boolean {
  const [r = 255, g = 255, b = 255] = (rgb.match(/\d+/g) ?? []).map(Number)
  return 0.2126 * r + 0.7152 * g + 0.0722 * b < 128
}
