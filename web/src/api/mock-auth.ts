// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The development stand-in (mock.ts) for signing in, applying, invitations, the first-run wizard, device
// confirmation and D-01 Users & sign-in. Shaped like the examples in api/openapi.yaml; the names are
// made up. Besides ?mock=admin|owner|member|signed-out it understands:
//
//   ?mock=pending       someone who signed in through SSO for the first time (the apply page)
//   ?mock=setup         a fresh server: the wizard from its first step (the setup token is anything but "wrong")
//   ?mock=setup-admin   the wizard after its first step, signed in as the new administrator
//   ?mockfail=403       every call of these pages fails with 403 (or network, 500); ?mockfail=off stops it
//   ?mockempty=1        no products, requests, invitations, sources or modules (?mockempty=0 stops it)
//
// Accounts for the sign-in form: admin, owner, jdoe (member), wyan (pending); any password but "wrong"
// ("locked" answers as a locked account). Invitation links: /invite/<anything>, /invite/expired-link-0000
// is used up. Device codes: /device?code=KQ7M-2XHP waits, EXPD-0000 has expired, anything else is unknown.

import type { Schemas } from './client'

/** Session is how mock.ts shares who is signed in. */
export interface Session {
  get: () => string
  set: (who: string) => void
  people: Record<string, Schemas['Me']>
}

const json = (status: number, body: unknown, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } })

const problem = (status: number, code: string, detail = '') =>
  new Response(JSON.stringify({ type: 'about:blank', title: '', status, code, detail, request_id: 'mock' }), {
    status,
    headers: { 'Content-Type': 'application/problem+json' },
  })

const none = () => new Response(null, { status: 204 })

const id = (n: number) => `0193a4b2-6c1e-7a00-8000-${String(n).padStart(12, '0')}`
const ago = (minutes: number) => new Date(Date.now() - minutes * 60_000).toISOString()
const ref = (u: { id: string; username: string; display_name: string }) => ({
  id: u.id,
  username: u.username,
  display_name: u.display_name,
})

export const PENDING: Schemas['Me'] = {
  id: id(12),
  username: 'wyan',
  display_name: 'Wen Yan',
  email: 'wyan@example.com',
  status: 'pending',
  platform_admin: false,
  focus_stages: [],
  locale: '',
  memberships: [],
}

/** The stages, named in the language of the request (the server translates them). */
const stages = (request: Request): Schemas['Stage'][] =>
  [
    ['requirement', 'Requirements', '需求'],
    ['architecture', 'Architecture', '架构'],
    ['design', 'Design', '设计'],
    ['develop', 'Development', '开发'],
    ['test', 'Testing', '测试'],
    ['scm', 'SCM', '配管'],
    ['release', 'Release', '发布'],
    ['ops', 'Operations', '运维'],
  ].map(([sid, en, zh]) => ({
    id: sid ?? '',
    name: ((request.headers.get('Accept-Language') ?? '').startsWith('zh') ? zh : en) ?? '',
    hidden: false,
  }))

const owners = {
  admin: { id: id(1), username: 'admin', display_name: 'Alex Admin' },
  gyuan: { id: id(13), username: 'gyuan', display_name: 'Gu Yuan' },
  blu: { id: id(14), username: 'blu', display_name: 'Bai Lu' },
  yzhou: { id: id(15), username: 'yzhou', display_name: 'Ye Zhou' },
}

function startProducts(): Schemas['Product'][] {
  return [
    {
      code: 'shop',
      name: 'Shop',
      line: 'Commerce',
      owners: [owners.admin, owners.gyuan],
      member_count: 64,
      resource_count: 7,
    },
    {
      code: 'pay',
      name: 'Payments',
      line: 'Commerce',
      owners: [owners.blu],
      member_count: 38,
      resource_count: 3,
    },
    {
      code: 'app',
      name: 'Mobile',
      line: 'Users',
      owners: [owners.yzhou],
      member_count: 42,
      resource_count: 4,
    },
    { code: 'data', name: 'Data platform', line: 'Data', owners: [], member_count: 27, resource_count: 2 },
  ]
}

const shop = { code: 'shop', name: 'Shop' }
const pay = { code: 'pay', name: 'Payments' }
const app = { code: 'app', name: 'Mobile' }
const data = { code: 'data', name: 'Data platform' }

function user(
  n: number,
  username: string,
  display_name: string,
  extra: Partial<Schemas['User']> = {},
): Schemas['User'] {
  return {
    id: id(n),
    username,
    display_name,
    email: `${username}@example.com`,
    email_verified: true,
    status: 'active',
    platform_admin: false,
    job: 'Developer',
    focus_stages: ['design', 'develop'],
    memberships: [],
    identities: [id(101)],
    created_at: '2026-09-01T08:00:00Z',
    last_login_at: ago(30),
    ...extra,
  }
}

function startUsers(): Schemas['User'][] {
  return [
    user(1, 'admin', 'Alex Admin', {
      platform_admin: true,
      memberships: [{ product: shop, role: 'owner' }],
      identities: ['local', id(101)],
      last_login_at: ago(3),
    }),
    user(13, 'gyuan', 'Gu Yuan', {
      job: 'Architect',
      focus_stages: ['architecture', 'design'],
      memberships: [{ product: shop, role: 'owner' }],
      identities: [id(102)],
      last_login_at: ago(70),
    }),
    user(2, 'owner', 'Olive Owner', {
      job: 'Tester',
      focus_stages: ['test'],
      memberships: [
        { product: pay, role: 'owner' },
        { product: shop, role: 'member' },
      ],
      last_login_at: ago(65),
    }),
    user(15, 'yzhou', 'Ye Zhou', {
      memberships: [{ product: app, role: 'owner' }],
      identities: [id(102)],
      last_login_at: ago(60 * 15),
    }),
    user(11, 'jdoe', 'Jamie Doe', {
      memberships: [{ product: shop, role: 'member' }],
      last_login_at: ago(12),
    }),
    user(16, 'sshen', 'Sam Shen', {
      job: 'Operations',
      focus_stages: ['release', 'ops'],
      memberships: [
        { product: shop, role: 'member' },
        { product: data, role: 'member' },
      ],
      last_login_at: ago(60 * 24 * 11),
    }),
    user(17, 'cyuan', 'Chen Yuan', {
      memberships: [{ product: pay, role: 'member' }],
      identities: ['local'],
      last_login_at: undefined,
    }),
    user(12, 'wyan', 'Wen Yan', {
      status: 'pending',
      job: undefined,
      focus_stages: [],
      identities: [id(102)],
      last_login_at: ago(20),
    }),
    user(18, 'lyi', 'Lee Yi', {
      status: 'disabled',
      identities: [id(102)],
      last_login_at: ago(60 * 24 * 56),
    }),
  ]
}

function startMethods(): Schemas['IdentityProvider'][] {
  return [
    {
      id: id(101),
      kind: 'oidc',
      name: 'Company SSO',
      enabled: true,
      config: { issuer: 'https://sso.example.com/realms/dev', client_id: 'hub' },
      has_client_secret: true,
      etag: '"p-101-1"',
    },
    {
      id: id(102),
      kind: 'gitlab',
      name: 'GitLab',
      enabled: true,
      config: { url: 'https://gitlab.example.com', client_id: 'hub' },
      has_client_secret: true,
      etag: '"p-102-1"',
    },
    {
      id: 'local',
      kind: 'local',
      name: 'Local account',
      enabled: true,
      config: {},
      has_client_secret: false,
      etag: '"p-local-1"',
    },
  ]
}

const KINDS: Schemas['IdentityProviderKind'][] = [
  {
    kind: 'oidc',
    name: 'OpenID Connect',
    config_schema: {
      type: 'object',
      required: ['issuer', 'client_id'],
      properties: { issuer: { type: 'string', format: 'uri' }, client_id: { type: 'string' } },
    },
  },
  {
    kind: 'gitlab',
    name: 'GitLab',
    config_schema: {
      type: 'object',
      required: ['url', 'client_id'],
      properties: { url: { type: 'string', format: 'uri' }, client_id: { type: 'string' } },
    },
  },
]

const flag = (name: string, values: readonly string[]): string => {
  const v = new URLSearchParams(location.search).get(name)
  if (v !== null) {
    if (values.includes(v)) sessionStorage.setItem(name, v)
    else sessionStorage.removeItem(name)
  }
  return sessionStorage.getItem(name) ?? ''
}

/** The two places mock.ts asks: first, before anything else answers; last, for what nobody else answered. */
export interface AuthMock {
  /** first fails the call on purpose (?mockfail=…): an answer, or a thrown network error, or undefined. */
  first: (route: string) => Response | undefined
  /** last answers the requests of the sign-in, setup, device and users pages, or undefined for others. */
  last: (request: Request, route: string) => Promise<Response | undefined>
}

export function authMock(session: Session): AuthMock {
  const mode = new URLSearchParams(location.search).get('mock')
  let setup: 'none' | 'open' | 'admin' = 'none'
  if (mode === 'setup' || mode === 'setup-admin') {
    setup = mode === 'setup' ? 'open' : 'admin'
    sessionStorage.setItem('mock-setup', setup)
    session.set(mode === 'setup' ? 'signed-out' : 'admin')
  } else if (mode !== null) {
    sessionStorage.removeItem('mock-setup')
  } else {
    const kept = sessionStorage.getItem('mock-setup')
    if (kept === 'open' || kept === 'admin') setup = kept
  }
  const fail = flag('mockfail', ['403', '500', 'network'])
  const empty = flag('mockempty', ['1']) === '1'
  session.people.pending = PENDING

  let products = empty ? [] : startProducts()
  let users = startUsers()
  let invites: Schemas['Invite'][] = empty
    ? []
    : [
        {
          id: id(901),
          email: 'new.colleague@example.com',
          products: [shop],
          created_by: owners.admin,
          created_at: ago(60 * 5),
          expires_at: new Date(Date.now() + 6.8 * 24 * 3600_000).toISOString(),
        },
      ]
  let methods = startMethods()
  const myRequests: Schemas['JoinRequest'][] = []
  const devices: Record<string, Schemas['DeviceRequest']> = {
    'KQ7M-2XHP': {
      user_code: 'KQ7M-2XHP',
      name: 'dev-laptop',
      os: 'darwin',
      arch: 'arm64',
      cli_version: '0.5.0',
      status: 'pending',
      requested_at: ago(1),
      expires_at: new Date(Date.now() + 9 * 60_000).toISOString(),
    },
    'EXPD-0000': {
      user_code: 'EXPD-0000',
      name: 'old-box',
      os: 'linux',
      arch: 'amd64',
      cli_version: '0.4.0',
      status: 'expired',
      requested_at: ago(40),
      expires_at: ago(30),
    },
  }

  const me = () => session.people[session.get()] ?? null
  const body = async <T>(request: Request): Promise<T> => (await request.json()) as T

  const first = (route: string) => {
    // The console's own start (meta, me) and signing in and out are never failed on purpose.
    const essential = /^GET \/api\/v1\/(meta|me)$|^POST \/api\/v1\/auth\//.test(route)
    if (!fail || essential) return undefined
    if (fail === 'network') throw new TypeError('mock: network down')
    return fail === '403'
      ? problem(403, 'access.denied', 'Not allowed.')
      : problem(500, 'server.error', 'Mock failure.')
  }

  const last = async (request: Request, route: string): Promise<Response | undefined> => {
    const m = (pattern: RegExp) => pattern.exec(route)
    let hit: RegExpExecArray | null

    // ---- sign-in
    if (route === 'POST /api/v1/auth/login') {
      const b = await body<Schemas['LoginRequest']>(request)
      const who = { admin: 'admin', owner: 'owner', jdoe: 'member', wyan: 'pending' }[b.username]
      if (b.password === 'locked') return problem(429, 'auth.locked', 'Too many failed attempts.')
      if (!who || b.password === 'wrong')
        return problem(401, 'auth.invalid_credentials', 'Wrong account or password.')
      session.set(who)
      return json(200, me())
    }
    if ((hit = m(/^GET \/api\/v1\/auth\/invites\/([^/]+)$/))) {
      if (hit[1]?.startsWith('expired')) return problem(410, 'invite.expired', 'This invitation has expired.')
      return json(200, {
        email: 'new.colleague@example.com',
        products: [shop],
        expires_at: invites[0]?.expires_at ?? ago(-7 * 24 * 60),
      })
    }
    if ((hit = m(/^POST \/api\/v1\/auth\/invites\/([^/]+)\/accept$/))) {
      const b = await body<Schemas['AcceptInviteRequest']>(request)
      if (b.username === 'jdoe' || b.username === 'admin')
        return problem(409, 'user.username_taken', 'Taken.')
      const person: Schemas['Me'] = {
        id: id(30),
        username: b.username,
        display_name: b.display_name,
        email: 'new.colleague@example.com',
        status: 'active',
        platform_admin: false,
        focus_stages: [],
        locale: '',
        memberships: [{ product: shop, role: 'member' }],
      }
      session.people.invited = person
      session.set('invited')
      return json(201, person)
    }

    // ---- setup
    if (route === 'GET /api/v1/setup') {
      if (setup === 'none') return problem(404, 'setup.closed', 'Setup is complete.')
      return json(200, { admin_created: setup === 'admin', setup_token_required: true })
    }
    if (route === 'POST /api/v1/setup/admin') {
      if (setup === 'none') return problem(404, 'setup.closed', 'Setup is complete.')
      if (setup === 'admin') return problem(409, 'setup.admin_exists', 'Already created.')
      if (request.headers.get('X-Setup-Token') === 'wrong')
        return problem(403, 'setup.token_invalid', 'Wrong token.')
      const b = await body<Schemas['SetupAdminRequest']>(request)
      setup = 'admin'
      sessionStorage.setItem('mock-setup', setup)
      users = [
        user(1, b.username, 'Local administrator', {
          platform_admin: true,
          identities: ['local'],
          job: undefined,
          focus_stages: [],
        }),
      ]
      products = []
      session.people.admin = {
        id: id(1),
        username: b.username,
        display_name: 'Local administrator',
        status: 'active',
        platform_admin: true,
        focus_stages: [],
        locale: '',
        memberships: [],
      }
      session.set('admin')
      return json(201, me())
    }
    if (route === 'POST /api/v1/setup/complete') {
      setup = 'none'
      sessionStorage.removeItem('mock-setup')
      return none()
    }

    // ---- me: applying
    if (route === 'PATCH /api/v1/me') {
      const person = me()
      if (!person) return problem(401, 'auth.required')
      Object.assign(person, await body<Schemas['UpdateMeRequest']>(request))
      return json(200, person)
    }
    if (route === 'GET /api/v1/me/join-requests') return json(200, { items: myRequests })
    if (route === 'POST /api/v1/me/join-requests') {
      const person = me()
      if (!person) return problem(401, 'auth.required')
      const b = await body<Schemas['CreateJoinRequestsRequest']>(request)
      const made = b.products.map((code, i): Schemas['JoinRequest'] => ({
        id: id(600 + myRequests.length + i),
        user: ref(person),
        job: person.job,
        focus_stages: person.focus_stages,
        identity: 'GitLab',
        product: { code, name: products.find((p) => p.code === code)?.name ?? code },
        note: b.note,
        // A request to the payments team is turned down at once, to show what that looks like.
        status: code === 'pay' ? 'rejected' : 'pending',
        reason: code === 'pay' ? 'Please check with your team lead which product to join first' : undefined,
        decided_by: code === 'pay' ? owners.blu : undefined,
        created_at: new Date().toISOString(),
      }))
      myRequests.unshift(...made)
      return json(201, { items: made })
    }

    // ---- shared lists
    if (route === 'GET /api/v1/stages') return json(200, { items: stages(request) })

    // ---- users
    if (route === 'GET /api/v1/users') {
      if (session.get() === 'member' || session.get() === 'pending')
        return problem(403, 'access.denied', 'Not allowed.')
      // ?q= matches the account, the name and the e-mail address (products: adding people, choosing owners).
      const q = (new URL(request.url).searchParams.get('q') ?? '').toLowerCase()
      const hits = users.filter(
        (u) => !q || `${u.username} ${u.display_name} ${u.email ?? ''}`.toLowerCase().includes(q),
      )
      return json(200, { items: hits })
    }
    if ((hit = m(/^PATCH \/api\/v1\/users\/([^/]+)$/))) {
      const b = await body<Schemas['UpdateUserRequest']>(request)
      const u = users.find((x) => x.id === hit?.[1])
      if (!u) return problem(404, 'object.not_found')
      const admins = users.filter((x) => x.platform_admin && x.status === 'active' && x.id !== u.id).length
      if ((b.platform_admin === false || b.status === 'disabled') && u.platform_admin && admins === 0) {
        return problem(409, 'user.last_admin', 'The platform needs at least one administrator.')
      }
      const saved = { ...u, ...b }
      users = users.map((x) => (x.id === u.id ? saved : x))
      return json(200, saved)
    }
    if (route === 'GET /api/v1/invites') {
      if (session.get() !== 'admin') return problem(403, 'access.denied', 'Not allowed.')
      return json(200, { items: invites })
    }
    if (route === 'POST /api/v1/invites') {
      const b = await body<Schemas['CreateInvitesRequest']>(request)
      if (b.emails.some((e) => users.some((u) => u.email === e)))
        return problem(409, 'invite.account_exists', 'Has an account.')
      // Mail is not configured in the mock: the links come back to be passed on by hand.
      const made = b.emails.map((email, i): Schemas['Invite'] => ({
        id: id(950 + invites.length + i),
        email,
        products: products
          .filter((p) => b.products?.includes(p.code))
          .map((p) => ({ code: p.code, name: p.name })),
        created_by: owners.admin,
        created_at: new Date().toISOString(),
        expires_at: new Date(Date.now() + 7 * 24 * 3600_000).toISOString(),
        link: `${location.origin}/invite/mock-invitation-${String(950 + invites.length + i)}`,
      }))
      invites = [...made.map((inv) => ({ ...inv, link: undefined })), ...invites]
      return json(201, { items: made })
    }
    if ((hit = m(/^DELETE \/api\/v1\/invites\/([^/]+)$/))) {
      invites = invites.filter((x) => x.id !== hit?.[1])
      return none()
    }

    // ---- sign-in methods
    if (route === 'GET /api/v1/identity-providers/kinds') return json(200, { items: KINDS })
    if (route === 'GET /api/v1/identity-providers') {
      if (session.get() !== 'admin') return problem(403, 'access.denied', 'Not allowed.')
      return json(200, { items: setup === 'none' ? methods : methods.filter((x) => x.kind === 'local') })
    }
    if (route === 'POST /api/v1/identity-providers') {
      const b = await body<Schemas['CreateIdentityProviderRequest']>(request)
      const made: Schemas['IdentityProvider'] = {
        id: id(110 + methods.length),
        kind: b.kind,
        name: b.name,
        enabled: b.enabled ?? true,
        config: b.config,
        has_client_secret: !!b.client_secret,
        etag: '"p-new-1"',
      }
      methods = [made, ...methods]
      return json(201, made)
    }
    if ((hit = m(/^PATCH \/api\/v1\/identity-providers\/([^/]+)$/))) {
      const p = methods.find((x) => x.id === hit?.[1])
      if (!p) return problem(404, 'object.not_found')
      if (request.headers.get('If-Match') !== p.etag)
        return problem(412, 'precondition.failed', 'Changed meanwhile.')
      const b = await body<Schemas['UpdateIdentityProviderRequest']>(request)
      if (b.enabled === false && methods.filter((x) => x.enabled && x.id !== p.id).length === 0) {
        return problem(409, 'identity.last_method', 'At least one method stays on.')
      }
      const n = Number(/-(\d+)"$/.exec(p.etag)?.[1] ?? '1') + 1
      const saved = {
        ...p,
        enabled: b.enabled ?? p.enabled,
        has_client_secret: p.has_client_secret || !!b.client_secret,
        etag: `"p-${p.id}-${n}"`,
      }
      methods = methods.map((x) => (x.id === p.id ? saved : x))
      return json(200, saved)
    }

    // ---- device confirmation
    if ((hit = m(/^GET \/api\/v1\/device\/requests\/([^/]+)$/))) {
      const d = devices[hit[1] ?? '']
      return d ? json(200, d) : problem(404, 'object.not_found', 'No such request.')
    }
    if ((hit = m(/^POST \/api\/v1\/device\/requests\/([^/]+)\/(approve|deny)$/))) {
      const d = devices[hit[1] ?? '']
      if (!d || d.status !== 'pending') return problem(404, 'object.not_found', 'No such request.')
      d.status = hit[2] === 'approve' ? 'approved' : 'denied'
      return none()
    }
    return undefined
  }
  return { first, last }
}

/** patchMeta adjusts /api/v1/meta for these pages: the sign-in methods, and an unfinished setup. */
export function patchMeta(meta: Schemas['ServerMeta']): Schemas['ServerMeta'] {
  const setup = sessionStorage.getItem('mock-setup')
  return {
    ...meta,
    setup_complete: setup !== 'open' && setup !== 'admin',
    login_methods:
      setup === 'open' || setup === 'admin'
        ? [{ id: 'local', kind: 'local', name: 'Local account' }]
        : [
            { id: id(101), kind: 'oidc', name: 'Company SSO' },
            { id: id(102), kind: 'gitlab', name: 'GitLab' },
            { id: 'local', kind: 'local', name: 'Local account' },
          ],
  }
}
