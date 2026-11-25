// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The development stand-in for the endpoints of the "我的" pages (home, catalog, connect, keys, feedback)
// and the feedback dialog. Imported only by mock.ts, so never part of a real build. The data is the
// prototype's fictional organisation in the contract's shapes, with names in English and Chinese so the
// pages can be compared with the prototype in both languages.
//
// Besides ?mock=<who>, two switches help check the states a page has to handle:
//   ?scene=fresh   a newcomer whose computer is not connected yet (no devices, secrets missing)
//   ?scene=empty   a fresh deployment without any published resource
//   ?fail=403      every endpoint here answers 403 access.denied
//   ?fail=network  every endpoint here fails as if the server were unreachable
// Each is remembered for the tab (sessionStorage) until set again; ?scene=normal and ?fail=none reset them.

import type { Schemas } from './client'

export type Who = 'admin' | 'owner' | 'member'
type Scene = 'normal' | 'fresh' | 'empty'

function setting(name: string, allowed: readonly string[], fallback: string): string {
  const v = new URLSearchParams(location.search).get(name) ?? sessionStorage.getItem('mock-' + name)
  const w = v && allowed.includes(v) ? v : fallback
  sessionStorage.setItem('mock-' + name, w)
  return w
}

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const problem = (status: number, code: string, detail?: string) =>
  new Response(
    JSON.stringify({ type: 'about:blank', title: '', status, code, ...(detail ? { detail } : {}) }),
    {
      status,
      headers: { 'Content-Type': 'application/problem+json' },
    },
  )

const ID = (n: number) => `0193a4b2-6c1e-7a00-8000-${String(n).padStart(12, '0')}`
const ago = (minutes: number) => new Date(Date.now() - minutes * 60_000).toISOString()
const later = (days: number) => new Date(Date.now() + days * 86_400_000).toISOString()

/** Text in both languages; the mock answers in the request's Accept-Language like the server does. */
type L = [en: string, zh: string]

const STAGES: [string, L, L][] = [
  [
    'requirement',
    ['Requirements', '需求'],
    [
      'Turn a one-line request into user stories and acceptance criteria',
      '把一句话需求拆成用户故事和验收标准',
    ],
  ],
  [
    'architecture',
    ['Architecture', '架构'],
    ['Draw the system boundaries and choose the technology', '梳理系统边界和技术选型'],
  ],
  [
    'design',
    ['Design', '设计'],
    ['Write the functional and development design from the templates', '按模板产出功能设计和开发设计'],
  ],
  [
    'develop',
    ['Development', '开发'],
    ['Implement to the design and the standards, check the impact', '照着设计和规范实现，自检影响面'],
  ],
  [
    'test',
    ['Testing', '测试'],
    ['Generate and run tests, cover the edge cases', '生成并运行测试，补齐边界用例'],
  ],
  [
    'scm',
    ['Configuration', '配管'],
    ['Branches and versions, merging, building and packaging', '管好分支和版本，合并集成、构建打包'],
  ],
  [
    'release',
    ['Release', '发布'],
    ['Deploy to the target environment and check its health', '部署到目标环境，做健康检查'],
  ],
  [
    'ops',
    ['Operations', '运维'],
    ['Read logs and alerts, find what is wrong in an environment', '看日志、查告警，定位环境问题'],
  ],
]

interface Res {
  code: string
  kind: string
  name: L
  summary: L
  try?: L
  stages: string[]
  audience?: 'rd' | 'mgmt'
  scope: string
  policy: Schemas['Policy']
  origin: Schemas['Origin']
  version: number
  enabled: boolean
  people: number
  secrets?: string[]
  delivery?: Schemas['Delivery']
  definition?: Record<string, unknown>
  references?: string[]
  local?: [string, L][]
}

const R: Res[] = [
  {
    code: 'jira',
    kind: 'mcp',
    name: ['Jira', 'Jira'],
    summary: [
      'Reads and writes requirements, tasks and defects; queries sprints',
      '读写需求、任务和缺陷，查询迭代计划',
    ],
    try: ['List my unfinished tasks in this sprint', '查一下我这个迭代还没完成的任务'],
    stages: ['requirement', 'develop', 'test'],
    scope: 'org',
    policy: 'required',
    origin: 'web',
    version: 4,
    enabled: true,
    people: 142,
    secrets: ['JIRA_API_TOKEN'],
    delivery: 'native',
    definition: {
      server: {
        type: 'http',
        url: 'https://jira.example.com/mcp',
        headers: { Authorization: 'Bearer ${secret:JIRA_API_TOKEN}' },
      },
      delivery: 'native',
      tools: [
        { name: 'search_issues', summary: 'Search issues with JQL' },
        { name: 'get_issue', summary: 'Read an issue with its comments' },
        { name: 'create_issue', summary: 'Create an issue', writes: true },
        { name: 'transition_issue', summary: 'Move an issue to another status', writes: true },
      ],
    },
  },
  {
    code: 'gitlab',
    kind: 'mcp',
    name: ['GitLab', 'GitLab'],
    summary: ['Merge requests, pipelines and review comments', '查询合并请求、流水线和评审意见'],
    stages: ['develop', 'scm', 'release'],
    scope: 'org',
    policy: 'required',
    origin: 'web',
    version: 2,
    enabled: true,
    people: 118,
    secrets: ['GITLAB_TOKEN'],
    delivery: 'native',
    definition: {
      server: {
        type: 'stdio',
        command: 'npx',
        args: ['-y', '@example/gitlab-mcp@1.4.2'],
        env: { GITLAB_TOKEN: '${secret:GITLAB_TOKEN}', GITLAB_URL: 'https://gitlab.example.com' },
      },
      tools: [
        { name: 'list_merge_requests', summary: 'List merge requests' },
        { name: 'get_pipeline', summary: 'Pipeline status and failed jobs' },
        { name: 'add_mr_note', summary: 'Comment on a merge request', writes: true },
      ],
    },
  },
  {
    code: 'confluence',
    kind: 'mcp',
    name: ['Confluence', 'Confluence'],
    summary: [
      'Searches and reads design documents and standards in team spaces',
      '检索和读取团队空间里的设计文档与规范',
    ],
    stages: ['architecture', 'design', 'develop'],
    scope: 'org',
    policy: 'recommended',
    origin: 'web',
    version: 1,
    enabled: true,
    people: 63,
    secrets: ['CONFLUENCE_TOKEN'],
    delivery: 'on_demand',
    definition: {
      server: {
        type: 'http',
        url: 'https://confluence.example.com/mcp',
        headers: { Authorization: 'Bearer ${secret:CONFLUENCE_TOKEN}' },
      },
      delivery: 'on-demand',
      tools: [
        { name: 'search_pages', summary: 'Full-text search' },
        { name: 'get_page', summary: 'Read a page' },
      ],
    },
  },
  {
    code: 'figma',
    kind: 'mcp',
    name: ['Figma', 'Figma'],
    summary: ['Reads layers, annotations and exports of design files', '读取设计稿的图层、标注和切图'],
    try: [
      'Read the annotations of this design and list the fields on the page',
      '读一下这个设计稿的标注，列出页面上的字段',
    ],
    stages: ['design', 'develop'],
    scope: 'org',
    policy: 'recommended',
    origin: 'web',
    version: 2,
    enabled: true,
    people: 39,
    secrets: ['FIGMA_TOKEN'],
    delivery: 'on_demand',
    definition: {
      server: {
        type: 'http',
        url: 'https://figma.example.com/mcp',
        headers: { 'X-Figma-Token': '${secret:FIGMA_TOKEN}' },
      },
      delivery: 'on-demand',
      tools: [
        { name: 'get_file', summary: 'Read the structure of a file' },
        { name: 'get_node', summary: 'Read the annotations of a node' },
      ],
    },
  },
  {
    code: 'sentry',
    kind: 'mcp',
    name: ['Sentry', 'Sentry'],
    summary: ['Production errors, stack traces and release health', '查询线上错误、堆栈和版本健康度'],
    stages: ['test', 'ops'],
    scope: 'org',
    policy: 'optional',
    origin: 'web',
    version: 1,
    enabled: false,
    people: 31,
    secrets: ['SENTRY_TOKEN'],
    delivery: 'native',
    definition: {
      server: {
        type: 'http',
        url: 'https://sentry.example.com/mcp',
        headers: { Authorization: 'Bearer ${secret:SENTRY_TOKEN}' },
      },
      tools: [
        { name: 'list_issues', summary: 'Recent errors' },
        { name: 'get_event', summary: 'Stack trace of one error' },
      ],
    },
  },
  {
    code: 'requirement-analysis',
    kind: 'skill',
    name: ['Requirement analysis', '需求分析'],
    summary: [
      'Breaks a requirement into user stories and acceptance criteria, lists open questions',
      '把需求拆成用户故事和验收标准，标出待澄清项',
    ],
    try: [
      'Analyse requirement SHOP-1250 and list what needs confirming',
      '分析一下需求 SHOP-1250，列出要确认的问题',
    ],
    stages: ['requirement'],
    scope: 'org',
    policy: 'recommended',
    origin: 'repo',
    version: 7,
    enabled: true,
    people: 48,
  },
  {
    code: 'functional-design',
    kind: 'skill',
    name: ['Functional design', '功能设计'],
    summary: [
      'Pages, interfaces, data and permissions from the requirement',
      '基于需求产出功能设计：页面、接口、数据、权限',
    ],
    try: ['Draft a functional design from that requirement analysis', '基于刚才的需求分析出一版功能设计'],
    stages: ['design'],
    scope: 'org',
    policy: 'recommended',
    origin: 'repo',
    version: 5,
    enabled: true,
    people: 44,
  },
  {
    code: 'architecture-design',
    kind: 'skill',
    name: ['Architecture design', '架构设计'],
    summary: [
      'System boundaries, modules and technology choices, with decision records',
      '梳理系统边界、模块划分和技术选型，产出架构方案和决策记录',
    ],
    stages: ['architecture'],
    scope: 'org',
    policy: 'recommended',
    origin: 'repo',
    version: 2,
    enabled: true,
    people: 17,
  },
  {
    code: 'design-doc',
    kind: 'skill',
    name: ['Design document', '开发设计文档'],
    summary: [
      'Writes or updates the design document from the template',
      '按模板生成或更新开发设计文档，自动解析原型图',
    ],
    stages: ['design'],
    scope: 'org',
    policy: 'recommended',
    origin: 'repo',
    version: 9,
    enabled: true,
    people: 41,
  },
  {
    code: 'task-development',
    kind: 'skill',
    name: ['Task development', '任务开发'],
    summary: [
      'Implements a task to the design and coding standards, checks the impact',
      '按设计文档和编码规范实现任务，自检影响面',
    ],
    try: ['Develop task SHOP-1287 as specified; list a plan first', '按任务 SHOP-1287 的要求开发，先列计划'],
    stages: ['develop'],
    scope: 'org',
    policy: 'recommended',
    origin: 'repo',
    version: 12,
    enabled: true,
    people: 97,
  },
  {
    code: 'unit-testing',
    kind: 'skill',
    name: ['Unit testing', '单元测试'],
    summary: [
      'Writes and runs unit tests for a change, covering edge cases',
      '为改动生成并运行单元测试，补齐边界用例',
    ],
    try: ['Add unit tests for the classes I just changed', '给我刚改的这几个类补单元测试'],
    stages: ['test'],
    scope: 'org',
    policy: 'recommended',
    origin: 'repo',
    version: 4,
    enabled: true,
    people: 58,
    local: [
      ['TEST_REPORT_DIR', ['Where test reports go', '测试报告输出目录']],
      ['JAVA8_HOME', ['Where Java 8 is installed, for old projects', 'Java 8 的安装目录，老工程编译用']],
    ],
  },
  {
    code: 'shop-e2e',
    kind: 'skill',
    name: ['End-to-end testing', '端到端测试'],
    summary: [
      'Runs test cases through the pages of the test environment and checks the results',
      '在测试环境里按用例操作页面并核对结果',
    ],
    stages: ['test'],
    scope: 'shop',
    policy: 'optional',
    origin: 'upload',
    version: 3,
    enabled: false,
    people: 14,
  },
  {
    code: 'delivery-check',
    kind: 'skill',
    name: ['Pre-submission check', '提测前自查'],
    summary: [
      'Four checks before handing over to testing',
      '提测前四查：提交与任务一致、验收标准覆盖、高危问题、文档同步',
    ],
    try: ['Run the pre-submission check for me', '提测前帮我做一次自查'],
    stages: ['test', 'release'],
    scope: 'org',
    policy: 'recommended',
    origin: 'repo',
    version: 6,
    enabled: true,
    people: 52,
  },
  {
    code: 'merge-integration',
    kind: 'skill',
    name: ['Merge and integration', '合并集成'],
    summary: [
      'Merges feature branches into the target branch by version and resolves conflicts',
      '把特性分支按版本合入目标分支并处理冲突',
    ],
    stages: ['scm'],
    scope: 'org',
    policy: 'optional',
    origin: 'repo',
    version: 3,
    enabled: false,
    people: 9,
  },
  {
    code: 'pay-reconcile',
    kind: 'skill',
    name: ['Reconciliation rules', '对账规则核对'],
    summary: [
      'Checks whether a change affects end-of-day reconciliation',
      '对照对账规则检查改动是否影响日终对账',
    ],
    stages: ['develop', 'test'],
    scope: 'pay',
    policy: 'recommended',
    origin: 'repo',
    version: 3,
    enabled: true,
    people: 19,
  },
  {
    code: 'weekly-report',
    kind: 'skill',
    name: ['Weekly report', '周报汇总'],
    summary: [
      "Summarises the team's week from tasks and commits: progress, risks, next week",
      '按任务和提交记录汇总团队本周进展、风险和下周计划',
    ],
    stages: [],
    audience: 'mgmt',
    scope: 'org',
    policy: 'recommended',
    origin: 'repo',
    version: 3,
    enabled: true,
    people: 31,
  },
  {
    code: 'review-minutes',
    kind: 'skill',
    name: ['Review minutes', '评审会纪要'],
    summary: [
      'Turns a review discussion into decisions, open issues and owners',
      '把评审会的讨论整理成结论、遗留问题和责任人',
    ],
    stages: [],
    audience: 'mgmt',
    scope: 'org',
    policy: 'optional',
    origin: 'repo',
    version: 2,
    enabled: true,
    people: 18,
  },
  {
    code: 'req-to-design',
    kind: 'workflow',
    name: ['From requirement to design', '从需求到设计'],
    summary: [
      'Requirement analysis → functional design → design document',
      '需求分析 → 功能设计 → 开发设计文档',
    ],
    try: ['/rgd-req-to-design SHOP-1250', '/rgd-req-to-design SHOP-1250'],
    stages: ['requirement', 'design'],
    scope: 'org',
    policy: 'recommended',
    origin: 'repo',
    version: 2,
    enabled: true,
    people: 29,
    references: ['requirement-analysis', 'functional-design', 'design-doc'],
    definition: {
      steps: [
        {
          use: 'requirement-analysis',
          checks: [
            { text: 'Every user story has acceptance criteria', by: 'ai' },
            { text: 'Open questions are listed with whom to ask', by: 'ai' },
          ],
          then: 'continue',
        },
        {
          use: 'functional-design',
          checks: [
            { text: 'The design covers every acceptance criterion', by: 'ai' },
            { text: 'The design review has passed', by: 'human' },
          ],
          then: 'confirm',
        },
        {
          use: 'design-doc',
          checks: [{ text: 'The document follows the template', by: 'ai' }],
          then: 'continue',
        },
      ],
    },
  },
  {
    code: 'task-to-submit',
    kind: 'workflow',
    name: ['From task to submission', '从任务到提测'],
    summary: ['Task development → unit testing → pre-submission check', '任务开发 → 单元测试 → 提测前自查'],
    try: ['/rgd-task-to-submit SHOP-1287', '/rgd-task-to-submit SHOP-1287'],
    stages: ['develop', 'test'],
    scope: 'org',
    policy: 'recommended',
    origin: 'repo',
    version: 3,
    enabled: true,
    people: 61,
    references: ['task-development', 'unit-testing', 'delivery-check'],
    definition: {
      steps: [
        {
          use: 'task-development',
          checks: [
            { text: 'The change stays within the task', by: 'ai' },
            { text: 'The project compiles', by: 'command', run: './gradlew compileJava -q' },
          ],
          then: 'continue',
        },
        {
          use: 'unit-testing',
          checks: [{ text: 'All unit tests pass', by: 'command', run: './gradlew test -q' }],
          then: 'continue',
        },
        {
          use: 'delivery-check',
          checks: [{ text: 'A person has confirmed the result', by: 'human' }],
          forbid: ['push the code'],
          then: 'confirm',
        },
      ],
    },
  },
  {
    code: 'backend-developer',
    kind: 'agent',
    name: ['Backend developer', '后端工程师'],
    summary: [
      'API development, data modelling, integration, defect diagnosis, with self-tests as proof',
      '接口开发、数据建模、服务集成、缺陷诊断，并用开发自测证明改动',
    ],
    try: [
      'Have the backend developer implement SHOP-1287; plan first',
      '让后端工程师按任务 SHOP-1287 实现，先列计划',
    ],
    stages: ['develop'],
    scope: 'org',
    policy: 'recommended',
    origin: 'repo',
    version: 2,
    enabled: true,
    people: 46,
    references: ['task-development', 'unit-testing', 'task-to-submit'],
  },
  {
    code: 'code-reviewer',
    kind: 'agent',
    name: ['Code reviewer', '代码审核员'],
    summary: [
      'Reviews a change against the review rules, with locations and conclusions',
      '对照评审规则审核改动，逐条给出问题位置和结论',
    ],
    try: ['Have the code reviewer look at the changes on my branch', '让代码审核员看一下我这个分支的改动'],
    stages: ['develop', 'test'],
    scope: 'org',
    policy: 'optional',
    origin: 'repo',
    version: 1,
    enabled: false,
    people: 13,
    secrets: ['GITLAB_TOKEN'],
    references: ['gitlab'],
  },
]

const SECRETS: { name: string; label: L; howto: L; configured: boolean; expires?: number }[] = [
  {
    name: 'JIRA_API_TOKEN',
    label: ['Jira API token', 'Jira API 令牌'],
    howto: ['Jira → Account settings → Security → API tokens', 'Jira → 个人设置 → 安全 → API 令牌'],
    configured: true,
    expires: 5,
  },
  {
    name: 'GITLAB_TOKEN',
    label: ['GitLab personal access token', 'GitLab 个人访问令牌'],
    howto: [
      'GitLab → Preferences → Access tokens, with read_api',
      'GitLab → 偏好设置 → 访问令牌，勾选 read_api',
    ],
    configured: true,
    expires: 61,
  },
  {
    name: 'SENTRY_TOKEN',
    label: ['Sentry token', 'Sentry 令牌'],
    howto: ['Sentry → User settings → Personal tokens', 'Sentry → 用户设置 → 个人令牌'],
    configured: true,
  },
  {
    name: 'CONFLUENCE_TOKEN',
    label: ['Confluence personal access token', 'Confluence 个人访问令牌'],
    howto: ['Confluence → Settings → Personal access tokens', 'Confluence → 个人设置 → 个人访问令牌'],
    configured: false,
  },
  {
    name: 'FIGMA_TOKEN',
    label: ['Figma personal access token', 'Figma 个人访问令牌'],
    howto: [
      'Figma → Settings → Security → Personal access tokens',
      'Figma → Settings → Security → Personal access tokens',
    ],
    configured: false,
  },
]

const PRODUCTS: Schemas['ProductRef'][] = [
  { code: 'shop', name: 'Shop' },
  { code: 'pay', name: 'Payments' },
  { code: 'app', name: 'Mobile app' },
  { code: 'data', name: 'Data platform' },
]
const MY_PRODUCTS: Record<Who, string[]> = { admin: ['shop'], owner: ['pay', 'shop'], member: ['shop'] }

const people = {
  jdoe: { id: ID(11), username: 'jdoe', display_name: 'Jamie Doe' },
  admin: { id: ID(1), username: 'admin', display_name: 'Alex Admin' },
  owner: { id: ID(2), username: 'owner', display_name: 'Olive Owner' },
  gyuan: { id: ID(13), username: 'gyuan', display_name: 'Gu Yuan' },
  ksong: { id: ID(14), username: 'ksong', display_name: 'Ke Song' },
}
const self: Record<Who, Schemas['UserRef']> = {
  admin: people.admin,
  owner: people.owner,
  member: people.jdoe,
}

interface Fb {
  number: number
  title: L
  body: L
  type: Schemas['FeedbackType']
  origin: Schemas['FeedbackOrigin']
  status: Schemas['FeedbackStatus']
  severity: Schemas['FeedbackSeverity']
  submitter?: Schemas['UserRef']
  assignee?: Schemas['UserRef']
  resource?: string
  version?: number
  score?: number
  note?: string
  at: number
  context: Schemas['FeedbackContext']
  events: Schemas['FeedbackEvent'][]
  extra?: L
}

function feedbackSeed(who: Who): Fb[] {
  const mine = self[who]
  return [
    {
      number: 42,
      title: ['Confluence MCP keeps failing to connect', 'Confluence MCP 经常连不上'],
      body: [
        '412 uses in 7 days, 142 errors (34.5 %), above the 30 % threshold. Mostly connection timeouts.',
        '近 7 天使用 412 次，报错 142 次，报错率 34.5%，超过阈值 30%。主要是连接超时。',
      ],
      type: 'friction',
      origin: 'system',
      status: 'in_progress',
      severity: 'high',
      assignee: people.admin,
      resource: 'confluence',
      version: 1,
      at: 180,
      context: { trigger: 'uses_7d >= 10 && error_rate >= 30%', tool: 'claude-code' },
      events: [
        { id: ID(801), kind: 'created', at: ago(180) },
        {
          id: ID(802),
          kind: 'comment',
          by: people.admin,
          body: 'Timeouts cluster between 14:00 and 16:00; asked the Confluence admins about rate limits.',
          at: ago(150),
        },
        { id: ID(803), kind: 'status', by: people.admin, status: 'in_progress', at: ago(149) },
      ],
    },
    {
      number: 41,
      title: ['unit-testing used var in a Java 8 project', 'unit-testing 在 Java 8 工程里生成了 var 关键字'],
      body: [
        'In an old Java 8 project the generated tests used var and did not compile. It took two corrections in the same session.',
        '在一个 Java 8 的老工程里让它补单测，生成的测试代码用了 var，编译不过。同一会话里纠正了两次才改对。',
      ],
      type: 'bug',
      origin: 'ai',
      status: 'accepted',
      severity: 'medium',
      submitter: mine,
      assignee: people.gyuan,
      resource: 'unit-testing',
      version: 4,
      at: 960,
      context: {
        tool: 'claude-code',
        tool_version: '2.1.330',
        cli_version: '0.5.0',
        os: 'darwin',
        frictions: [
          { type: 'repeated_correction', count: 2 },
          { type: 'tool_error', count: 1 },
        ],
      },
      events: [
        { id: ID(811), kind: 'created', by: mine, at: ago(960) },
        { id: ID(812), kind: 'status', by: people.gyuan, status: 'accepted', at: ago(400) },
      ],
    },
    {
      number: 39,
      title: [
        'Let the workflow stop and wait for me to confirm the design',
        '希望工作流能在中途停下来等我确认设计',
      ],
      body: [
        '"From requirement to design" runs all three steps at once. I want to look at the functional design before it writes the design document.',
        '「从需求到设计」现在一口气跑完三步。功能设计出来后我想先看一眼，再让它写开发设计文档。',
      ],
      type: 'suggestion',
      origin: 'web',
      status: 'new',
      severity: 'low',
      submitter: people.ksong,
      resource: 'req-to-design',
      version: 2,
      at: 14_400,
      context: { page: '/', browser: 'Chrome 140 · macOS' },
      events: [{ id: ID(821), kind: 'created', by: people.ksong, at: ago(14_400) }],
    },
    {
      number: 37,
      title: ['Slash commands do not install in OpenCode', 'OpenCode 里装不上斜杠命令'],
      body: [
        'rgd sync says it skipped 3 workflows for OpenCode.',
        'rgd sync 提示 3 个工作流在 OpenCode 跳过。',
      ],
      type: 'bug',
      origin: 'cli',
      status: 'resolved',
      severity: 'medium',
      submitter: mine,
      assignee: people.admin,
      at: 17_280,
      context: { tool: 'opencode', cli_version: '0.4.2', os: 'windows', last_sync: 'partial' },
      events: [
        { id: ID(831), kind: 'created', by: mine, at: ago(17_280) },
        {
          id: ID(832),
          kind: 'comment',
          by: people.admin,
          body: 'Workflows now go to OpenCode as skills from 0.5.0; upgrade and sync again.',
          at: ago(16_000),
        },
        { id: ID(833), kind: 'status', by: people.admin, status: 'resolved', at: ago(15_990) },
      ],
    },
    {
      number: 33,
      title: ["Jira MCP cannot find next sprint's plan", 'Jira MCP 查不到下个迭代的计划'],
      body: ['Asking the AI for next sprint’s tasks returns nothing.', '让 AI 查下个迭代的任务，返回空。'],
      type: 'question',
      origin: 'web',
      status: 'resolved',
      severity: 'low',
      submitter: mine,
      assignee: people.gyuan,
      resource: 'jira',
      version: 3,
      score: 5,
      note: 'Answered the same day',
      at: 21_600,
      context: { page: '/catalog/jira', browser: 'Chrome 140 · macOS' },
      events: [
        { id: ID(841), kind: 'created', by: mine, at: ago(21_600) },
        {
          id: ID(842),
          kind: 'comment',
          by: people.gyuan,
          body: 'Next sprint has not started; list_sprints returns active sprints unless you ask for state=future. Added to the description.',
          at: ago(21_000),
        },
        { id: ID(843), kind: 'status', by: people.gyuan, status: 'resolved', at: ago(20_990) },
        { id: ID(844), kind: 'rated', by: mine, score: 5, at: ago(20_000) },
      ],
    },
  ]
}

/** memberMock returns the handler for these endpoints; it answers undefined for any other path. */
export function memberMock(): (request: Request, path: string, who: Who) => Promise<Response | undefined> {
  const scene = setting('scene', ['normal', 'fresh', 'empty'], 'normal') as Scene
  const fail = setting('fail', ['none', '403', 'network'], 'none')
  const enabled = new Map(R.map((r) => [r.code, r.enabled]))
  const configured = new Map(SECRETS.map((s) => [s.name, s.configured]))
  const expires = new Map(SECRETS.map((s) => [s.name, s.expires]))
  let devices: Schemas['Device'][] =
    scene === 'fresh'
      ? []
      : [
          {
            id: ID(201),
            name: 'dev-laptop',
            os: 'darwin',
            arch: 'arm64',
            cli_version: '0.5.0',
            key_storage: 'keyring',
            current: true,
            created_at: ago(33_000),
            last_seen_at: ago(25),
            last_sync: { at: ago(6), result: 'ok', changes: 0 },
            tools: [
              { tool: 'claude-code', version: '2.1.330' },
              { tool: 'codex', version: '0.172.0' },
            ],
          },
          {
            id: ID(202),
            name: 'dev-pc',
            os: 'windows',
            arch: 'amd64',
            cli_version: '0.4.2',
            key_storage: 'file',
            current: false,
            created_at: ago(60_000),
            last_seen_at: ago(4_320),
            last_sync: { at: ago(4_320), result: 'partial', changes: 3 },
            tools: [{ tool: 'claude-code' }, { tool: 'opencode', version: '2.4.1' }],
          },
          {
            id: ID(203),
            name: 'dev-box',
            os: 'linux',
            arch: 'amd64',
            cli_version: '0.5.0',
            key_storage: 'file',
            current: false,
            created_at: ago(90_000),
            last_seen_at: ago(1_400),
            last_sync: { at: ago(1_400), result: 'ok', changes: 2 },
            tools: [{ tool: 'codex', version: '0.172.0' }],
          },
        ]
  let tokens: Schemas['AccessToken'][] = [
    {
      id: ID(301),
      name: 'CI pipeline',
      prefix: 'rgd_Qm4x',
      scope: 'read',
      created_at: ago(80_000),
      expires_at: later(35),
      last_used_at: ago(1_200),
    },
  ]
  const joins: Schemas['JoinRequest'][] = []
  let feedback: Fb[] | null = null
  let nextNumber = 43

  const visible = (who: Who) =>
    scene === 'empty' ? [] : R.filter((r) => r.scope === 'org' || MY_PRODUCTS[who].includes(r.scope))

  return async (request, path, who) => {
    if (!/^\/api\/v1\/(stages|me\/|catalog|installs|feedback|cli\/downloads)/.test(path)) {
      return undefined
    }
    if (request.method === 'GET' && path === '/api/v1/me') return undefined
    if (request.method === 'PATCH' && path === '/api/v1/me') return undefined
    // A little latency, so loading states can be seen.
    await new Promise((r) => setTimeout(r, 120))
    if (fail === 'network') throw new TypeError('Failed to fetch')
    if (fail === '403') return problem(403, 'access.denied', undefined)

    const zh = (request.headers.get('Accept-Language') ?? '').startsWith('zh')
    const tr = (l: L) => (zh ? l[1] : l[0])
    const url = new URL(request.url)
    const q = url.searchParams
    const route = `${request.method} ${path}`
    const body = async <T>() => (await request.json()) as T
    feedback ??= feedbackSeed(who)
    const fbs = feedback

    const secretNeeds = (r: Res): Schemas['SecretNeed'][] | undefined =>
      r.secrets?.map((name) => ({ name, configured: configured.get(name) ?? false }))
    const item = (r: Res): Schemas['CatalogItem'] => ({
      code: r.code,
      kind: r.kind,
      name: tr(r.name),
      summary: tr(r.summary),
      stages: r.stages,
      audience: r.audience ?? 'rd',
      scope: r.scope,
      policy: r.policy,
      origin: r.origin,
      version: r.version,
      enabled: r.policy === 'required' || (enabled.get(r.code) ?? false),
      installed_people: r.people,
      ...(r.secrets ? { secrets: secretNeeds(r) } : {}),
      ...(r.delivery ? { delivery: r.delivery } : {}),
    })
    const ref = (code: string): Schemas['ResourceRef'] => {
      const r = R.find((x) => x.code === code)
      return { code, kind: r?.kind ?? 'skill', name: r ? tr(r.name) : code }
    }
    const fbItem = (f: Fb): Schemas['FeedbackListItem'] => ({
      number: f.number,
      title: tr(f.title),
      type: f.type,
      origin: f.origin,
      status: f.status,
      severity: f.severity,
      ...(f.submitter ? { submitter: f.submitter } : {}),
      ...(f.assignee ? { assignee: f.assignee } : {}),
      ...(f.resource ? { resource: ref(f.resource), resource_version: f.version ?? 1 } : {}),
      ...(f.score ? { score: f.score } : {}),
      created_at: ago(f.at),
      updated_at: f.events.at(-1)?.at ?? ago(f.at),
    })
    const fbFull = (f: Fb): Schemas['Feedback'] => ({
      ...fbItem(f),
      body: tr(f.body),
      context: f.context,
      events: f.events,
      ...(f.note ? { score_note: f.note } : {}),
    })
    const canSeeFb = (f: Fb) =>
      who === 'admin' ||
      f.submitter?.id === self[who].id ||
      (who === 'owner' && f.resource !== undefined && R.find((r) => r.code === f.resource)?.scope === 'pay')
    const findFb = (n: string) => fbs.find((f) => String(f.number) === n && canSeeFb(f))

    switch (route) {
      case 'GET /api/v1/stages':
        return json(200, {
          items: STAGES.map(([id, name, d]) => ({ id, name: tr(name), description: tr(d), hidden: false })),
        })
      case 'GET /api/v1/me/join-requests':
        return json(200, { items: joins })
      case 'POST /api/v1/me/join-requests': {
        const b = await body<Schemas['CreateJoinRequestsRequest']>()
        const made = b.products.map((code, i) => ({
          id: ID(500 + joins.length + i),
          user: self[who],
          product: PRODUCTS.find((p) => p.code === code) ?? { code, name: code },
          ...(b.note ? { note: b.note } : {}),
          status: 'pending' as const,
          created_at: new Date().toISOString(),
        }))
        joins.unshift(...made)
        return json(201, { items: made })
      }
      case 'GET /api/v1/me/devices':
        return json(200, { items: devices })
      case 'POST /api/v1/me/sign-out-all':
        devices = []
        return new Response(null, { status: 204 })
      case 'GET /api/v1/me/tokens':
        return json(200, { items: tokens })
      case 'POST /api/v1/me/tokens': {
        const b = await body<Schemas['CreateAccessTokenRequest']>()
        if (!b.name.trim())
          return problem(400, 'request.invalid', zh ? '请填写名称。' : 'A name is required.')
        const tok: Schemas['AccessToken'] = {
          id: ID(310 + tokens.length),
          name: b.name,
          prefix: 'rgd_Zx8k',
          scope: b.scope,
          created_at: new Date().toISOString(),
          expires_at: later(b.expires_in_days),
        }
        tokens = [tok, ...tokens]
        return json(201, { token: '<placeholder: rgd_ token, shown once>', access_token: tok })
      }
      case 'GET /api/v1/me/secrets':
        return json(200, {
          items: SECRETS.map((s) => {
            const on = configured.get(s.name) ?? false
            const exp = expires.get(s.name)
            return {
              name: s.name,
              label: tr(s.label),
              howto: tr(s.howto),
              used_by: R.filter((r) => r.secrets?.includes(s.name) && visible(who).includes(r)).map((r) =>
                ref(r.code),
              ),
              configured: on,
              ...(on ? { updated_at: ago(50_000) } : {}),
              ...(on && exp ? { expires_at: later(exp) } : {}),
            }
          }).filter((s) => s.used_by.length > 0),
        })
      case 'GET /api/v1/catalog': {
        const kind = q.get('kind')
        const text = (q.get('q') ?? '').toLowerCase()
        return json(200, {
          items: visible(who)
            .filter((r) => !kind || r.kind === kind)
            .filter((r) => !text || (tr(r.name) + r.code + tr(r.summary)).toLowerCase().includes(text))
            .map(item),
        })
      }
      case 'GET /api/v1/installs/summary':
        if (who === 'member') return problem(403, 'access.denied')
        return json(200, {
          people_connected: 179,
          devices_active: 231,
          required_complete: { people: 154, of: 179 },
          by_tool: [
            { tool: 'claude-code', people: 164 },
            { tool: 'codex', people: 71 },
            { tool: 'opencode', people: 23 },
          ],
          cli_versions: [
            { version: '0.5.0', people: 171 },
            { version: '0.4.2', people: 8 },
          ],
        })
      case 'GET /api/v1/cli/downloads':
        return json(200, {
          version: '0.5.0',
          install_sh: `${location.origin}/install.sh`,
          install_ps1: `${location.origin}/install.ps1`,
          items: [
            {
              os: 'darwin',
              arch: 'arm64',
              file: 'rgd-darwin-arm64',
              url: `${location.origin}/downloads/rgd-darwin-arm64`,
              sha256: '5b7c9d1e3f5a7b9c1d3e5f7a9b1c4f9a5c0d7e2b1a3c6d8e0f2a4b6c8d0e1f3a',
              size: 9437184,
            },
            {
              os: 'linux',
              arch: 'amd64',
              file: 'rgd-linux-amd64',
              url: `${location.origin}/downloads/rgd-linux-amd64`,
              sha256: '1c4f9a5c0d7e2b1a3c6d8e0f2a4b6c8d0e1f3a5b7c9d1e3f5a7b9c1d3e5f7a9b',
              size: 9830400,
            },
            {
              os: 'windows',
              arch: 'amd64',
              file: 'rgd-windows-amd64.exe',
              url: `${location.origin}/downloads/rgd-windows-amd64.exe`,
              sha256: '3c6d8e0f2a4b6c8d0e1f3a5b7c9d1e3f5a7b9c1d3e5f7a9b1c4f9a5c0d7e2b1a',
              size: 9830400,
            },
          ],
        })
      case 'GET /api/v1/feedback': {
        const status = q.get('status')
        const list = fbs
          .filter(canSeeFb)
          .filter((f) => q.get('mine') !== 'true' || f.submitter?.id === self[who].id)
          .filter((f) => !status || f.status === status)
          .sort((a, b) => a.at - b.at)
        const limit = Number(q.get('limit') ?? '50')
        return json(200, { items: list.slice(0, limit).map(fbItem) })
      }
      case 'GET /api/v1/feedback/summary': {
        const list = fbs
          .filter(canSeeFb)
          .filter((f) => q.get('mine') !== 'true' || f.submitter?.id === self[who].id)
        const by = { new: 0, accepted: 0, in_progress: 0, resolved: 0, wont_fix: 0 }
        for (const f of list) by[f.status]++
        return json(200, { open: by.new + by.accepted + by.in_progress, by_status: by })
      }
      case 'POST /api/v1/feedback': {
        const b = await body<Schemas['CreateFeedbackRequest']>()
        const f: Fb = {
          number: nextNumber++,
          title: [b.title, b.title],
          body: [b.body, b.body],
          type: b.type,
          origin: 'web',
          status: 'new',
          severity: b.severity ?? 'medium',
          submitter: self[who],
          ...(b.resource ? { resource: b.resource, version: b.resource_version ?? 1 } : {}),
          at: 0,
          context: b.context ?? {},
          events: [
            { id: ID(900 + nextNumber), kind: 'created', by: self[who], at: new Date().toISOString() },
          ],
        }
        fbs.unshift(f)
        return json(201, fbFull(f))
      }
    }

    let m: RegExpExecArray | null
    if ((m = /^\/api\/v1\/catalog\/([a-z0-9-]+)$/.exec(path)) && request.method === 'GET') {
      const r = visible(who).find((x) => x.code === m?.[1])
      if (!r) return problem(404, 'object.not_found')
      const it = item(r)
      const files =
        r.kind === 'skill'
          ? [
              { path: 'SKILL.md', size: 1840 },
              { path: 'references/checklist.md', size: 512 },
            ]
          : r.kind === 'agent'
            ? [
                { path: 'agent.yaml', size: 320 },
                { path: 'AGENT.md', size: 2100 },
              ]
            : r.kind === 'workflow'
              ? [{ path: `${r.code}.yaml`, size: 860 }]
              : []
      const res: Schemas['CatalogResource'] = {
        ...it,
        ...(r.try ? { try: tr(r.try) } : {}),
        when: tr(r.summary),
        maintainers: [people.gyuan],
        digest: 'sha256:4f9a5c0d7e2b1a3c6d8e0f2a4b6c8d0e1f3a5b7c9d1e3f5a7b9c1d3e5f7a9b1c',
        published_at: ago(30_000),
        files,
        definition: r.definition ?? {},
        references: (r.references ?? []).map(ref),
        referenced_by: R.filter((x) => x.references?.includes(r.code)).map((x) => ref(x.code)),
        ...(r.local ? { local_config: r.local.map(([name, d]) => ({ name, description: tr(d) })) } : {}),
        my_installs:
          it.enabled && devices.length > 0
            ? devices.flatMap((d) =>
                d.tools.map((tool) => ({
                  device_id: d.id,
                  device_name: d.name,
                  tool: tool.tool,
                  version: d.current ? r.version : Math.max(1, r.version - 1),
                  status: 'installed' as const,
                  degraded: r.kind === 'workflow' && tool.tool !== 'claude-code',
                  ...(r.kind === 'workflow' && tool.tool !== 'claude-code' ? { form: 'skill' } : {}),
                })),
              )
            : [],
      }
      return json(200, res)
    }
    if ((m = /^\/api\/v1\/catalog\/([a-z0-9-]+)\/file$/.exec(path))) {
      const r = visible(who).find((x) => x.code === m?.[1])
      if (!r) return problem(404, 'object.not_found')
      const file = q.get('path') ?? ''
      const name = tr(r.name)
      const text =
        file === 'SKILL.md'
          ? `---\nname: ${r.code}\ndescription: ${tr(r.summary)}\n---\n\n# ${name}\n\n## Before you start\n- Make sure the current directory is the project root\n- Ask when information is missing; do not guess\n\n## Steps\n1. Read the input: the task, document or code location given\n2. Work through references/checklist.md item by item\n3. Present the result in the format under "Output", marking what is uncertain\n4. Check once more and list what was not done\n\n## Output\n- Conclusion first, evidence after\n- Every piece of evidence with its source (file and line, task number)\n\n## When you cannot go on\nStop, say where and what is missing; do not skip.\n`
          : file === 'references/checklist.md'
            ? `# ${name} checklist\n\n- [ ] Input complete: source, scope and deadline are clear\n- [ ] Every conclusion matches a piece of evidence\n- [ ] No accounts, passwords or internal addresses written down\n- [ ] Uncertain points listed separately for a person to confirm\n`
            : `# ${file}\n\n${tr(r.summary)}\n`
      return new Response(text, { status: 200, headers: { 'Content-Type': 'application/octet-stream' } })
    }
    if ((m = /^\/api\/v1\/catalog\/([a-z0-9-]+)\/preference$/.exec(path)) && request.method === 'PUT') {
      const r = visible(who).find((x) => x.code === m?.[1])
      if (!r) return problem(404, 'object.not_found')
      const b = await body<Schemas['PreferenceRequest']>()
      if (r.policy === 'required' && !b.enabled) {
        return problem(
          409,
          'resource.required',
          zh
            ? `${tr(r.name)} 是必装资源，不能停用。`
            : `${tr(r.name)} is required in your scope and cannot be turned off.`,
        )
      }
      enabled.set(r.code, b.enabled)
      return json(200, { code: r.code, enabled: b.enabled, policy: r.policy })
    }
    if ((m = /^\/api\/v1\/me\/secrets\/([A-Z0-9_]+)$/.exec(path))) {
      const s = SECRETS.find((x) => x.name === m?.[1])
      if (!s) return problem(404, 'object.not_found')
      if (request.method === 'DELETE') {
        configured.set(s.name, false)
        return new Response(null, { status: 204 })
      }
      const b = await body<Schemas['SetSecretRequest']>()
      if (!b.value) return problem(400, 'request.invalid')
      configured.set(s.name, true)
      expires.set(
        s.name,
        b.expires_at ? Math.round((Date.parse(b.expires_at) - Date.now()) / 86_400_000) : undefined,
      )
      return json(200, {
        name: s.name,
        label: tr(s.label),
        howto: tr(s.howto),
        used_by: [],
        configured: true,
        updated_at: new Date().toISOString(),
      })
    }
    if ((m = /^\/api\/v1\/me\/devices\/(.+)$/.exec(path)) && request.method === 'DELETE') {
      devices = devices.filter((d) => d.id !== m?.[1])
      return new Response(null, { status: 204 })
    }
    if ((m = /^\/api\/v1\/me\/tokens\/(.+)$/.exec(path)) && request.method === 'DELETE') {
      tokens = tokens.filter((x) => x.id !== m?.[1])
      return new Response(null, { status: 204 })
    }
    if ((m = /^\/api\/v1\/feedback\/(\d+)$/.exec(path))) {
      const f = findFb(m[1] ?? '')
      if (!f) return problem(404, 'object.not_found')
      if (request.method === 'PATCH') {
        if (who === 'member') return problem(403, 'access.denied')
        const b = await body<Schemas['UpdateFeedbackRequest']>()
        if (b.status === 'wont_fix' && !b.status_reason)
          return problem(
            400,
            'feedback.reason_required',
            zh ? '标为不处理要写明理由。' : 'Say why it will not be fixed.',
          )
        const now = new Date().toISOString()
        if (b.status) {
          f.status = b.status
          f.events.push({
            id: ID(950 + f.events.length),
            kind: 'status',
            by: self[who],
            status: b.status,
            ...(b.status_reason ? { body: b.status_reason } : {}),
            at: now,
          })
        }
        if (b.assignee) {
          const a = Object.values(people).find((p) => p.id === b.assignee)
          if (a) {
            f.assignee = a
            f.events.push({
              id: ID(960 + f.events.length),
              kind: 'assigned',
              by: self[who],
              assignee: a,
              at: now,
            })
          }
        }
        return json(200, fbFull(f))
      }
      return json(200, fbFull(f))
    }
    if ((m = /^\/api\/v1\/feedback\/(\d+)\/comments$/.exec(path))) {
      const f = findFb(m[1] ?? '')
      if (!f) return problem(404, 'object.not_found')
      const b = await body<Schemas['CommentRequest']>()
      const ev: Schemas['FeedbackEvent'] = {
        id: ID(970 + f.events.length),
        kind: 'comment',
        by: self[who],
        body: b.body,
        at: new Date().toISOString(),
      }
      f.events.push(ev)
      return json(201, ev)
    }
    if ((m = /^\/api\/v1\/feedback\/(\d+)\/rate$/.exec(path))) {
      const f = findFb(m[1] ?? '')
      if (!f) return problem(404, 'object.not_found')
      if (f.status !== 'resolved') return problem(409, 'feedback.not_resolved')
      const b = await body<Schemas['RateRequest']>()
      f.score = b.score
      if (b.note) f.note = b.note
      f.events.push({
        id: ID(980 + f.events.length),
        kind: 'rated',
        by: self[who],
        score: b.score,
        at: new Date().toISOString(),
      })
      if (b.score <= 2) {
        f.status = 'accepted'
        f.events.push({ id: ID(990 + f.events.length), kind: 'reopened', at: new Date().toISOString() })
      }
      return json(200, fbFull(f))
    }
    return problem(404, 'mock.not_implemented')
  }
}
