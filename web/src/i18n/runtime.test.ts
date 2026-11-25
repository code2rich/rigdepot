// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

import { describe, expect, it } from 'vitest'
import { matchLocale, PSEUDO, pseudo, render, type Messages } from './runtime'

describe('matchLocale', () => {
  const available = ['en', 'zh-CN']
  it.each([
    [['zh-CN'], 'zh-CN'],
    [['zh-cn'], 'zh-CN'],
    [['zh'], 'zh-CN'],
    [['zh-TW'], 'zh-CN'],
    [['zh-Hant-HK'], 'zh-CN'],
    [['zh_SG'], 'zh-CN'],
    [['en-US'], 'en'],
    [['fr-FR', 'zh-CN'], 'zh-CN'],
    [['fr-FR', 'de'], 'en'],
    [[undefined, '', 'en-GB'], 'en'],
    [[], 'en'],
  ])('%j -> %s', (prefs, want) => {
    expect(matchLocale(prefs, available)).toBe(want)
  })

  it('prefers an earlier preference over a closer match of a later one', () => {
    expect(matchLocale(['zh-TW', 'en'], available)).toBe('zh-CN')
  })
})

describe('render', () => {
  const catalogues = new Map<string, Messages>([
    [
      'en',
      {
        'files.total_one': 'one file in {{dir}}',
        'files.total_other': '{{count}} files in {{dir}}',
        greeting: 'Hello, {{name}}',
        'only.en': 'Only in English',
        brand: 'Welcome to {{product}}',
      },
    ],
    [
      'zh-CN',
      {
        'files.total_one': '{{dir}} 里有 {{count}} 个文件',
        'files.total_other': '{{dir}} 里有 {{count}} 个文件',
        greeting: '你好，{{name}}',
      },
    ],
  ])

  it('picks the plural form by count', () => {
    expect(render(catalogues, 'en', 'files.total', { count: 1, dir: 'a' })).toBe('one file in a')
    expect(render(catalogues, 'en', 'files.total', { count: 3, dir: 'a' })).toBe('3 files in a')
    expect(render(catalogues, 'zh-CN', 'files.total', { count: 1, dir: 'a' })).toBe('a 里有 1 个文件')
  })

  it('falls back to English, then to the key itself', () => {
    expect(render(catalogues, 'zh-CN', 'only.en')).toBe('Only in English')
    expect(render(catalogues, 'zh-CN', 'no.such.key')).toBe('no.such.key')
  })

  it('fills the brand placeholders without being passed them, and leaves unknown ones visible', () => {
    expect(render(catalogues, 'en', 'brand', {}, { product: 'Acme' })).toBe('Welcome to Acme')
    expect(render(catalogues, 'en', 'greeting')).toBe('Hello, {{name}}')
  })

  it('inserts values as text, never as markup', () => {
    expect(render(catalogues, 'en', 'greeting', { name: '<b>x</b>' })).toBe('Hello, <b>x</b>')
  })
})

describe('pseudo', () => {
  it('accents letters, keeps placeholders and adds about 40% in brackets', () => {
    const out = pseudo('Sign in to {{server}}')
    expect(out.startsWith('[Šîĝñ îñ ţö {{server}} ')).toBe(true)
    expect(out.endsWith(']')).toBe(true)
    const letters = (s: string) => [...s].filter((c) => /\p{L}/u.test(c)).length
    expect(letters(out)).toBeGreaterThanOrEqual(Math.ceil(letters('Sign in to server') * 1.3))
    expect(out.replace('{{server}}', '')).not.toMatch(/[A-Za-z]/)
  })

  it('leaves text without letters alone', () => {
    expect(pseudo(', ')).toBe(', ')
    expect(pseudo('{{count}}')).toBe('{{count}}')
  })

  it('renders English through it under the pseudo-language, with values filled in afterwards', () => {
    const catalogues = new Map<string, Messages>([
      ['en', { greeting: 'Hello, {{name}}' }],
      ['zh-CN', { greeting: '你好，{{name}}' }],
    ])
    const out = render(catalogues, PSEUDO, 'greeting', { name: 'Ada' })
    expect(out.startsWith('[Ĥéļļö, Ada ')).toBe(true)
    expect(render(catalogues, PSEUDO, 'missing.key')).toBe('missing.key')
  })
})
