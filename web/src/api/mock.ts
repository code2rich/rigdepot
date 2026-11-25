// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// A stand-in server for working on pages before their API exists: `VITE_API_MOCK=1 pnpm dev`. It is only
// imported when that variable is set at build time, so it never reaches a real build. The answers follow
// the examples in api/openapi.yaml; pages add their own endpoints here while they are being built.
//
// ?mock=admin (default), ?mock=owner, ?mock=member pick who is signed in; ?mock=signed-out has nobody.
// mock-auth.ts adds more for the sign-in, setup and users pages (?mock=pending, ?mock=setup, ?mockfail=…).

import type { Schemas } from './client'
import { authMock, patchMeta } from './mock-auth'
import { memberMock } from './mock-member'

type Who = 'admin' | 'owner' | 'member' | 'pending' | 'signed-out'

function who(): Who {
  const v = new URLSearchParams(location.search).get('mock') ?? sessionStorage.getItem('mock')
  const w: Who = v === 'owner' || v === 'member' || v === 'pending' || v === 'signed-out' ? v : 'admin'
  sessionStorage.setItem('mock', w)
  return w
}

const meta: Schemas['ServerMeta'] = {
  version: '0.0.0-dev',
  api: 'v1',
  organization: 'Example Corp',
  setup_complete: true,
  default_locale: 'en',
  locales: ['en', 'zh-CN'],
  login_methods: [{ id: 'local', kind: 'local', name: 'Local account' }],
  modules: ['insights'],
  cli: { min_version: '0.0.0', latest_version: '0.0.0-dev' },
  limits: { max_bundle_bytes: 5242880 },
}

const people: Record<string, Schemas['Me']> = {
  admin: {
    id: '0193a4b2-6c1e-7a00-8000-000000000001',
    username: 'admin',
    display_name: 'Alex Admin',
    email: 'admin@example.com',
    status: 'active',
    platform_admin: true,
    job: 'Developer',
    focus_stages: ['design', 'develop'],
    locale: '',
    memberships: [{ product: { code: 'shop', name: 'Shop' }, role: 'owner' }],
  },
  owner: {
    id: '0193a4b2-6c1e-7a00-8000-000000000002',
    username: 'owner',
    display_name: 'Olive Owner',
    email: 'owner@example.com',
    status: 'active',
    platform_admin: false,
    job: 'Tester',
    focus_stages: ['test'],
    locale: '',
    memberships: [
      { product: { code: 'pay', name: 'Payments' }, role: 'owner' },
      { product: { code: 'shop', name: 'Shop' }, role: 'member' },
    ],
  },
  member: {
    id: '0193a4b2-6c1e-7a00-8000-000000000011',
    username: 'jdoe',
    display_name: 'Jamie Doe',
    email: 'jdoe@example.com',
    status: 'active',
    platform_admin: false,
    job: 'Developer',
    focus_stages: ['design', 'develop'],
    locale: '',
    memberships: [{ product: { code: 'shop', name: 'Shop' }, role: 'member' }],
  },
}

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const problem = (status: number, code: string) =>
  new Response(JSON.stringify({ type: 'about:blank', title: '', status, code }), {
    status,
    headers: { 'Content-Type': 'application/problem+json' },
  })

/** mockServer answers the console's requests in memory. */
export function mockServer(): (request: Request) => Promise<Response> {
  let current: string = who()
  const member = memberMock()
  const auth = authMock({
    get: () => current,
    set: (w) => {
      current = w
      sessionStorage.setItem('mock', w)
    },
    people,
  })
  return async (request) => {
    const path = new URL(request.url).pathname.replace(/^.*\/api\//, '/api/')
    const route = `${request.method} ${path}`
    const failed = auth.first(route)
    if (failed) return failed
    switch (route) {
      case 'GET /api/v1/meta':
        return json(200, patchMeta(meta))
      case 'GET /api/v1/me': {
        const person = people[current]
        return person ? json(200, person) : problem(401, 'auth.required')
      }
      case 'PATCH /api/v1/me': {
        const person = people[current]
        if (!person) return problem(401, 'auth.required')
        const patch = (await request.json()) as Schemas['UpdateMeRequest']
        people[current] = { ...person, ...patch }
        return json(200, people[current])
      }
      case 'POST /api/v1/auth/logout':
        current = 'signed-out'
        sessionStorage.setItem('mock', current)
        return new Response(null, { status: 204 })
    }
    if (current !== 'signed-out' && current !== 'pending') {
      const answer = await member(request, path, current as 'admin' | 'owner' | 'member')
      if (answer) return answer
    }
    return (await auth.last(request, route)) ?? problem(404, 'mock.not_implemented')
  }
}
