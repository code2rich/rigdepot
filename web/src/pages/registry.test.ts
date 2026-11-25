// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

import { describe, expect, it } from 'vitest'
import { NAV, PUBLIC, findPage, findPublic, pageCode, visibleGroups } from './registry'

describe('the navigation', () => {
  it('numbers pages as the prototype does', () => {
    expect(pageCode('home')).toMatchObject({ letter: 'A', number: '01' })
    expect(pageCode('feedback')).toMatchObject({ letter: 'A', number: '05' })
    expect(pageCode('insights')).toMatchObject({ letter: 'B', number: '01' })
    expect(pageCode('resources')).toMatchObject({ letter: 'C', number: '02' })
    expect(pageCode('settings')).toMatchObject({ letter: 'D', number: '04' })
    expect(pageCode('login')).toBeUndefined()
  })

  it('shows each role its groups, and a module only while it is enabled', () => {
    const labels = (role: 'admin' | 'owner' | 'member', modules: string[]) =>
      visibleGroups(role, modules).map((g) => g.label)
    expect(labels('member', ['insights'])).toEqual(['nav.group.me'])
    expect(labels('owner', [])).toEqual(['nav.group.me', 'nav.group.manage'])
    expect(labels('owner', ['insights'])).toEqual(['nav.group.me', 'nav.group.insights', 'nav.group.manage'])
    expect(labels('admin', ['insights'])).toEqual([
      'nav.group.me',
      'nav.group.insights',
      'nav.group.manage',
      'nav.group.system',
    ])
  })

  it('finds the page a path belongs to', () => {
    expect(findPage('/')?.id).toBe('home')
    expect(findPage('/catalog')?.id).toBe('catalog')
    expect(findPage('/catalog/design-doc')?.id).toBe('catalog')
    expect(findPage('/manage/resources/x')?.id).toBe('resources')
    expect(findPage('/catalogue')).toBeUndefined()
    expect(findPage('/nowhere')).toBeUndefined()
  })

  it('tells public pages apart', () => {
    expect(findPublic('/login')?.id).toBe('login')
    expect(findPublic('/invite/abc')?.id).toBe('invite')
    expect(findPublic('/')).toBeUndefined()
    expect(findPublic('/catalog')).toBeUndefined()
  })

  it('gives every page a unique id and path', () => {
    const pages = [...NAV.flatMap((g) => g.pages), ...PUBLIC]
    expect(new Set(pages.map((p) => p.id)).size).toBe(pages.length)
    expect(new Set(pages.map((p) => p.path)).size).toBe(pages.length)
  })
})
