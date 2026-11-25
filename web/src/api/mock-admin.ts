// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The development stand-in (mock.ts) for the management and system pages: products and members, join
// requests, resources and their drafts and versions, resource repositories, settings, modules, tool adapters,
// command-line versions and the audit log. The data follows the examples in api/openapi.yaml and lives in
// memory, so changes made on the pages stick until the page is reloaded. Only development builds include it.
//
// The people are mock-auth.ts's (same ids and names), so the users page and these pages agree. This file is
// the only answer for its routes: products and members, join requests, sources, modules and adapters are
// not answered by the other mocks. Failures (?mockfail=403|500|network) come from mock-auth.ts before any
// mock answers; ?mockempty=1, and the first-run wizard (?mock=setup…), start without products, requests,
// resources or repositories.

import type { Schemas } from './client'

type S = Schemas
/** Who is signed in; pending is someone not yet in any product (they may list products to apply). */
type Who = 'admin' | 'owner' | 'member' | 'pending'

const id = (n: number) => `0193a4b2-6c1e-7a00-8000-${String(n).padStart(12, '0')}`

const person = (n: number, username: string, display_name: string): S['UserRef'] => ({
  id: id(n),
  username,
  display_name,
})

const P = {
  admin: person(1, 'admin', 'Alex Admin'),
  owner: person(2, 'owner', 'Olive Owner'),
  jdoe: person(11, 'jdoe', 'Jamie Doe'),
  wyan: person(12, 'wyan', 'Wen Yan'),
  gyuan: person(13, 'gyuan', 'Gu Yuan'),
  blu: person(14, 'blu', 'Bai Lu'),
  yzhou: person(15, 'yzhou', 'Ye Zhou'),
  sshen: person(16, 'sshen', 'Sam Shen'),
  cyuan: person(17, 'cyuan', 'Chen Yuan'),
  qshao: person(19, 'qshao', 'Quinn Shao'),
}

const now = Date.now()
const ago = (minutes: number) => new Date(now - minutes * 60_000).toISOString()

interface MemberRow {
  user: S['UserRef']
  role: S['Role']
  job: string
  focus: string[]
  added: number
}

interface ProductRow {
  code: string
  name: string
  line: string
  members: MemberRow[]
  extra: number // people not listed, so the counts look like a real organisation
}

const m = (user: S['UserRef'], role: S['Role'], job: string, focus: string[], added: number): MemberRow => ({
  user,
  role,
  job,
  focus,
  added,
})

function seedProducts(): ProductRow[] {
  return [
    {
      code: 'shop',
      name: 'Shop',
      line: 'Commerce',
      extra: 58,
      members: [
        m(P.admin, 'owner', 'Developer', ['design', 'develop'], 90_000),
        m(P.gyuan, 'owner', 'Developer', ['design', 'develop'], 80_000),
        m(P.owner, 'member', 'Tester', ['test'], 60_000),
        m(P.jdoe, 'member', 'Developer', ['develop'], 40_000),
        m(P.sshen, 'member', 'Operations', ['release', 'ops'], 30_000),
      ],
    },
    {
      code: 'pay',
      name: 'Payments',
      line: 'Commerce',
      extra: 34,
      members: [
        m(P.owner, 'owner', 'Tester', ['test'], 70_000),
        m(P.blu, 'owner', 'Developer', ['develop'], 65_000),
        m(P.cyuan, 'member', 'Developer', ['develop', 'test'], 10_000),
      ],
    },
    {
      code: 'app',
      name: 'Mobile',
      line: 'Users',
      extra: 41,
      members: [m(P.yzhou, 'owner', 'Developer', ['develop'], 50_000)],
    },
    { code: 'data', name: 'Data platform', line: 'Data', extra: 0, members: [] },
  ]
}

function seedRequests(): S['JoinRequest'][] {
  return [
    {
      id: id(501),
      user: P.wyan,
      job: 'Developer',
      focus_stages: ['design', 'develop'],
      identity: 'GitLab',
      product: { code: 'shop', name: 'Shop' },
      note: 'Joining the shop backend team',
      status: 'pending',
      created_at: ago(95),
    },
    {
      id: id(502),
      user: P.wyan,
      job: 'Developer',
      focus_stages: ['design', 'develop'],
      identity: 'GitLab',
      product: { code: 'pay', name: 'Payments' },
      note: 'Also helping out with payments',
      status: 'pending',
      created_at: ago(94),
    },
    {
      id: id(503),
      user: P.qshao,
      job: 'Tester',
      focus_stages: ['test'],
      identity: 'Single sign-on',
      product: { code: 'pay', name: 'Payments' },
      status: 'pending',
      created_at: ago(60 * 20),
    },
  ]
}

interface ResourceRow {
  res: S['ManagedResource']
  draft?: S['Draft']
  versions: S['ResourceVersion'][]
  definition?: Record<string, unknown>
  n: number // etag counter
}

const finding = (
  rule: string,
  severity: S['Severity'],
  message: string,
  file?: string,
  line?: number,
): S['Finding'] => ({
  id: `f-${rule}-${(rule.length * 7919 + (line ?? 0) * 31 + message.length).toString(16).padStart(8, '0')}`,
  rule,
  severity,
  message,
  file,
  line,
  first_seen_at: ago(60 * 24 * 9),
})

const digest = (seed: string) => {
  let h = ''
  for (let i = 0; h.length < 64; i++) h += ((seed.charCodeAt(i % seed.length) * (i + 7)) % 16).toString(16)
  return `sha256:${h}`
}

function resource(
  r: Partial<S['ManagedResource']> & Pick<S['ManagedResource'], 'code' | 'kind' | 'name' | 'origin'>,
  extra: { draft?: S['Finding'][] | true; versions?: number; definition?: Record<string, unknown> } = {},
): ResourceRow {
  const versions: S['ResourceVersion'][] = []
  const count = extra.versions ?? (r.status === 'draft' ? 0 : 3)
  const notes = [
    'Covers the case of a missing template',
    'Pulled from the repository',
    'Clearer trigger',
    'First release',
  ]
  for (let v = count; v >= 1; v--) {
    versions.push({
      number: v,
      digest: digest(r.code + v),
      bundle: digest(v + r.code),
      note: notes[Math.min(count - v, notes.length - 1)],
      ruleset: 1,
      checks: { blocking: 0, advisory: v === count ? 1 : 0 },
      published_by: v % 2 ? P.gyuan : P.admin,
      published_at: ago(60 * 24 * (count - v) * 9 + 120),
    })
  }
  const findings = extra.draft === true ? [] : (extra.draft ?? [])
  const checks = {
    blocking: findings.filter((f) => f.severity === 'must_fix' && !f.waiver).length,
    advisory: findings.filter((f) => f.severity === 'advisory').length,
  }
  const res: S['ManagedResource'] = {
    stages: [],
    audience: 'rd',
    scope: 'org',
    policy: 'recommended',
    status: count ? 'published' : 'draft',
    has_draft: extra.draft !== undefined,
    checks: extra.draft !== undefined ? checks : { blocking: 0, advisory: count ? 1 : 0 },
    updated_at: ago(300),
    maintainers: [P.gyuan],
    overrides: [],
    secrets: [],
    references: [],
    created_at: ago(60 * 24 * 80),
    etag: '',
    ...(count ? { version: count, published_at: versions[0]?.published_at } : {}),
    ...r,
  }
  const row: ResourceRow = { res, versions, definition: extra.definition, n: 1 }
  if (extra.draft !== undefined) {
    row.draft = {
      code: r.code,
      kind: r.kind,
      digest: digest('draft' + r.code),
      meta: { name: r.name, summary: r.summary, stages: res.stages, scope: res.scope, policy: res.policy },
      definition: extra.definition ?? {},
      files:
        r.kind === 'skill' || r.kind === 'agent'
          ? [
              { path: 'SKILL.md', size: 2210 },
              { path: 'meta.yaml', size: 214 },
            ]
          : [],
      checks: { ruleset: 1, ...checks, findings },
      base_version: res.version,
      updated_by: r.origin === 'repo' ? undefined : P.admin,
      updated_at: ago(240),
      etag: '',
    }
  }
  return row
}

const repo = (path: string): S['ResourceSource'] => ({
  id: id(701),
  name: 'harness-resources',
  path,
  commit: '3f2a1b0c9d8e7f6a5b4c3d2e1f0a9b8c7d6e5f4a',
})

function seedResources(): ResourceRow[] {
  return [
    resource(
      {
        code: 'jira',
        kind: 'mcp',
        name: 'Jira',
        summary: 'Reads and writes issues and sprints.',
        origin: 'web',
        stages: ['requirement', 'develop', 'test'],
        policy: 'required',
        secrets: ['JIRA_API_TOKEN'],
        delivery: 'native',
      },
      {
        versions: 4,
        definition: {
          transport: 'http',
          url: 'https://jira-mcp.example.com/mcp',
          headers: { Authorization: 'Bearer ${secret:JIRA_API_TOKEN}' },
          delivery: 'native',
        },
      },
    ),
    resource(
      {
        code: 'gitlab',
        kind: 'mcp',
        name: 'GitLab',
        summary: 'Merge requests, pipelines and code search.',
        origin: 'web',
        stages: ['develop', 'scm', 'release'],
        policy: 'required',
        secrets: ['GITLAB_TOKEN'],
        delivery: 'on_demand',
      },
      {
        versions: 2,
        definition: {
          transport: 'http',
          url: 'https://gitlab-mcp.example.com/mcp',
          headers: { 'PRIVATE-TOKEN': '${secret:GITLAB_TOKEN}' },
          delivery: 'on_demand',
          tools: ['list_merge_requests', 'get_pipeline'],
        },
      },
    ),
    resource(
      {
        code: 'design-doc',
        kind: 'skill',
        name: 'Design document',
        summary: 'Writes or updates the design document from the template.',
        origin: 'repo',
        stages: ['design'],
        overrides: ['policy'],
        repository_differs: ['policy'],
        when: 'When a design document has to be written or updated',
        source: repo('skills/design-doc'),
      },
      {
        versions: 9,
        draft: [
          finding(
            'overlap',
            'advisory',
            'The description overlaps with functional-design; one sentence may select both.',
            'SKILL.md',
            3,
          ),
          finding('description', 'advisory', 'The description does not say when to use it.', 'SKILL.md', 2),
        ],
      },
    ),
    resource(
      {
        code: 'requirement-analysis',
        kind: 'skill',
        name: 'Requirement analysis',
        summary: 'Turns a one-line request into user stories and acceptance criteria.',
        origin: 'repo',
        stages: ['requirement'],
        policy: 'recommended',
        source: repo('skills/requirement-analysis'),
      },
      { versions: 7 },
    ),
    resource(
      {
        code: 'shop-config-check',
        kind: 'skill',
        name: 'Config check',
        summary: 'Checks shop configuration changes before they are merged.',
        origin: 'upload',
        scope: 'shop',
        policy: 'optional',
        maintainers: [P.jdoe],
      },
      {
        versions: 0,
        draft: [
          finding('secrets', 'must_fix', 'Looks like a password: password=… in an example.', 'SKILL.md', 41),
          finding(
            'paths',
            'must_fix',
            'An absolute path from someone’s computer: /Users/…',
            'scripts/check.sh',
            7,
          ),
          finding('description', 'advisory', 'The description is too short.', 'SKILL.md', 2),
        ],
      },
    ),
    resource(
      {
        code: 'task-to-submit',
        kind: 'workflow',
        name: 'From task to submission',
        summary: 'Design, code, test and open a merge request, step by step.',
        origin: 'repo',
        stages: ['design', 'develop', 'test'],
        source: repo('workflows/task-to-submit.yaml'),
        references: [{ code: 'design-doc', kind: 'skill', name: 'Design document' }],
      },
      { versions: 3 },
    ),
    resource(
      {
        code: 'code-reviewer',
        kind: 'agent',
        name: 'Code reviewer',
        summary: 'Reviews a change against the team’s rules before it is submitted.',
        origin: 'repo',
        stages: ['develop'],
        policy: 'optional',
        source: repo('agents/code-reviewer'),
      },
      { versions: 2 },
    ),
    resource(
      {
        code: 'pay-rules',
        kind: 'knowledge',
        name: 'Payment rules',
        summary: 'Settlement and refund rules of the payments team.',
        origin: 'web',
        scope: 'pay',
        audience: 'mgmt',
        maintainers: [P.blu],
      },
      { versions: 1, definition: { repo: 'pay/handbook', dir: 'docs/rules' } },
    ),
    resource(
      {
        code: 'old-lint',
        kind: 'skill',
        name: 'Old lint rules',
        summary: 'Replaced by the code reviewer.',
        origin: 'repo',
        stages: ['develop'],
        status: 'disabled',
        disabled_reason: 'Replaced by the code reviewer',
        source: repo('skills/old-lint'),
      },
      { versions: 5 },
    ),
  ]
}

function seedSources(): S['Source'][] {
  return [
    {
      id: id(701),
      name: 'harness-resources',
      url: 'https://gitlab.example.com/platform/harness-resources.git',
      branch: 'main',
      dir: '',
      order: 1,
      has_credential: true,
      resource_count: 41,
      last_pull: {
        at: ago(200),
        result: 'ok',
        commit: '3f2a1b0c9d8e7f6a5b4c3d2e1f0a9b8c7d6e5f4a',
        imported: 1,
        failed: 0,
        last_success_at: ago(200),
      },
      etag: '"s-701-1"',
    },
    {
      id: id(702),
      name: 'shop-team-skills',
      url: 'git@gitlab.example.com:shop/ai-skills.git',
      branch: 'main',
      dir: 'rigdepot',
      order: 2,
      has_credential: true,
      resource_count: 6,
      last_pull: {
        at: ago(15),
        result: 'failed',
        error: 'authentication failed: the token was revoked',
        last_success_at: ago(60 * 26),
      },
      etag: '"s-702-1"',
    },
  ]
}

const ADAPTERS: S['Adapter'][] = [
  {
    id: 'claude-code',
    name: 'Claude Code',
    verified: { version: '2.1.330', at: '2026-11-24' },
    enabled: true,
    people: 164,
    targets: {
      skill: { location: '~/.claude/skills/' },
      mcp: { location: '~/.claude.json', detail: 'mcpServers' },
      command: { location: '~/.claude/commands/' },
      agent: { location: '~/.claude/agents/' },
    },
  },
  {
    id: 'codex',
    name: 'Codex',
    verified: { version: '0.172.0', at: '2026-11-25' },
    enabled: true,
    people: 71,
    targets: {
      skill: { location: '~/.agents/skills/' },
      mcp: { location: '~/.codex/config.toml', detail: 'mcp_servers' },
      command: { location: '~/.codex/prompts/' },
      agent: { fallback: 'skill' },
    },
  },
  {
    id: 'opencode',
    name: 'OpenCode',
    verified: { version: '2.4.1', at: '2026-11-24' },
    enabled: true,
    people: 23,
    targets: {
      skill: { location: '~/.config/opencode/skills/' },
      mcp: { location: '~/.config/opencode/opencode.json', detail: 'mcp.servers' },
      command: { location: '~/.config/opencode/commands/' },
      agent: { location: '~/.config/opencode/agents/' },
    },
  },
  {
    id: 'gemini-cli',
    name: 'Gemini CLI',
    enabled: false,
    people: 0,
    targets: {
      skill: {},
      mcp: { location: '~/.gemini/settings.json', detail: 'mcpServers' },
      command: { location: '~/.gemini/commands/' },
      agent: {},
    },
  },
]

function auditSeed(): S['AuditEntry'][] {
  const rows: S['AuditEntry'][] = []
  let seq = 1900
  const add = (minutes: number, e: Omit<S['AuditEntry'], 'seq' | 'at'>) =>
    rows.push({ seq: seq--, at: ago(minutes), ...e })
  add(30, {
    category: 'platform',
    actor: P.gyuan,
    origin: 'cli',
    via_tool: 'claude-code',
    device: { id: id(202), name: 'gyuan-laptop' },
    action: 'resource.publish',
    action_label: 'Publish resource',
    object: { kind: 'resource', id: 'design-doc', label: 'Design document' },
    change: { version: { from: 8, to: 9 }, files_changed: 1 },
    result: 'ok',
  })
  add(40, {
    category: 'platform',
    actor: P.blu,
    origin: 'web',
    action: 'product.member_add',
    action_label: 'Add member',
    object: { kind: 'product', id: 'pay', label: 'Payments' },
    change: { user: 'cyuan', role: 'member' },
    result: 'ok',
  })
  add(51, {
    category: 'platform',
    actor: P.jdoe,
    origin: 'cli',
    via_tool: 'claude-code',
    device: { id: id(201), name: 'dev-laptop' },
    action: 'resource.publish',
    action_label: 'Publish resource',
    object: { kind: 'resource', id: 'task-to-submit', label: 'From task to submission' },
    change: {},
    result: 'denied',
    reason: 'not a maintainer of this resource',
  })
  add(58, {
    category: 'platform',
    actor: P.jdoe,
    origin: 'web',
    action: 'secret.replace',
    action_label: 'Replace personal secret',
    object: { kind: 'secret', id: 'JIRA_API_TOKEN', label: 'JIRA_API_TOKEN' },
    change: { value: 'replaced' },
    result: 'ok',
  })
  add(90, {
    category: 'platform',
    origin: 'system',
    action: 'source.import',
    action_label: 'Import draft',
    object: { kind: 'resource', id: 'design-doc', label: 'Design document' },
    change: { source: 'harness-resources', files_changed: 3 },
    result: 'ok',
  })
  add(60 * 20, {
    category: 'platform',
    actor: P.admin,
    origin: 'web',
    action: 'resource.update',
    action_label: 'Change resource',
    object: { kind: 'resource', id: 'gitlab', label: 'GitLab' },
    change: { delivery: { from: 'native', to: 'on_demand' } },
    result: 'ok',
  })
  add(60 * 22, {
    category: 'platform',
    actor: P.admin,
    origin: 'web',
    action: 'settings.cli.min_version',
    action_label: 'Change minimum command-line version',
    object: { kind: 'settings', id: 'cli', label: 'Command line' },
    change: { min_version: { from: '0.3.0', to: '0.4.0' } },
    result: 'ok',
  })
  add(12, {
    category: 'login',
    actor: P.jdoe,
    origin: 'cli',
    device: { id: id(201), name: 'dev-laptop' },
    method: 'device_code',
    action: 'auth.login',
    action_label: 'Sign in',
    object: { kind: 'user', id: id(11), label: 'jdoe' },
    change: {},
    result: 'ok',
  })
  add(41, {
    category: 'login',
    actor: P.admin,
    origin: 'web',
    method: 'oidc',
    action: 'auth.login',
    action_label: 'Sign in',
    object: { kind: 'user', id: id(1), label: 'admin' },
    change: {},
    result: 'ok',
  })
  add(42, {
    category: 'login',
    origin: 'web',
    method: 'local',
    action: 'auth.login',
    action_label: 'Sign in',
    object: { kind: 'user', id: id(17), label: 'cyuan' },
    change: { failed_attempts: 3 },
    result: 'denied',
    reason: 'wrong password',
  })
  add(60 * 18, {
    category: 'login',
    actor: P.owner,
    origin: 'cli',
    method: 'access_token',
    action: 'auth.login',
    action_label: 'Sign in',
    object: { kind: 'user', id: id(2), label: 'owner' },
    change: {},
    result: 'denied',
    reason: 'the token was revoked',
  })
  // Older entries, so that the list has more than one page.
  for (let i = 0; i < 64; i++) {
    add(60 * 24 * 2 + i * 97, {
      category: 'platform',
      actor: i % 3 ? P.gyuan : P.blu,
      origin: i % 2 ? 'web' : 'cli',
      via_tool: i % 2 ? undefined : 'codex',
      action: 'resource.update',
      action_label: 'Change resource',
      object: { kind: 'resource', id: 'requirement-analysis', label: 'Requirement analysis' },
      change: { stages: { from: ['requirement'], to: ['requirement', 'design'] } },
      result: 'ok',
    })
  }
  return rows.sort((a, b) => b.seq - a.seq)
}

// ---------------------------------------------------------------- state

function startDB(empty: boolean) {
  return {
    products: empty ? [] : seedProducts(),
    requests: empty ? [] : seedRequests(),
    resources: empty ? [] : seedResources(),
    sources: empty ? [] : seedSources(),
    settings: {
      organization: { name: 'Example Corp' },
      mail: {
        enabled: true,
        host: 'smtp.example.com',
        port: 465,
        security: 'tls',
        username: 'hub@example.com',
        has_password: true,
        from: 'hub@example.com',
      },
      etag: '"settings-1"',
    } as S['PlatformSettings'],
    modules: [
      {
        id: 'insights',
        name: 'Usage insights',
        description:
          'Counts, outcomes and durations of resource use. Never prompts, code, arguments or results.',
        enabled: true,
        notice:
          'Computers start reporting usage metadata at their next sync. Each person can still turn it off on their own computer.',
        locked: false,
      },
      {
        id: 'env-resources',
        name: 'Environment resources',
        description: 'Databases, servers and test environments handed to the people they are granted to.',
        enabled: false,
        notice: 'Connection details go to the computers of the people you grant them to.',
        locked: true,
      },
    ] as S['Module'][],
    adapters: ADAPTERS.map((a) => ({ ...a })),
    cli: {
      min_version: '0.4.0',
      latest_version: '0.5.0',
      min_version_floor: '0.4.0',
      distribution: [
        { version: '0.5.0', people: 171 },
        { version: '0.4.2', people: 16 },
      ],
    } as S['CLIPolicy'],
    audit: auditSeed(),
  }
}

let db = startDB(false)
let settingsN = 1

// ---------------------------------------------------------------- helpers

const json = (status: number, body: unknown, etag?: string) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...(etag ? { ETag: etag } : {}) },
  })

const problem = (status: number, code: string, detail?: string, params?: Record<string, unknown>) =>
  new Response(
    JSON.stringify({
      type: 'about:blank',
      title: '',
      status,
      code,
      detail,
      params,
      request_id: '01JDV6ZQ8M3K',
    }),
    { status, headers: { 'Content-Type': 'application/problem+json' } },
  )

const denied = () => problem(403, 'access.denied', 'You are not allowed to do this.')
const notFound = () => problem(404, 'object.not_found', 'Not there, or not visible to you.')
const noContent = () => new Response(null, { status: 204 })

async function body<T>(request: Request): Promise<T> {
  return (await request.json()) as T
}

function etagOf(row: ResourceRow) {
  return `"r-${row.res.code}-${row.n}"`
}

function managed(row: ResourceRow): S['ManagedResource'] {
  return { ...row.res, etag: etagOf(row) }
}

function summary(row: ResourceRow): S['ResourceSummary'] {
  const r = row.res
  return {
    code: r.code,
    kind: r.kind,
    name: r.name,
    summary: r.summary,
    stages: r.stages,
    audience: r.audience,
    scope: r.scope,
    policy: r.policy,
    origin: r.origin,
    status: r.status,
    version: r.version,
    has_draft: r.has_draft,
    checks: r.checks,
    updated_at: r.updated_at,
  }
}

function draftDoc(row: ResourceRow): S['Draft'] | undefined {
  if (!row.draft) return undefined
  return { ...row.draft, etag: `"${row.draft.digest}"` }
}

function product(row: ProductRow, who: Who): S['Product'] {
  const me = meOf(who)
  const mine = row.members.find((x) => x.user.id === me?.id)
  return {
    code: row.code,
    name: row.name,
    line: row.line,
    owners: row.members.filter((x) => x.role === 'owner').map((x) => x.user),
    member_count: row.members.length + row.extra,
    resource_count: db.resources.filter((r) => r.res.scope === row.code).length,
    my_role: mine?.role,
  }
}

function meOf(who: Who): S['UserRef'] | undefined {
  return who === 'admin' ? P.admin : who === 'owner' ? P.owner : who === 'member' ? P.jdoe : P.wyan
}

function owns(who: Who, code: string): boolean {
  if (who === 'admin') return true
  const me = meOf(who)
  return !!db.products
    .find((p) => p.code === code)
    ?.members.some((x) => x.user.id === me?.id && x.role === 'owner')
}

function audited(e: Omit<S['AuditEntry'], 'seq' | 'at' | 'category' | 'origin'>, who: Who) {
  db.audit.unshift({
    seq: (db.audit[0]?.seq ?? 0) + 1,
    at: new Date().toISOString(),
    category: 'platform',
    origin: 'web',
    actor: meOf(who),
    ...e,
  })
}

// ---------------------------------------------------------------- routes

type Handler = (request: Request, url: URL, who: Who, match: RegExpMatchArray) => Promise<Response> | Response

const routes: [string, RegExp, Handler][] = [
  // ------------------------------------------------------------ products
  [
    'GET',
    /^\/api\/v1\/products$/,
    (_r, _u, who) => json(200, { items: db.products.map((p) => product(p, who)) }),
  ],
  [
    'POST',
    /^\/api\/v1\/products$/,
    async (request, _u, who) => {
      if (who !== 'admin') return denied()
      const req = await body<S['CreateProductRequest']>(request)
      if (['org', 'me', 'sync', 'status', 'login', 'check'].includes(req.code))
        return problem(
          409,
          'product.code_reserved',
          `"${req.code}" is a command name and cannot be a product code.`,
          { code: req.code },
        )
      if (db.products.some((p) => p.code === req.code))
        return problem(409, 'product.code_taken', `"${req.code}" is taken.`, { code: req.code })
      const all = Object.values(P)
      const row: ProductRow = {
        code: req.code,
        name: req.name,
        line: req.line ?? '',
        extra: 0,
        members: (req.owners ?? []).flatMap((uid) => {
          const u = all.find((x) => x.id === uid)
          return u ? [m(u, 'owner', '', [], 0)] : []
        }),
      }
      db.products.push(row)
      audited(
        {
          action: 'product.create',
          action_label: 'Create product',
          object: { kind: 'product', id: row.code, label: row.name },
          change: {},
          result: 'ok',
        },
        who,
      )
      return json(201, product(row, who))
    },
  ],
  [
    'GET',
    /^\/api\/v1\/products\/([^/]+)\/members$/,
    (_r, _u, _who, [, code]) => {
      const p = db.products.find((x) => x.code === code)
      if (!p) return notFound()
      const items = [...p.members]
        .sort((a, b) =>
          a.role === b.role
            ? a.user.display_name.localeCompare(b.user.display_name)
            : a.role === 'owner'
              ? -1
              : 1,
        )
        .map((x) => ({
          user: x.user,
          role: x.role,
          job: x.job,
          focus_stages: x.focus,
          added_at: ago(x.added),
        }))
      return json(200, { items })
    },
  ],
  [
    'PUT',
    /^\/api\/v1\/products\/([^/]+)\/members\/([^/]+)$/,
    async (request, _u, who, [, code, uid]) => {
      const p = db.products.find((x) => x.code === code)
      if (!p) return notFound()
      if (!owns(who, p.code)) return denied()
      const { role } = await body<S['PutMemberRequest']>(request)
      const existing = p.members.find((x) => x.user.id === uid)
      if (existing) {
        if (
          existing.role === 'owner' &&
          role === 'member' &&
          p.members.filter((x) => x.role === 'owner').length === 1
        )
          return problem(409, 'product.last_owner', 'A product needs at least one owner.', { product: code })
        existing.role = role
        return json(200, { user: existing.user, role, job: existing.job, added_at: ago(existing.added) })
      }
      const u = Object.values(P).find((x) => x.id === uid)
      if (!u) return notFound()
      p.members.push(m(u, role, '', [], 0))
      db.requests = db.requests.filter((q) => !(q.user.id === uid && q.product.code === code))
      audited(
        {
          action: 'product.member_add',
          action_label: 'Add member',
          object: { kind: 'product', id: p.code, label: p.name },
          change: { user: u.username, role },
          result: 'ok',
        },
        who,
      )
      return json(201, { user: u, role, added_at: new Date().toISOString() })
    },
  ],
  [
    'DELETE',
    /^\/api\/v1\/products\/([^/]+)\/members\/([^/]+)$/,
    (_r, _u, who, [, code, uid]) => {
      const p = db.products.find((x) => x.code === code)
      if (!p) return notFound()
      if (!owns(who, p.code)) return denied()
      const x = p.members.find((y) => y.user.id === uid)
      if (x?.role === 'owner' && p.members.filter((y) => y.role === 'owner').length === 1)
        return problem(409, 'product.last_owner', 'A product needs at least one owner.', { product: code })
      p.members = p.members.filter((y) => y.user.id !== uid)
      return noContent()
    },
  ],
  [
    'GET',
    /^\/api\/v1\/join-requests$/,
    (_r, _u, who) =>
      json(200, { items: db.requests.filter((q) => q.status === 'pending' && owns(who, q.product.code)) }),
  ],
  [
    'POST',
    /^\/api\/v1\/join-requests\/([^/]+)\/(approve|reject)$/,
    async (request, _u, who, [, rid, verb]) => {
      const q = db.requests.find((x) => x.id === rid)
      if (!q) return notFound()
      if (!owns(who, q.product.code)) return denied()
      if (q.status !== 'pending')
        return problem(409, 'join_request.decided', 'Someone already decided on this request.')
      if (verb === 'reject') {
        const { reason } = await body<S['ReasonRequest']>(request)
        Object.assign(q, { status: 'rejected', reason })
      } else {
        Object.assign(q, { status: 'approved' })
        const p = db.products.find((x) => x.code === q.product.code)
        if (p && !p.members.some((x) => x.user.id === q.user.id))
          p.members.push(m(q.user, 'member', q.job ?? '', q.focus_stages ?? [], 0))
      }
      return json(200, { ...q, decided_by: meOf(who), decided_at: new Date().toISOString() })
    },
  ],

  // ------------------------------------------------------------ resources
  [
    'GET',
    /^\/api\/v1\/resources$/,
    (_r, _u, who) => {
      const rows = db.resources.filter(
        (r) =>
          who === 'admin' ||
          (r.res.scope !== 'org' && owns(who, r.res.scope)) ||
          r.res.maintainers.some((x) => x.id === meOf(who)?.id),
      )
      return json(200, { items: rows.map(summary).sort((a, b) => a.name.localeCompare(b.name)) })
    },
  ],
  [
    'GET',
    /^\/api\/v1\/resources\/([^/]+)$/,
    (_r, _u, _who, [, code]) => {
      const row = db.resources.find((x) => x.res.code === code)
      return row ? json(200, managed(row), etagOf(row)) : notFound()
    },
  ],
  [
    'PATCH',
    /^\/api\/v1\/resources\/([^/]+)$/,
    async (request, _u, who, [, code]) => {
      const row = db.resources.find((x) => x.res.code === code)
      if (!row) return notFound()
      const match = request.headers.get('If-Match')
      if (!match) return problem(428, 'precondition.required', 'Send If-Match with the ETag you saw.')
      if (match !== etagOf(row))
        return problem(412, 'precondition.failed', 'Someone changed this in the meantime.')
      const req = await body<S['UpdateResourceRequest']>(request)
      const { reset, ...rest } = req
      delete rest.maintainers
      const fields = rest
      const changed = Object.keys(fields) as S['OverridableField'][]
      Object.assign(row.res, fields)
      if (row.res.origin === 'repo') {
        row.res.overrides = [
          ...new Set([...row.res.overrides.filter((f) => !reset?.includes(f)), ...changed]),
        ]
        row.res.repository_differs = (row.res.repository_differs ?? []).filter((f) => !reset?.includes(f))
      }
      row.res.updated_at = new Date().toISOString()
      row.n++
      audited(
        {
          action: 'resource.update',
          action_label: 'Change resource',
          object: { kind: 'resource', id: code ?? '', label: row.res.name },
          change: Object.fromEntries(changed.map((f) => [f, (fields as Record<string, unknown>)[f]])),
          result: 'ok',
        },
        who,
      )
      return json(200, managed(row), etagOf(row))
    },
  ],
  [
    'GET',
    /^\/api\/v1\/resources\/([^/]+)\/impact$/,
    (_r, url, _who, [, code]) => {
      const row = db.resources.find((x) => x.res.code === code)
      if (!row) return notFound()
      const scope = url.searchParams.get('scope')
      const people = scope ? (scope === 'org' ? 0 : 58) : row.res.status === 'published' ? 164 : 0
      return json(200, {
        people,
        devices: Math.round(people * 1.27),
        referenced_by: db.resources
          .filter((r) => r.res.references.some((x) => x.code === code))
          .map((r) => ({ code: r.res.code, kind: r.res.kind, name: r.res.name })),
      })
    },
  ],
  [
    'GET',
    /^\/api\/v1\/resources\/([^/]+)\/draft$/,
    (_r, _u, _who, [, code]) => {
      const row = db.resources.find((x) => x.res.code === code)
      const d = row && draftDoc(row)
      return d ? json(200, d, d.etag) : problem(404, 'resource.no_draft', 'There is no draft.')
    },
  ],
  [
    'PUT',
    /^\/api\/v1\/resources\/([^/]+)\/draft$/,
    async (request, _u, who, [, code]) => {
      const req = await body<S['PutDraftRequest']>(request)
      const text = JSON.stringify(req.definition)
      if (
        /(token|password|secret)["']?\s*[:=]\s*["'][^$"'][^"']{6,}/i.test(text) ||
        /Bearer [A-Za-z0-9]{12,}/.test(text)
      )
        return problem(
          400,
          'resource.plaintext_secret',
          'A secret is written in plain text. Write it as ${secret:NAME}.',
        )
      let row = db.resources.find((x) => x.res.code === code)
      if (row?.res.origin === 'repo')
        return problem(
          409,
          'resource.from_repository',
          'This resource comes from a repository. Change it there.',
          { source: 'harness-resources' },
        )
      const created = !row
      if (!row) {
        row = resource(
          {
            code: code ?? '',
            kind: req.kind,
            name: req.meta.name,
            summary: req.meta.summary,
            origin: 'web',
            stages: req.meta.stages ?? [],
            scope: req.meta.scope ?? 'org',
            policy: req.meta.policy ?? 'recommended',
            maintainers: [meOf(who) ?? P.admin],
          },
          { versions: 0, draft: [], definition: req.definition },
        )
        db.resources.push(row)
      } else {
        const d = draftDoc(row)
        const match = request.headers.get('If-Match')
        if (d && match !== d.etag)
          return problem(
            match ? 412 : 428,
            match ? 'precondition.failed' : 'precondition.required',
            'The draft changed in the meantime.',
          )
        row.definition = req.definition
        row.draft = {
          code: row.res.code,
          kind: row.res.kind,
          digest: digest(text + Date.now()),
          meta: req.meta,
          definition: req.definition,
          files: [],
          checks: { ruleset: 1, blocking: 0, advisory: 0, findings: [] },
          base_version: row.res.version,
          updated_by: meOf(who),
          updated_at: new Date().toISOString(),
          etag: '',
        }
        row.res.has_draft = true
        row.res.checks = { blocking: 0, advisory: 0 }
        row.n++
      }
      const d = draftDoc(row)
      return json(created ? 201 : 200, d, d?.etag)
    },
  ],
  [
    'PUT',
    /^\/api\/v1\/resources\/([^/]+)\/draft\/bundle$/,
    async (request, url, who, [, code]) => {
      const bytes = new Uint8Array(await request.arrayBuffer())
      const zip = bytes[0] === 0x50 && bytes[1] === 0x4b
      const gz = bytes[0] === 0x1f && bytes[1] === 0x8b
      if (!zip && !gz && bytes.length < 512)
        return problem(415, 'bundle.unknown_format', 'This is not a zip or tar archive.')
      let row = db.resources.find((x) => x.res.code === code)
      if (row?.res.origin === 'repo')
        return problem(
          409,
          'resource.from_repository',
          'This resource comes from a repository. Change it there.',
          { source: 'harness-resources' },
        )
      const created = !row
      const findings = [
        finding('description', 'advisory', 'The description does not say when to use it.', 'SKILL.md', 2),
      ]
      if (!row) {
        const kind = url.searchParams.get('kind') ?? 'skill'
        row = resource(
          { code: code ?? '', kind, name: code ?? '', origin: 'upload', maintainers: [meOf(who) ?? P.admin] },
          { versions: 0, draft: findings },
        )
        db.resources.push(row)
      } else {
        const prior = row.draft ?? resource({ ...row.res }, { draft: findings }).draft
        if (!prior) return notFound()
        row.draft = {
          ...prior,
          digest: digest(code + String(Date.now())),
          checks: { ruleset: 1, blocking: 0, advisory: 1, findings },
          updated_by: meOf(who),
          updated_at: new Date().toISOString(),
        }
        row.res.has_draft = true
        row.res.checks = { blocking: 0, advisory: 1 }
        row.n++
      }
      const d = draftDoc(row)
      return json(created ? 201 : 200, d, d?.etag)
    },
  ],
  [
    'DELETE',
    /^\/api\/v1\/resources\/([^/]+)\/draft$/,
    (_r, _u, _who, [, code]) => {
      const row = db.resources.find((x) => x.res.code === code)
      if (!row?.draft) return notFound()
      if (row.res.status === 'draft') db.resources = db.resources.filter((x) => x !== row)
      row.draft = undefined
      row.res.has_draft = false
      row.res.checks = { blocking: 0, advisory: 0 }
      row.n++
      return noContent()
    },
  ],
  [
    'GET',
    /^\/api\/v1\/resources\/([^/]+)\/draft\/diff$/,
    (_r, _u, _who, [, code]) => {
      const row = db.resources.find((x) => x.res.code === code)
      if (!row?.draft) return notFound()
      return json(200, {
        base_version: row.res.version,
        files: [
          {
            path: 'SKILL.md',
            change: 'modified',
            unified:
              '@@ -12,3 +12,4 @@\n ## Steps\n-1. Read the input\n+1. Read the input and the template\n+2. Check the design for open questions\n',
          },
          { path: 'references/template.md', change: 'added' },
        ],
      })
    },
  ],
  [
    'POST',
    /^\/api\/v1\/resources\/([^/]+)\/publish$/,
    async (request, _u, who, [, code]) => {
      const row = db.resources.find((x) => x.res.code === code)
      const d = row && draftDoc(row)
      if (!row || !d) return problem(409, 'resource.no_draft', 'There is no draft to publish.')
      if (request.headers.get('If-Match') !== d.etag)
        return problem(412, 'precondition.failed', 'The draft changed since you looked at it.')
      if (d.checks.blocking > 0)
        return problem(
          409,
          'resource.check_blocked',
          `${d.checks.blocking} problems must be fixed before this can be published.`,
          { blocking: d.checks.blocking },
        )
      const { note } = await body<S['PublishRequest']>(request).catch(() => ({ note: undefined }))
      const number = (row.res.version ?? 0) + 1
      const v: S['ResourceVersion'] = {
        number,
        digest: d.digest,
        bundle: digest('b' + d.digest),
        note,
        ruleset: 1,
        checks: { blocking: 0, advisory: d.checks.advisory },
        published_by: meOf(who) ?? P.admin,
        published_at: new Date().toISOString(),
      }
      row.versions.unshift(v)
      Object.assign(row.res, {
        version: number,
        status: row.res.status === 'disabled' ? 'disabled' : 'published',
        has_draft: false,
        published_at: v.published_at,
        checks: v.checks,
      })
      row.draft = undefined
      row.n++
      audited(
        {
          action: 'resource.publish',
          action_label: 'Publish resource',
          object: { kind: 'resource', id: code ?? '', label: row.res.name },
          change: { version: { from: number - 1, to: number } },
          result: 'ok',
        },
        who,
      )
      return json(201, v, etagOf(row))
    },
  ],
  [
    'GET',
    /^\/api\/v1\/resources\/([^/]+)\/versions$/,
    (_r, _u, _who, [, code]) => {
      const row = db.resources.find((x) => x.res.code === code)
      return row ? json(200, { items: row.versions }) : notFound()
    },
  ],
  [
    'GET',
    /^\/api\/v1\/resources\/([^/]+)\/versions\/(\d+)$/,
    (_r, _u, _who, [, code, n]) => {
      const row = db.resources.find((x) => x.res.code === code)
      const v = row?.versions.find((x) => x.number === Number(n))
      if (!v) return notFound()
      return json(200, {
        ...v,
        findings: v.checks.advisory
          ? [
              finding(
                'description',
                'advisory',
                'The description does not say when to use it.',
                'SKILL.md',
                2,
              ),
            ]
          : [],
      })
    },
  ],
  [
    'POST',
    /^\/api\/v1\/resources\/([^/]+)\/rollback$/,
    async (request, _u, who, [, code]) => {
      const row = db.resources.find((x) => x.res.code === code)
      if (!row) return notFound()
      if (request.headers.get('If-Match') !== etagOf(row))
        return problem(412, 'precondition.failed', 'Someone changed this in the meantime.')
      const req = await body<S['RollbackRequest']>(request)
      const old = row.versions.find((x) => x.number === req.to_version)
      if (!old) return notFound()
      const number = (row.res.version ?? 0) + 1
      const v: S['ResourceVersion'] = {
        ...old,
        number,
        rollback_of: old.number,
        note: req.note,
        published_by: meOf(who) ?? P.admin,
        published_at: new Date().toISOString(),
      }
      row.versions.unshift(v)
      row.res.version = number
      row.n++
      return json(201, v, etagOf(row))
    },
  ],
  [
    'POST',
    /^\/api\/v1\/resources\/([^/]+)\/(disable|enable)$/,
    async (request, _u, who, [, code, verb]) => {
      const row = db.resources.find((x) => x.res.code === code)
      if (!row) return notFound()
      if (request.headers.get('If-Match') !== etagOf(row))
        return problem(412, 'precondition.failed', 'Someone changed this in the meantime.')
      if (verb === 'disable') {
        const { reason } = await body<S['ReasonRequest']>(request)
        Object.assign(row.res, { status: 'disabled', disabled_reason: reason })
      } else {
        Object.assign(row.res, {
          status: row.res.version ? 'published' : 'draft',
          disabled_reason: undefined,
        })
      }
      row.n++
      audited(
        {
          action: `resource.${verb ?? ''}`,
          action_label: verb === 'disable' ? 'Disable resource' : 'Enable resource',
          object: { kind: 'resource', id: code ?? '', label: row.res.name },
          change: {},
          result: 'ok',
        },
        who,
      )
      return json(200, managed(row), etagOf(row))
    },
  ],
  [
    'POST',
    /^\/api\/v1\/resources\/([^/]+)\/waivers$/,
    async (request, _u, who, [, code]) => {
      if (who !== 'admin') return denied()
      const row = db.resources.find((x) => x.res.code === code)
      const req = await body<S['WaiverRequest']>(request)
      const f = row?.draft?.checks.findings.find((x) => x.id === req.finding)
      if (!row?.draft || !f) return notFound()
      if (f.rule !== 'secrets')
        return problem(409, 'check.not_waivable', 'Findings of this rule cannot be waived.')
      f.waiver = { id: id(601), rule: f.rule, reason: req.reason, by: P.admin, at: new Date().toISOString() }
      const blocking = row.draft.checks.findings.filter((x) => x.severity === 'must_fix' && !x.waiver).length
      row.draft.checks.blocking = blocking
      row.res.checks = { ...row.res.checks, blocking }
      row.draft.digest = digest(row.draft.digest + 'w')
      return json(201, f.waiver)
    },
  ],
  [
    'GET',
    /^\/api\/v1\/check-rules$/,
    () =>
      json(200, {
        version: 1,
        rules: [
          {
            id: 'structure',
            severity: 'must_fix',
            name: 'Structure',
            description: 'SKILL.md exists, has a name and a description, and the name equals the code.',
          },
          {
            id: 'secrets',
            severity: 'must_fix',
            name: 'Suspected secret',
            description: 'Common token formats, password=, private key headers, high-entropy strings.',
            waivable: true,
          },
          {
            id: 'commands',
            severity: 'must_fix',
            name: 'Dangerous commands',
            description: 'rm -rf /, curl | sh and the like.',
          },
          {
            id: 'paths',
            severity: 'must_fix',
            name: 'Personal paths',
            description: 'Absolute paths from someone’s computer.',
          },
          {
            id: 'description',
            severity: 'advisory',
            name: 'Description quality',
            description: 'The description is too short or does not say when to use it.',
          },
          {
            id: 'overlap',
            severity: 'advisory',
            name: 'Overlapping triggers',
            description: 'Two resources could be selected by the same sentence.',
          },
        ],
      }),
  ],

  // ------------------------------------------------------------ sources
  [
    'GET',
    /^\/api\/v1\/sources$/,
    (_r, _u, who) => (who === 'admin' ? json(200, { items: db.sources }) : denied()),
  ],
  [
    'POST',
    /^\/api\/v1\/sources$/,
    async (request, _u, who) => {
      if (who !== 'admin') return denied()
      const req = await body<S['CreateSourceRequest']>(request)
      if (!/^(https:\/\/|ssh:\/\/|git@)/.test(req.url))
        return problem(400, 'request.invalid', 'Only https and ssh addresses are allowed.', undefined)
      const src: S['Source'] = {
        id: id(710 + db.sources.length),
        name: req.name,
        url: req.url,
        branch: req.branch,
        dir: req.dir ?? '',
        order: db.sources.length + 1,
        has_credential: !!req.credential,
        resource_count: 0,
        last_pull: { at: new Date().toISOString(), result: 'running' },
        etag: '"s-new-1"',
      }
      db.sources.push(src)
      return json(201, src, src.etag)
    },
  ],
  [
    'PUT',
    /^\/api\/v1\/sources\/order$/,
    async (request, _u, who) => {
      if (who !== 'admin') return denied()
      const { ids } = await body<S['SourceOrderRequest']>(request)
      db.sources = ids.flatMap((x, i) => {
        const s = db.sources.find((y) => y.id === x)
        return s ? [{ ...s, order: i + 1 }] : []
      })
      return json(200, { items: db.sources })
    },
  ],
  [
    'PATCH',
    /^\/api\/v1\/sources\/([^/]+)$/,
    async (request, _u, who, [, sid]) => {
      if (who !== 'admin') return denied()
      const s = db.sources.find((x) => x.id === sid)
      if (!s) return notFound()
      if (request.headers.get('If-Match') !== s.etag)
        return problem(412, 'precondition.failed', 'Someone changed this in the meantime.')
      const req = await body<S['UpdateSourceRequest']>(request)
      const { credential, clear_credential, ...rest } = req
      Object.assign(s, rest, {
        has_credential: clear_credential ? false : credential ? true : s.has_credential,
        etag: `"s-${sid}-${Date.now()}"`,
      })
      return json(200, s, s.etag)
    },
  ],
  [
    'DELETE',
    /^\/api\/v1\/sources\/([^/]+)$/,
    (_r, _u, who, [, sid]) => {
      if (who !== 'admin') return denied()
      db.sources = db.sources.filter((x) => x.id !== sid).map((x, i) => ({ ...x, order: i + 1 }))
      return noContent()
    },
  ],
  [
    'POST',
    /^\/api\/v1\/sources\/([^/]+)\/pull$/,
    (_r, _u, who, [, sid]) => {
      if (who !== 'admin') return denied()
      const s = db.sources.find((x) => x.id === sid)
      if (!s) return notFound()
      s.last_pull = { ...s.last_pull, at: new Date().toISOString(), result: 'running' }
      setTimeout(() => {
        s.last_pull = {
          at: new Date().toISOString(),
          result: 'ok',
          commit: '9b8c7d6e5f4a3f2a1b0c9d8e7f6a5b4c3d2e1f0a',
          imported: 0,
          failed: 0,
          last_success_at: new Date().toISOString(),
        }
      }, 2500)
      return json(202, s)
    },
  ],

  // ------------------------------------------------------------ settings
  [
    'GET',
    /^\/api\/v1\/settings$/,
    (_r, _u, who) => (who === 'admin' ? json(200, db.settings, db.settings.etag) : denied()),
  ],
  [
    'PATCH',
    /^\/api\/v1\/settings$/,
    async (request, _u, who) => {
      if (who !== 'admin') return denied()
      const match = request.headers.get('If-Match')
      if (!match) return problem(428, 'precondition.required', 'Send If-Match with the ETag you saw.')
      if (match !== db.settings.etag)
        return problem(412, 'precondition.failed', 'Someone changed the settings in the meantime.')
      const req = await body<S['UpdateSettingsRequest']>(request)
      const { password, ...mail } = req.mail ?? {}
      db.settings = {
        organization: { ...db.settings.organization, ...req.organization },
        mail: { ...db.settings.mail, ...mail, has_password: password ? true : db.settings.mail.has_password },
        etag: `"settings-${++settingsN}"`,
      }
      audited(
        {
          action: 'settings.update',
          action_label: 'Change settings',
          object: { kind: 'settings', id: 'platform', label: 'Platform settings' },
          change: { fields: Object.keys({ ...req.organization, ...req.mail }) },
          result: 'ok',
        },
        who,
      )
      return json(200, db.settings, db.settings.etag)
    },
  ],
  [
    'POST',
    /^\/api\/v1\/settings\/mail\/test$/,
    async (request, _u, who) => {
      if (who !== 'admin') return denied()
      const { to } = await body<S['TestMailRequest']>(request)
      if (to.endsWith('.invalid'))
        return problem(502, 'mail.failed', 'smtp.example.com:465: authentication failed')
      return noContent()
    },
  ],
  [
    'GET',
    /^\/api\/v1\/modules$/,
    (_r, _u, who) => (who === 'admin' ? json(200, { items: db.modules }) : denied()),
  ],
  [
    'PATCH',
    /^\/api\/v1\/modules\/([^/]+)$/,
    async (request, _u, who, [, mid]) => {
      if (who !== 'admin') return denied()
      const mod = db.modules.find((x) => x.id === mid)
      if (!mod) return notFound()
      const { enabled } = await body<S['UpdateModuleRequest']>(request)
      if (mod.locked && enabled)
        return problem(409, 'module.locked', 'The deployment turned this module off.')
      mod.enabled = enabled
      return json(200, mod)
    },
  ],

  // ------------------------------------------------------------ adapters and command line
  [
    'GET',
    /^\/api\/v1\/adapters$/,
    (_r, _u, who) => (who === 'admin' ? json(200, { items: db.adapters }) : denied()),
  ],
  [
    'PATCH',
    /^\/api\/v1\/adapters\/([^/]+)$/,
    async (request, _u, who, [, aid]) => {
      if (who !== 'admin') return denied()
      const a = db.adapters.find((x) => x.id === aid)
      if (!a) return notFound()
      a.enabled = (await body<S['UpdateAdapterRequest']>(request)).enabled
      return json(200, a)
    },
  ],
  ['GET', /^\/api\/v1\/cli$/, () => json(200, db.cli)],
  [
    'PATCH',
    /^\/api\/v1\/cli$/,
    async (request, _u, who) => {
      if (who !== 'admin') return denied()
      const { min_version } = await body<S['UpdateCLIPolicyRequest']>(request)
      const key = (v: string) =>
        v
          .split(/[.-]/)
          .slice(0, 3)
          .map((x) => x.padStart(6, '0'))
          .join('.')
      if (key(min_version) < key(db.cli.min_version_floor))
        return problem(409, 'cli.below_floor', `This server needs at least ${db.cli.min_version_floor}.`, {
          floor: db.cli.min_version_floor,
        })
      db.cli = { ...db.cli, min_version }
      return json(200, db.cli)
    },
  ],

  // ------------------------------------------------------------ audit
  [
    'GET',
    /^\/api\/v1\/audit$/,
    (_r, url, who) => {
      const rows = auditFilter(url, who)
      const limit = Number(url.searchParams.get('limit') ?? 50)
      const from = Number(url.searchParams.get('cursor') ?? 0)
      const page = rows.slice(from, from + limit)
      return json(200, {
        items: page,
        ...(from + limit < rows.length ? { next_cursor: String(from + limit) } : {}),
      })
    },
  ],
  [
    'GET',
    /^\/api\/v1\/audit\/export$/,
    (_r, url, who) => {
      const rows = auditFilter(url, who).reverse()
      const cell = (v: unknown) => {
        const s = v === undefined ? '' : typeof v === 'string' ? v : JSON.stringify(v)
        return /[",\n]/.test(s) ? `"${s.replaceAll('"', '""')}"` : s
      }
      const lines = rows.map((e) =>
        [
          e.seq,
          e.at,
          e.category,
          e.actor?.username,
          e.origin,
          e.via_tool,
          e.device?.name,
          e.action,
          e.object.kind,
          e.object.id,
          e.object.label,
          e.change,
          e.result,
          e.reason,
          e.method,
        ]
          .map(cell)
          .join(','),
      )
      const csv =
        [
          'seq,at,category,actor,origin,via_tool,device,action,object_kind,object_id,object_label,change,result,reason,method',
          ...lines,
        ].join('\n') + '\n'
      return new Response(csv, {
        status: 200,
        headers: { 'Content-Type': 'text/csv', 'Content-Disposition': 'attachment; filename="audit.csv"' },
      })
    },
  ],
]

function auditFilter(url: URL, who: Who): S['AuditEntry'][] {
  const q = url.searchParams
  const me = meOf(who)
  return db.audit.filter(
    (e) =>
      (who === 'admin' || e.actor?.id === me?.id) &&
      (!q.get('category') || e.category === q.get('category')) &&
      (!q.get('result') || e.result === q.get('result')) &&
      (!q.get('action') ||
        (q.get('action')?.endsWith('.')
          ? e.action.startsWith(q.get('action') ?? '')
          : e.action === q.get('action'))) &&
      (!q.get('since') || e.at >= (q.get('since') ?? '')),
  )
}

/**
 * adminMock starts the data (after mock-auth.ts has read ?mockempty and ?mock=setup…) and returns what answers
 * a request for one of the pages above, or undefined to let the other mocks answer it.
 */
export function adminMock(): (request: Request, who: Who) => Promise<Response> | undefined {
  const setup = sessionStorage.getItem('mock-setup')
  db = startDB(sessionStorage.getItem('mockempty') === '1' || setup === 'open' || setup === 'admin')
  return (request, who) => {
    const url = new URL(request.url)
    const path = url.pathname.replace(/^.*\/api\//, '/api/')
    for (const [method, pattern, handler] of routes) {
      const match = path.match(pattern)
      if (!match || request.method !== method) continue
      return (async () => {
        await new Promise((r) => setTimeout(r, 120))
        return handler(request, url, who, match)
      })()
    }
    return undefined
  }
}
