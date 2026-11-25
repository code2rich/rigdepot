// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The first-run wizard on a server with an empty database: the setup token from the server's environment
// (tools/dev sets it), the first administrator, the defaults for the other steps, no product.

import { expect, test } from '@playwright/test'
import { ADMIN, PASSWORD, SETUP_TOKEN } from '../lib/real'
import { noMessageKeys, noSidewaysScroll, ready, shot, watchErrors } from '../lib/console'

test('the first-run wizard creates the administrator', async ({ page }, info) => {
  const notImplemented: string[] = []
  const errors = watchErrors(page, notImplemented)
  await page.goto('/?lang=en')
  await expect(page, 'a fresh server opens the wizard').toHaveURL(/\/setup/)
  await ready(page)
  await noMessageKeys(page)

  await page.getByLabel('Setup token').fill(SETUP_TOKEN)
  await page.getByLabel('Administrator account').fill(ADMIN)
  await page.getByLabel('Password').fill(PASSWORD)
  await page.getByRole('button', { name: 'Next' }).click()

  await expect(page.getByText('Step 2 / 5', { exact: false })).toBeVisible()
  await shot(page, info, 'setup-methods')
  await page.getByRole('button', { name: 'Next' }).click()

  await expect(page.getByText('Step 3 / 5', { exact: false })).toBeVisible()
  await page.getByRole('button', { name: 'Skip' }).click()

  await expect(page.getByText('Step 4 / 5', { exact: false })).toBeVisible()
  await page.getByRole('button', { name: 'Next' }).click()

  await expect(page.getByText('Step 5 / 5', { exact: false })).toBeVisible()
  await noSidewaysScroll(page)
  await page.getByRole('button', { name: 'Finish and enter the platform' }).click()

  await expect(page).toHaveURL(/\/$|\/\?/)
  await expect(page.locator('main h1')).toContainText(ADMIN)
  await ready(page)
  await noMessageKeys(page)
  await shot(page, info, 'setup-done-home')
  if (notImplemented.length)
    info.annotations.push({ type: 'not implemented', description: notImplemented.join(' ') })
  expect(errors).toEqual([])
})
