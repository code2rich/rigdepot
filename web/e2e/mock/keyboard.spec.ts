// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The keyboard through drawers and dialogs (docs/architecture/07-web-console.md, 几件容易做错的事): Tab
// reaches what opens them, Enter opens, the focus goes in and stays in, Escape closes and the focus comes
// back to where it was.

import { expect, test, type Locator, type Page } from '@playwright/test'
import { tabKey } from '../lib/console'

/** tabTo presses Tab until target has the focus, as someone with only a keyboard would. */
async function tabTo(page: Page, target: Locator, max = 80) {
  const key = tabKey(page)
  for (let i = 0; i < max; i++) {
    await page.keyboard.press(key)
    if (await target.evaluate((el) => el === document.activeElement)) return
  }
  throw new Error(`Tab did not reach ${target.toString()} in ${max} presses`)
}

/** throughOverlay checks an open drawer or dialog: focus inside, kept inside, Escape back to the opener. */
async function throughOverlay(page: Page, opener: Locator) {
  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible()
  const inside = () => page.evaluate(() => !!document.activeElement?.closest('[role="dialog"]'))
  await expect.poll(inside, { message: 'the focus moves into the overlay' }).toBe(true)
  const key = tabKey(page)
  for (let i = 0; i < 40; i++) {
    await page.keyboard.press(key)
    expect(await inside(), `the focus stays inside the overlay (Tab ${i + 1})`).toBe(true)
  }
  await page.keyboard.press(`Shift+${key}`)
  expect(await inside(), 'Shift+Tab stays inside too').toBe(true)
  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()
  await expect(opener, 'the focus is back where it was').toBeFocused()
}

test.beforeEach(async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
})

test('a resource card opens its drawer from the keyboard', async ({ page }) => {
  await page.goto('/catalog?mock=member&lang=en')
  const card = page.locator('.rc .rc-open').first()
  await expect(card).toBeVisible()
  await tabTo(page, card)
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(/\/catalog\/[a-z0-9-]+/)
  await throughOverlay(page, card)
  await expect(page).toHaveURL(/\/catalog(\?|$)/)
})

test('a resource row opens the editing drawer from the keyboard', async ({ page }) => {
  await page.goto('/manage/resources?mock=admin&lang=en')
  const row = page.locator('tr[tabindex="0"]').first()
  await expect(row).toBeVisible()
  await tabTo(page, row, 120)
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(/\/manage\/resources\/[a-z0-9-]+/)
  await throughOverlay(page, row)
})

test('the feedback button opens its dialog from the keyboard', async ({ page }) => {
  await page.goto('/?mock=member&lang=en')
  const fab = page.locator('button.fab')
  await expect(fab).toBeVisible()
  await tabTo(page, fab, 150)
  await page.keyboard.press('Enter')
  await throughOverlay(page, fab)
})

test('the skip link moves the focus to the page', async ({ page }) => {
  await page.goto('/catalog?mock=member&lang=en')
  await expect(page.locator('main h1')).toBeVisible()
  const skip = page.locator('button.skip')
  await tabTo(page, skip, 3)
  await expect(skip).toBeVisible()
  await page.keyboard.press('Enter')
  const inMain = await page.evaluate(() => !!document.activeElement?.closest('main'))
  expect(inMain, 'after the skip link the focus is in the main area').toBe(true)
})
