// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// What every page test checks, in both runs (mock and real): the page opened without errors in the
// browser's console, nothing scrolls sideways, no message key shows instead of its text, the
// pseudo-language leaves no plain English behind, and the keyboard reaches the page.

import { expect, type Page, type TestInfo } from '@playwright/test'
import { messageKeys } from './keys'

export type Lang = 'zh-CN' | 'en' | 'en-XA'
export type Theme = 'light' | 'dark'

export const LANGS: readonly Lang[] = ['zh-CN', 'en', 'en-XA']
export const THEMES: readonly Theme[] = ['light', 'dark']
export const WIDTHS = [390, 1440] as const

/**
 * watchErrors collects what the page reports as errors: console.error, uncaught exceptions, failed
 * requests. An answer in the 4xx range is the server's answer to a question, which the page handles (401
 * before signing in, 404, 409); the browser logs it all the same, and it is left out. A 501 is an
 * operation the server does not implement yet: it goes to notImplemented when that list is given.
 */
export function watchErrors(page: Page, notImplemented?: string[]): string[] {
  const errors: string[] = []
  page.on('console', (msg) => {
    if (msg.type() !== 'error') return
    if (shooting.has(page) && msg.text().startsWith('Refused to apply a stylesheet')) return
    const status = /Failed to load resource: the server responded with a status of (\d{3})/.exec(
      msg.text(),
    )?.[1]
    if (status?.startsWith('4')) return
    const where = msg.location().url
    if (status === '501' && notImplemented) {
      if (!notImplemented.includes(where)) notImplemented.push(where)
      return
    }
    errors.push(`console: ${msg.text()} ${where}`)
  })
  page.on('pageerror', (err) => errors.push(`exception: ${err.message}`))
  page.on('requestfailed', (req) => {
    // A navigation away cancels what the old page was loading; that is not the page's fault.
    const why = req.failure()?.errorText ?? ''
    if (!/abort|cancel/i.test(why)) errors.push(`request failed: ${req.method()} ${req.url()} ${why}`)
  })
  return errors
}

/** ready waits for the console to have rendered a page: the shell's main area or a public page's card. */
export async function ready(page: Page) {
  await page.waitForLoadState('domcontentloaded')
  await expect(page.locator('#app > *').first()).toBeVisible()
  await page.waitForLoadState('networkidle')
  // Fonts change widths; measure after they are in.
  await page.evaluate(() => document.fonts.ready.then(() => undefined))
}

/** noSidewaysScroll fails when the page is wider than the window (the 390 px rule of the prototype). */
export async function noSidewaysScroll(page: Page) {
  const { scroll, client, culprits } = await page.evaluate(() => {
    const client = document.documentElement.clientWidth
    const culprits: string[] = []
    for (const el of Array.from(document.querySelectorAll<HTMLElement>('body *'))) {
      const r = el.getBoundingClientRect()
      if (r.width > 0 && r.right > client + 1 && getComputedStyle(el).position !== 'fixed') {
        // Report the outermost element that sticks out, not all its children.
        if (!culprits.some((c) => el.closest(`[data-e2e-mark="${c}"]`))) {
          const mark = String(culprits.length)
          el.dataset.e2eMark = mark
          culprits.push(mark)
        }
      }
    }
    const described = culprits.slice(0, 5).map((mark) => {
      const el = document.querySelector<HTMLElement>(`[data-e2e-mark="${mark}"]`)
      if (!el) return mark
      const r = el.getBoundingClientRect()
      return `${el.tagName.toLowerCase()}.${String(el.className).split(' ').join('.')} right=${Math.round(r.right)}: ${(el.textContent ?? '').trim().slice(0, 40)}`
    })
    document.querySelectorAll<HTMLElement>('[data-e2e-mark]').forEach((el) => delete el.dataset.e2eMark)
    return { scroll: document.documentElement.scrollWidth, client, culprits: described }
  })
  expect(
    scroll,
    `the page scrolls sideways (${scroll} > ${client}); sticking out: ${culprits.join(' | ')}`,
  ).toBeLessThanOrEqual(client)
}

/** visibleTexts returns the text people see or hear: text nodes and the labelling attributes. */
async function texts(page: Page): Promise<{ text: string; where: string; data: boolean }[]> {
  return page.evaluate(() => {
    const out: { text: string; where: string; data: boolean }[] = []
    // Data from the server (names, codes, commands, file contents) is not the console's to translate.
    const dataLike = (el: Element | null) =>
      !!el?.closest(
        'code, pre, kbd, samp, .mono, .code, [translate="no"], .cmd, .code-box, input, textarea, select',
      )
    const where = (el: Element | null) => {
      if (!el) return '?'
      const cls =
        typeof el.className === 'string' && el.className ? '.' + el.className.split(' ').join('.') : ''
      return el.tagName.toLowerCase() + cls
    }
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
    for (let n = walker.nextNode(); n; n = walker.nextNode()) {
      const el = n.parentElement
      const text = (n.textContent ?? '').trim()
      if (!text || !el || el.closest('script, style, noscript, [hidden], [aria-hidden="true"]')) continue
      out.push({ text, where: where(el), data: dataLike(el) })
    }
    for (const el of Array.from(
      document.querySelectorAll('[aria-label], [title], [placeholder], img[alt]'),
    )) {
      for (const attr of ['aria-label', 'title', 'placeholder', 'alt']) {
        const v = el.getAttribute(attr)?.trim()
        // A placeholder in a field for code, an address or a command is an example of data.
        const example = attr === 'placeholder' && !!el.closest('.mono, code, [translate="no"]')
        if (v) out.push({ text: v, where: `${where(el)}[${attr}]`, data: example })
      }
    }
    out.push({ text: document.title, where: 'title', data: false })
    return out
  })
}

/** noMessageKeys fails when a message key shows instead of its text: a key missing from the catalogues. */
export async function noMessageKeys(page: Page) {
  const keys = messageKeys()
  const found = (await texts(page)).filter((t) =>
    t.text.split(/[\s()[\]{}'"“”,:;]+/).some((w) => keys.has(w)),
  )
  expect(
    found.map((f) => `${f.where}: ${f.text}`),
    'message keys on the page',
  ).toEqual([])
}

/**
 * Words that stay as they are in every language: the product's names, the terms the glossary keeps in
 * English (locales/README.md), units and formats.
 */
const keptWords =
  /^(Skill|Skills|MCP|Agent|Agents|AI|API|URL|URLs|HTTPS?|OIDC|SSO|GitLab|GitHub|SMTP|JSON|YAML|SQLite|PostgreSQL|Claude|Code|Codex|OpenCode|Gemini|CLI|ID|OK|UTC|KB|MB|GB|KiB|MiB|GiB|Discord|Markdown|README|Git|PM|AM|macOS|Linux|Windows)$/

/**
 * cutMessages returns the console's own text that is cut off with an ellipsis. Under the pseudo-language
 * every message is in brackets, so a cut message is one whose element overflows and holds an opening
 * bracket; names and other data may be cut on purpose and are not reported.
 */
export async function cutMessages(page: Page): Promise<string[]> {
  return page.evaluate(() =>
    Array.from(document.querySelectorAll<HTMLElement>('body *'))
      .filter((el) => {
        const style = getComputedStyle(el)
        return (
          style.textOverflow === 'ellipsis' &&
          // The phone's top bar repeats the page's title, which is in full right below it.
          !el.closest('.mtop') &&
          el.scrollWidth > el.clientWidth + 1 &&
          el.offsetParent !== null &&
          Array.from(el.childNodes).some((n) => n.nodeType === Node.TEXT_NODE && n.textContent?.includes('['))
        )
      })
      .map(
        (el) =>
          `${el.tagName.toLowerCase()}.${String(el.className)}: ${(el.textContent ?? '').trim().slice(0, 60)}`,
      ),
  )
}

/**
 * recordData makes the page remember every string it reads from the server, mocked or real (the console
 * parses every answer with Response.json or Response.text). Those strings are data, which the
 * pseudo-language does not touch; untranslated leaves their words alone. Call it before the first goto.
 */
export async function recordData(page: Page) {
  await page.addInitScript(() => {
    const seen = new Set<string>()
    const w = window as unknown as { __e2eData: Set<string> }
    w.__e2eData = seen
    const add = (v: unknown) => {
      if (typeof v === 'string') seen.add(v)
      else if (typeof v === 'number' || typeof v === 'boolean') seen.add(String(v))
      else if (Array.isArray(v)) v.forEach(add)
      else if (v && typeof v === 'object') {
        // Keys too: some pages show an object's own field names (an audit entry's changes).
        Object.keys(v).forEach((k) => seen.add(k))
        Object.values(v).forEach(add)
      }
    }
    const json = Response.prototype.json
    Response.prototype.json = async function (this: Response) {
      const v: unknown = await json.call(this)
      add(v)
      return v
    }
    const text = Response.prototype.text
    Response.prototype.text = async function (this: Response) {
      const v = await text.call(this)
      try {
        add(JSON.parse(v))
      } catch {
        seen.add(v)
      }
      return v
    }
  })
}

/**
 * untranslated returns the plain English the pseudo-language left on the page: words of three letters or
 * more without accents, outside brackets (where every translated message is), that are not data the page
 * read from the server (recordData), not made by Intl (dates, relative times), not a term every language
 * keeps (keptWords) and not one of kept (the names from brand.json).
 */
export async function untranslated(page: Page, kept: readonly string[]): Promise<string[]> {
  const keep = new Set([...kept, ...(await dataWords(page))].map((k) => k.toLowerCase()))
  const out: string[] = []
  for (const t of await texts(page)) {
    if (t.data) continue
    const rest = t.text.replace(/\[[^\]]*\]?/g, ' ')
    const words = rest.match(/[A-Za-z][A-Za-z'-]{2,}/g) ?? []
    const plain = words.filter((w) => !keptWords.test(w) && !keep.has(w.toLowerCase()))
    if (plain.length > 0) out.push(`${t.where}: ${t.text.slice(0, 80)} [${plain.join(' ')}]`)
  }
  return out
}

/** dataWords are the words of the server's strings (recordData) and of what Intl writes in English. */
async function dataWords(page: Page): Promise<string[]> {
  return page.evaluate(() => {
    const words = new Set<string>()
    const add = (s: string) => s.match(/[A-Za-z][A-Za-z'-]*/g)?.forEach((w) => words.add(w))
    const data = (window as unknown as { __e2eData?: Set<string> }).__e2eData
    data?.forEach(add)
    const lang = document.documentElement.lang
    const rtf = new Intl.RelativeTimeFormat(lang, { numeric: 'auto' })
    const units: Intl.RelativeTimeFormatUnit[] = [
      'second',
      'minute',
      'hour',
      'day',
      'week',
      'month',
      'quarter',
      'year',
    ]
    for (const unit of units) for (const n of [-2, -1, 0, 1, 2]) add(rtf.format(n, unit))
    for (let day = 0; day < 366; day += 1) {
      const d = new Date(Date.UTC(2026, 0, 1 + day))
      for (const month of ['short', 'long'] as const) add(d.toLocaleDateString(lang, { month }))
      if (day < 7)
        for (const weekday of ['short', 'long'] as const) add(d.toLocaleDateString(lang, { weekday }))
    }
    add(new Date().toLocaleTimeString(lang, { hour: 'numeric' }) + ' AM PM')
    return [...words]
  })
}

/**
 * keyboardReachesPage checks the basics: the first Tab lands on the skip link (in the shell), and the next
 * presses move through visible controls, never onto something hidden or off the page.
 */
export async function keyboardReachesPage(page: Page, presses = 12, shell = true) {
  const tab = tabKey(page)
  // Start from the top of the page, as after loading it: focus a non-tabbable marker before everything.
  await page.evaluate(() => {
    const start = document.createElement('span')
    start.tabIndex = -1
    start.id = 'e2e-focus-start'
    document.body.prepend(start)
    start.focus()
  })
  const seen: string[] = []
  for (let i = 0; i < presses; i++) {
    await page.keyboard.press(tab)
    const focus = await page.evaluate(() => {
      const el = document.activeElement as HTMLElement | null
      if (!el || el === document.body) return null
      const r = el.getBoundingClientRect()
      const style = getComputedStyle(el)
      return {
        tag: el.tagName.toLowerCase(),
        cls: String(el.className),
        text: (el.innerText || el.getAttribute('aria-label') || '').trim().slice(0, 30),
        visible: r.width > 0 && r.height > 0 && style.visibility !== 'hidden' && Number(style.opacity) > 0,
      }
    })
    if (!focus) break
    if (i === 0 && shell) expect(focus.cls, 'the first Tab reaches the skip link').toContain('skip')
    expect(focus.visible, `focus went to a hidden element: ${focus.tag}.${focus.cls} "${focus.text}"`).toBe(
      true,
    )
    seen.push(`${focus.tag} ${focus.text}`)
  }
  await page.evaluate(() => document.getElementById('e2e-focus-start')?.remove())
  expect(seen.length, 'Tab moves the focus').toBeGreaterThan(0)
}

/**
 * tabKey is the key that moves the focus to the next control. WebKit on macOS moves Tab between form
 * fields only, as Safari does by default; Option-Tab reaches buttons and links too.
 */
export function tabKey(page: Page): string {
  return page.context().browser()?.browserType().name() === 'webkit' && process.platform === 'darwin'
    ? 'Alt+Tab'
    : 'Tab'
}

/** shot saves a screenshot under E2E_OUT/shots when the run asks for them (E2E_SHOTS=1). */
export async function shot(page: Page, info: TestInfo, name: string) {
  if (process.env.E2E_SHOTS !== '1') return
  const dir = `${process.env.E2E_OUT ?? 'e2e/.out'}/shots/${info.project.name}`
  shooting.add(page)
  try {
    await page.screenshot({
      path: `${dir}/${name}.jpg`,
      fullPage: true,
      type: 'jpeg',
      quality: 60,
      caret: 'initial',
    })
    // The browser reports what the screenshot did a moment later.
    await page.waitForTimeout(100)
  } finally {
    shooting.delete(page)
  }
}

/**
 * shooting are the pages being photographed. In WebKit a screenshot adds a style sheet to the page, which
 * WebKit holds to the server's content security policy and refuses; that error is the test's, not the
 * page's, and watchErrors leaves it out while a screenshot is taken.
 */
const shooting = new WeakSet<Page>()
