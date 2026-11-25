// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The console's translations. The catalogues are the repository's locales/<tag>/{catalog,check,web}.json,
// bundled at build time, one chunk per language, loaded when the language is chosen. Which languages exist
// is read from the directory names: adding a language needs no code here.

import { signal } from '@preact/signals'
import { BRAND } from '../brand.gen'
import type { MessageKey } from './keys.gen'
import { FALLBACK, matchLocale, render, type Messages, type Params } from './runtime'

export type { MessageKey } from './keys.gen'
export { FALLBACK, matchLocale } from './runtime'

type Loader = () => Promise<unknown>

const files = import.meta.glob('../../../locales/*/{catalog,check,web}.json', {
  import: 'default',
}) as Record<string, Loader>
const names = import.meta.glob('../../../locales/*/meta.json', { import: 'name', eager: true }) as Record<
  string,
  string
>

const tagOf = (path: string) => path.split('/').at(-2) ?? ''

/** Every language the console was built with, and its name in itself (from meta.json). */
export const LANGUAGES: readonly { tag: string; name: string }[] = Object.entries(names)
  .map(([path, name]) => ({ tag: tagOf(path), name }))
  .sort((a, b) => (a.tag === FALLBACK ? -1 : b.tag === FALLBACK ? 1 : a.tag.localeCompare(b.tag)))

const available = LANGUAGES.map((l) => l.tag)

/** Placeholders every message can use without passing them; the names come from brand.json. */
const brandParams: Params = { product: BRAND.name, cli: BRAND.cli, server: BRAND.server, home: BRAND.home }

const catalogues = signal<ReadonlyMap<string, Messages>>(new Map())

/** The language the console shows. Change it with setLocale. */
export const locale = signal(FALLBACK)

async function load(tag: string): Promise<Messages> {
  const loaders = Object.entries(files).filter(([path]) => tagOf(path) === tag)
  const parts = await Promise.all(loaders.map(([, load]) => load() as Promise<Messages>))
  return Object.assign({}, ...parts) as Messages
}

/**
 * setLocale switches the console to the best available match for tag (English always loads too, for
 * whatever the language lacks) and returns the language chosen. The document's lang attribute follows.
 */
export async function setLocale(tag: string): Promise<string> {
  const chosen = matchLocale([tag], available)
  const next = new Map(catalogues.value)
  for (const t of new Set([FALLBACK, chosen])) {
    if (!next.has(t)) next.set(t, await load(t))
  }
  catalogues.value = next
  locale.value = chosen
  document.documentElement.lang = chosen
  return chosen
}

/**
 * chooseLocale picks the console's language (docs/architecture/09-i18n.md): the signed-in person's
 * setting, then the browser's languages, then the default the server put into the page, then English.
 */
export function chooseLocale(personal?: string): string {
  const serverDefault = document.documentElement.dataset.defaultLocale
  return matchLocale([personal, ...navigator.languages, serverDefault], available)
}

/**
 * t returns the message for key in the current language. Write keys out in full (t('nav.home'), never a
 * template literal): tools/dev check reads them to find unused messages. Components that call t re-render
 * when the language changes.
 */
export function t(key: MessageKey, params?: Params): string {
  return render(catalogues.value, locale.value, key, params, brandParams)
}
