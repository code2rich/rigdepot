// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Every page of the console and where it lives. The navigation is the prototype's NAV (app.js): four
// groups, lettered A to D, and the page codes (A-01 …) come from this order, so keep it in step with the
// prototype. A page's directory is named after its id. To add a page, see web/README.md.

import type { ComponentType } from 'preact'
import { lazy } from 'preact-iso'
import type { MessageKey } from '../i18n'
import type { IconName } from '../ui/icons'
import { notBuilt } from './not-built'

/** Role decides which navigation groups someone sees. It is a convenience, not a permission: the server decides. */
export type Role = 'admin' | 'owner' | 'member'

export interface NavPage {
  id: string
  /** The path under the console's base, as in docs/architecture/07-web-console.md. */
  path: string
  label: MessageKey
  icon: IconName
  component: ComponentType
}

export interface NavGroup {
  label: MessageKey
  roles: readonly Role[]
  /** The optional module the group belongs to; it shows only while the module is enabled (/api/v1/meta). */
  module?: string
  pages: readonly NavPage[]
}

const all: readonly Role[] = ['admin', 'owner', 'member']
const managers: readonly Role[] = ['admin', 'owner']
const admins: readonly Role[] = ['admin']

export const NAV: readonly NavGroup[] = [
  {
    label: 'nav.group.me',
    roles: all,
    pages: [
      { id: 'home', path: '/', label: 'nav.home', icon: 'home', component: lazy(() => import('./home')) },
      { id: 'catalog', path: '/catalog', label: 'nav.catalog', icon: 'box', component: notBuilt('catalog') },
      { id: 'connect', path: '/connect', label: 'nav.connect', icon: 'plug', component: notBuilt('connect') },
      { id: 'keys', path: '/keys', label: 'nav.keys', icon: 'key', component: notBuilt('keys') },
      {
        id: 'feedback',
        path: '/feedback',
        label: 'nav.feedback',
        icon: 'chat',
        component: notBuilt('feedback'),
      },
    ],
  },
  {
    label: 'nav.group.insights',
    roles: managers,
    module: 'insights',
    pages: [
      {
        id: 'insights',
        path: '/insights',
        label: 'nav.insights',
        icon: 'chart',
        component: notBuilt('insights'),
      },
    ],
  },
  {
    label: 'nav.group.manage',
    roles: managers,
    pages: [
      {
        id: 'products',
        path: '/products',
        label: 'nav.products',
        icon: 'users',
        component: notBuilt('products'),
      },
      {
        id: 'resources',
        path: '/manage/resources',
        label: 'nav.resources',
        icon: 'layers',
        component: notBuilt('resources'),
      },
    ],
  },
  {
    label: 'nav.group.system',
    roles: admins,
    pages: [
      {
        id: 'users',
        path: '/admin/users',
        label: 'nav.users',
        icon: 'user',
        component: notBuilt('users'),
      },
      { id: 'tools', path: '/admin/tools', label: 'nav.tools', icon: 'wrench', component: notBuilt('tools') },
      { id: 'audit', path: '/admin/audit', label: 'nav.audit', icon: 'shield', component: notBuilt('audit') },
      {
        id: 'settings',
        path: '/admin/settings',
        label: 'nav.settings',
        icon: 'gear',
        component: notBuilt('settings'),
      },
    ],
  },
]

/** The pages outside the shell: before signing in, and the ones the command line opens. */
export interface PublicPage {
  id: string
  path: string
  label: MessageKey
  component: ComponentType
}

export const PUBLIC: readonly PublicPage[] = [
  { id: 'login', path: '/login', label: 'page.login', component: lazy(() => import('./login')) },
  { id: 'apply', path: '/apply', label: 'page.apply', component: lazy(() => import('./apply')) },
  { id: 'setup', path: '/setup', label: 'page.setup', component: lazy(() => import('./setup')) },
  { id: 'device', path: '/device', label: 'page.device', component: notBuilt('device') },
  { id: 'invite', path: '/invite/:token', label: 'page.invite', component: lazy(() => import('./invite')) },
]

/** visibleGroups are the navigation groups for a role and the enabled modules. */
export function visibleGroups(role: Role, modules: readonly string[]): NavGroup[] {
  return NAV.filter((g) => g.roles.includes(role) && (!g.module || modules.includes(g.module)))
}

/** pageCode is the code of a navigation page as the prototype shows it: A-01 is the first page of group A. */
export function pageCode(
  id: string,
): { letter: string; number: string; group: MessageKey; label: MessageKey } | undefined {
  for (const [g, group] of NAV.entries()) {
    const i = group.pages.findIndex((p) => p.id === id)
    const page = group.pages[i]
    if (page) {
      return {
        letter: String.fromCharCode(65 + g),
        number: String(i + 1).padStart(2, '0'),
        group: group.label,
        label: page.label,
      }
    }
  }
  return undefined
}

/** groupLetter is a navigation group's letter: A for the first group of NAV. */
export function groupLetter(group: NavGroup): string {
  return String.fromCharCode(65 + NAV.indexOf(group))
}

/** findPage returns the navigation page a console path belongs to ("/catalog/x" belongs to catalog). */
export function findPage(path: string): NavPage | undefined {
  let best: NavPage | undefined
  for (const page of NAV.flatMap((g) => g.pages)) {
    const hit = page.path === '/' ? path === '/' : path === page.path || path.startsWith(page.path + '/')
    if (hit && (!best || page.path.length > best.path.length)) best = page
  }
  return best
}

/** findPublic returns the public page a console path belongs to. */
export function findPublic(path: string): PublicPage | undefined {
  const first = '/' + (path.split('/')[1] ?? '')
  return PUBLIC.find((p) => '/' + (p.path.split('/')[1] ?? '') === first)
}
