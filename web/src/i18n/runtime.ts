// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The message runtime, without any browser state: choosing a language,
// looking up a message with its plural form, filling placeholders. The Go
// side (internal/i18n) follows the same rules; docs/architecture/09-i18n.md.

/** Messages of one language: flat key -> text, as in locales/<tag>/*.json. */
export type Messages = Readonly<Record<string, string>>

/** The language everything falls back to. A key missing here is a defect that tools/dev check catches. */
export const FALLBACK = 'en'

/**
 * The pseudo-language: English with every letter swapped for an accented look-alike, about 40% longer and
 * in brackets ("[Šîĝñ îñ ļöŕéɱ]"). It is for development and the browser tests, never offered to people:
 * text that stays plain English under it was not translated, and a missing closing bracket shows text that
 * was cut off (docs/architecture/09-i18n.md). Choose it with ?lang=en-XA; it is never matched automatically.
 */
export const PSEUDO = 'en-XA'

/** Values for placeholders. `count` also picks the plural form. */
export type Params = Readonly<Record<string, string | number>>

const placeholder = /\{\{([A-Za-z0-9_]+)\}\}/g

/**
 * matchLocale picks one of the available languages for a list of preferences, most preferred first
 * (the person's setting, navigator.languages, the server's default). Each preference is tried exactly,
 * then by its base language; Chinese in any script or region falls back to zh-CN until other Chinese
 * catalogues exist. Nothing matching gives the fallback.
 */
export function matchLocale(
  prefs: readonly (string | null | undefined)[],
  available: readonly string[],
): string {
  const byLower = new Map(available.map((tag) => [tag.toLowerCase(), tag]))
  for (const raw of prefs) {
    const pref = (raw ?? '').trim().replaceAll('_', '-').toLowerCase()
    if (!pref) continue
    const exact = byLower.get(pref)
    if (exact) return exact
    const base = pref.split('-')[0] ?? ''
    const same = available.filter((tag) => tag.toLowerCase().split('-')[0] === base)
    if (same.length > 0) {
      return (
        same.find((tag) => tag === 'zh-CN') ??
        same.find((tag) => tag.toLowerCase() === base) ??
        same[0] ??
        FALLBACK
      )
    }
  }
  return available.includes(FALLBACK) ? FALLBACK : (available[0] ?? FALLBACK)
}

const pluralRules = new Map<string, Intl.PluralRules>()

function pluralForm(locale: string, count: number): Intl.LDMLPluralRule {
  let rules = pluralRules.get(locale)
  if (!rules) {
    rules = new Intl.PluralRules(locale)
    pluralRules.set(locale, rules)
  }
  return rules.select(count)
}

function lookup(msgs: Messages | undefined, locale: string, key: string, count: number | undefined) {
  if (!msgs) return undefined
  if (count !== undefined) {
    return msgs[`${key}_${pluralForm(locale, count)}`] ?? msgs[`${key}_other`] ?? msgs[key]
  }
  return msgs[key]
}

/**
 * render returns the message for key in locale, falling back to English, with placeholders filled. A
 * numeric `count` picks the plural form (key_one, key_other …). Values are inserted as they are: format
 * numbers and dates before passing them (format.ts). A key missing everywhere comes back as itself, so
 * the defect shows on the page.
 */
export function render(
  catalogues: ReadonlyMap<string, Messages>,
  locale: string,
  key: string,
  params: Params = {},
  always: Params = {},
): string {
  const count = typeof params.count === 'number' ? params.count : undefined
  let text =
    (locale === PSEUDO ? undefined : lookup(catalogues.get(locale), locale, key, count)) ??
    lookup(catalogues.get(FALLBACK), FALLBACK, key, count) ??
    key
  // The template is transformed before its placeholders are filled: values are data, not translations.
  if (locale === PSEUDO && text !== key) text = pseudo(text)
  return text.replace(placeholder, (match, name: string) => {
    const value = name in params ? params[name] : always[name]
    return value === undefined ? match : String(value)
  })
}

const accented: Readonly<Record<string, string>> = Object.fromEntries(
  [...'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ'].map((c, i) => [
    c,
    [...'åƀçðéƒĝĥîĵķļɱñöþǫŕšţûṽŵẋýžÅƁÇÐÉƑĜĤÎĴĶĻṀÑÖÞǪŔŠŢÛṼŴẊÝŽ'][i] ?? c,
  ]),
)

/** Words that make pseudo-language text longer; they wrap like words do. */
const padding = 'ļöŕéɱ îþšûɱ ðöļöŕ šîţ åɱéţ çöñšéçţéţûŕ åðîþîšçîñĝ éļîţ šéð ðö éîûšɱöð ţéɱþöŕ'

/**
 * pseudo turns an English message into the pseudo-language: letters accented, placeholders ({{name}})
 * untouched, about 40% more letters as extra words, the whole in brackets. Text without letters (a
 * separator, a number format) stays as it is.
 */
export function pseudo(text: string): string {
  let letters = 0
  const parts = text.split(/(\{\{[A-Za-z0-9_]+\}\})/)
  const out = parts
    .map((part, i) => {
      if (i % 2 === 1) return part
      return part.replace(/[A-Za-z]/g, (c) => {
        letters++
        return accented[c] ?? c
      })
    })
    .join('')
  if (letters === 0) return text
  const words = padding.split(' ')
  const kept: string[] = []
  for (let i = 0, n = 0; n < Math.ceil(letters * 0.4); i++) {
    const word = words[i % words.length] ?? ''
    kept.push(word)
    n += [...word].length
  }
  return `[${out} ${kept.join(' ')}]`
}
