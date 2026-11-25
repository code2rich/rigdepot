// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The main paths against a real server, after the wizard (setup.spec.ts): signing in, inviting someone who
// then creates their account, a product with that person in it, uploading a skill and publishing it, and
// finding it in the catalog. Each browser uses names of its own, so both can run against one server.

import { expect, test, type Page } from '@playwright/test'
import { BRAND } from '../../src/brand.gen'
import {
  noMessageKeys,
  noSidewaysScroll,
  ready,
  recordData,
  shot,
  untranslated,
  watchErrors,
} from '../lib/console'
import { ADMIN, PASSWORD, signIn } from '../lib/real'
import { tar } from '../lib/tar'

test.describe.configure({ mode: 'serial' })

let page: Page
let errors: string[]
const notImplemented: string[] = []

test.beforeAll(async ({ browser }) => {
  page = await browser.newPage()
  // Drawers and dialogs appear at once, so screenshots show them.
  await page.emulateMedia({ reducedMotion: 'reduce' })
  errors = watchErrors(page, notImplemented)
})

test.afterAll(async () => {
  if (notImplemented.length) {
    test.info().annotations.push({ type: 'not implemented (501)', description: notImplemented.join(' ') })
  }
  await page.close()
})

test.afterEach(() => {
  expect(errors, 'errors in the browser').toEqual([])
  errors.length = 0
})

/** check is what every page gets: it opened, nothing sideways, no message key showing. */
async function check(where: string) {
  await ready(page)
  await expect(page.locator('main h1'), where).toBeVisible()
  await noSidewaysScroll(page)
  await noMessageKeys(page)
}

test('sign in', async ({ browserName }, info) => {
  await signIn(page, ADMIN)
  await check('home')
  await shot(page, info, `${browserName}-home`)
})

const who = (browserName: string) => browserName.slice(0, 1)
let invited = ''

test('users and sign-in: invite someone, who creates their account', async ({
  browser,
  browserName,
}, info) => {
  const email = `member-${who(browserName)}@example.com`
  invited = `member-${who(browserName)}`
  await page.goto('/admin/users?lang=en')
  await check('users')
  await page.getByRole('button', { name: 'Invite', exact: true }).click()
  await page.getByLabel('E-mail addresses').fill(email)
  await page.getByRole('button', { name: 'Send invitations' }).click()
  // Mail is not set up: the dialog shows the link once.
  const dialog = page.getByRole('dialog')
  const link = (await dialog.getByText(/\/invite\//).textContent())?.trim() ?? ''
  expect(link).toMatch(/^https?:\/\/.+\/invite\/\S+$/)
  await shot(page, info, `${browserName}-invite-link`)
  await dialog.getByRole('button', { name: 'Done' }).click()

  // The invited person opens the link in a browser of their own.
  const theirs = await browser.newPage()
  const theirErrors = watchErrors(theirs, notImplemented)
  await theirs.goto(`${link}?lang=en`)
  await ready(theirs)
  await theirs.getByLabel('Name', { exact: true }).fill(`Member ${who(browserName).toUpperCase()}`)
  await theirs.getByLabel('Account', { exact: true }).fill(invited)
  await theirs.getByLabel('Password', { exact: true }).fill(PASSWORD)
  await theirs.getByLabel('Password again', { exact: true }).fill(PASSWORD)
  await theirs.getByRole('button', { name: 'Create account and sign in' }).click()
  await expect(theirs.locator('main h1')).toBeVisible()
  await shot(theirs, info, `${browserName}-member-home`)
  expect(theirErrors, 'errors in the invited person’s browser').toEqual([])
  await theirs.close()

  await page.reload()
  await check('users')
  await expect(page.locator('main').getByText(invited, { exact: true }).first()).toBeVisible()
})

test('products and members: a product with the new person in it', async ({ browserName }, info) => {
  const code = `pay-${who(browserName)}`
  await page.goto('/products?lang=en')
  await check('products')
  await page.getByRole('button', { name: 'New product' }).first().click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name').fill(`Payments ${who(browserName).toUpperCase()}`)
  await dialog.getByLabel('Code').fill(code)
  await dialog.getByRole('button', { name: 'Create' }).click()
  await expect(dialog).toBeHidden()
  await expect(page.locator('main').getByText(code, { exact: true }).first()).toBeVisible()

  await page.locator('main').getByText(code, { exact: true }).first().click()
  await page.locator('main').getByRole('button', { name: 'Add' }).first().click()
  const add = page.getByRole('dialog')
  await add.getByRole('textbox').first().fill(invited)
  await add.getByText(invited).first().click()
  await add.getByRole('button', { name: 'Add', exact: true }).click()
  await expect(add).toBeHidden()
  const name = `Member ${who(browserName).toUpperCase()}`
  await expect(page.getByRole('combobox', { name: `Role of ${name} in this product` })).toBeVisible()
  await shot(page, info, `${browserName}-products`)
})

let resource = ''

test('resources: upload a skill and publish it', async ({ browserName }, info) => {
  resource = `e2e-notes-${who(browserName)}`
  await page.goto('/manage/resources?lang=en')
  await check('resources')
  await page.getByRole('button', { name: 'New resource' }).first().click()
  const dialog = page.getByRole('dialog')
  await dialog.getByRole('button', { name: /^Skill/ }).click()
  await dialog.getByLabel('Code').fill(resource)
  await dialog.locator('input[type="file"]').setInputFiles({
    name: `${resource}.tar`,
    mimeType: 'application/x-tar',
    buffer: Buffer.from(
      tar({
        [`${resource}/SKILL.md`]: `---\nname: ${resource}\ndescription: Use it when writing release notes from merged changes.\n---\n\n# Release notes\n\nList the merged changes and group them by kind.\n`,
        [`${resource}/meta.yaml`]: `name: Release notes ${who(browserName).toUpperCase()}\nstages: [release]\nwhen: Before a release\ntry: Write the release notes for this version\n`,
      }),
    ) as never,
  })
  await dialog.getByRole('button', { name: 'Upload as draft' }).click()

  // The upload opens the resource's drawer on its draft; publish it from there.
  await expect(page).toHaveURL(new RegExp(`/manage/resources/${resource}`))
  const drawer = page.getByRole('dialog')
  await expect(drawer).toBeVisible()
  await shot(page, info, `${browserName}-resource-draft`)
  const publish = drawer.getByRole('button', { name: 'Publish', exact: true })
  await expect(publish, 'publishing waits for the checks to pass').toBeEnabled()
  await publish.click()
  await expect(
    page.locator('main tr').filter({ hasText: resource }).getByText('Published v1', { exact: true }),
  ).toBeVisible()
  await shot(page, info, `${browserName}-resource-published`)
})

test('catalog: the published skill is there', async ({ browserName }, info) => {
  await page.goto('/catalog?lang=en')
  await check('catalog')
  const card = page.locator('.rc').filter({ hasText: resource })
  await expect(card).toBeVisible()
  await card.locator('.rc-open').click()
  await expect(page).toHaveURL(new RegExp(`/catalog/${resource}`))
  await expect(page.getByRole('dialog')).toBeVisible()
  await shot(page, info, `${browserName}-catalog-drawer`)
})

test('the pages at 390 px, dark, in Chinese and the pseudo-language', async ({ browserName }, info) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await page.emulateMedia({ colorScheme: 'dark' })
  await recordData(page)
  const paths = ['/', '/catalog', '/connect', '/keys', '/feedback', '/products', '/manage/resources']
  const admin = ['/admin/users', '/admin/tools', '/admin/audit', '/admin/settings']
  for (const lang of ['zh-CN', 'en-XA']) {
    for (const path of [...paths, ...admin]) {
      await test.step(`${path} ${lang}`, async () => {
        await page.goto(`${path}?lang=${lang}`)
        await check(`${path} ${lang}`)
        if (lang === 'en-XA') {
          const plain = await untranslated(page, [
            BRAND.name,
            BRAND.cli,
            BRAND.server,
            ADMIN,
            invited,
            resource,
          ])
          expect.soft(plain, `${path}: plain English under the pseudo-language`).toEqual([])
        }
        await shot(
          page,
          info,
          `${browserName}-390-dark-${lang}${path === '/' ? '-home' : path.replaceAll('/', '-')}`,
        )
      })
    }
  }
})
