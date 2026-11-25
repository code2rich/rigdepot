// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// What the runs against a real server share: the setup token tools/dev started it with, the first
// administrator's account, signing in through the sign-in page.

import { expect, type Page } from '@playwright/test'

export const SETUP_TOKEN = process.env.E2E_SETUP_TOKEN ?? '<placeholder: setup token>'
export const ADMIN = 'admin'
export const PASSWORD = '<placeholder: e2e password>'

/** signIn signs in with a local account through the sign-in page, in English. */
export async function signIn(page: Page, account: string, password = PASSWORD) {
  await page.goto('/login?lang=en')
  await page.getByLabel('Account').fill(account)
  await page.getByLabel('Password').fill(password)
  await page.getByRole('button', { name: 'Sign in', exact: true }).click()
  await expect(page.locator('main h1')).toBeVisible()
}
