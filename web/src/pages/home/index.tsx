// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// A-01 工作台 (the prototype's V.home): who I am and what I follow, the four figures for administrators and
// product owners, the first-steps guide, resources by stage, my connection and my feedback. A fresh
// deployment without resources gets the next steps instead. "等你确认", "任务时推荐" and the figures that
// come from usage data belong to M2 (docs/architecture/07-web-console.md).

import type { ComponentChildren } from 'preact'
import { useState } from 'preact/hooks'
import { useLocation } from 'preact-iso'
import { ApiError, api, unwrap, type Schemas } from '../../api/client'
import { BRAND } from '../../brand.gen'
import { fmtNumber, fmtRelative } from '../../i18n/format'
import { t } from '../../i18n'
import { href } from '../../router/paths'
import { me, role } from '../../shell/session'
import { Empty } from '../../ui/empty'
import { StatusBadge, fbNumber } from '../../ui/feedback'
import { Icon } from '../../ui/icons'
import { KindShape } from '../../ui/kind-shape'
import { LoadError, Loading, useLoad } from '../../ui/load'
import { KINDS, KindIcon, codeLabel, kindName, stageName, toolName, useStages } from '../../ui/resource'
import { AiTip, Cmd, SelfTip, StatLi } from '../../ui/say'
import { toast } from '../../ui/toast'
import { pageCode } from '../registry'
import { InstallBadge, clickable } from '../catalog'
import { ResourceDrawer } from '../catalog/drawer'
import '../catalog/catalog.css'
import { quickInstall } from '../connect/commands'
import { JoinDialog, ProfileDialog } from './dialogs'
import './home.css'

type Item = Schemas['CatalogItem']

interface HomeData {
  catalog: Item[]
  devices: Schemas['Device'][] | null
  secrets: Schemas['MySecret'][]
  joins: Schemas['JoinRequest'][]
  downloads: Schemas['CLIDownloadList'] | null
}

const optional = <T,>(p: Promise<{ data?: T }>): Promise<T | null> =>
  p.then(
    (r) => r.data ?? null,
    () => null,
  )

async function loadHome(): Promise<HomeData> {
  const [catalog, devices, secrets, joins, downloads] = await Promise.all([
    api.GET('/api/v1/catalog').then(unwrap),
    optional(api.GET('/api/v1/me/devices')),
    optional(api.GET('/api/v1/me/secrets')),
    optional(api.GET('/api/v1/me/join-requests')),
    optional(api.GET('/api/v1/cli/downloads')),
  ])
  return {
    catalog: catalog.items,
    devices: devices?.items ?? null,
    secrets: secrets?.items ?? [],
    joins: joins?.items ?? [],
    downloads,
  }
}

/** A per-person flag in this browser (guide dismissed, first sentence tried …). */
function useFlag(name: string): [boolean, () => void] {
  const key = `home.${name}.${me.value?.id ?? ''}`
  const read = () => {
    try {
      return localStorage.getItem(key) === '1'
    } catch {
      return false
    }
  }
  const [on, setOn] = useState(read)
  return [
    on,
    () => {
      setOn(true)
      try {
        localStorage.setItem(key, '1')
      } catch {
        // Kept for this page only.
      }
    },
  ]
}

function hello(name: string): string {
  const h = new Date().getHours()
  if (h < 6) return t('home.hello.night', { name })
  if (h < 12) return t('home.hello.morning', { name })
  if (h < 14) return t('home.hello.noon', { name })
  if (h < 18) return t('home.hello.afternoon', { name })
  return t('home.hello.evening', { name })
}

function Eyebrow() {
  const code = pageCode('home')
  if (!code) return null
  return (
    <div class="eyebrow">
      <b>
        {code.letter}
        <i>-</i>
        {code.number}
      </b>
      <span class="tag">
        {t(code.group)} / {t(code.label)}
      </span>
    </div>
  )
}

export default function Home() {
  useStages()
  const data = useLoad(loadHome)
  const [res, setRes] = useState<string | null>(null)
  const person = me.value
  if (!person) return null

  if (data.state === 'loading' || data.state === 'failed') {
    return (
      <>
        <div class="hero">
          <div>
            <Eyebrow />
            <h1>{hello(person.display_name)}</h1>
          </div>
        </div>
        <div class="card">
          {data.state === 'loading' ? <Loading /> : <LoadError error={data.error} retry={data.reload} />}
        </div>
      </>
    )
  }

  const d = data.data
  const fresh = d.devices !== null && d.devices.length === 0
  const manager = role.value !== 'member'
  if (d.catalog.length === 0 && manager) return <HomeEmpty />

  return (
    <>
      <Hero data={d} fresh={fresh} reload={data.reload} />
      {manager && !fresh && <Pillars catalog={d.catalog} />}
      <Guide data={d} fresh={fresh} />
      <div class="grid g-main">
        <Browse catalog={d.catalog} fresh={fresh} open={setRes} />
        <div class="grid" style={{ alignContent: 'start' }}>
          <Connection devices={d.devices} fresh={fresh} />
          <MyFeedback />
        </div>
      </div>
      {res && <ResourceDrawer code={res} fresh={fresh} onClose={() => setRes(null)} />}
    </>
  )
}

function Hero(props: { data: HomeData; fresh: boolean; reload: () => void }) {
  const person = me.value
  const [dialog, setDialog] = useState<'profile' | 'join' | null>(null)
  if (!person) return null
  const pending = props.data.joins.filter((j) => j.status === 'pending')
  return (
    <div class="hero">
      <div>
        <Eyebrow />
        <h1>{hello(person.display_name)}</h1>
        <p class="hero-sub">{props.fresh ? t('home.sub.fresh') : t('home.sub')}</p>
        <div class="hero-tags">
          <span>
            <span class="lbl" style={{ marginRight: '4px' }}>
              {t('home.tags.focus')}
            </span>
            {person.focus_stages.length === 0 && <span class="faint">{t('home.tags.focus.none')}</span>}
            {person.focus_stages.map((s) => (
              <span key={s} class="badge brand">
                {stageName(s)}
              </span>
            ))}
            <button
              type="button"
              class="btn ghost icon"
              onClick={() => setDialog('profile')}
              aria-label={t('home.tags.focus.edit')}
              title={t('home.tags.focus.edit')}
            >
              <Icon name="edit" />
            </button>
          </span>
          <span>
            <span class="lbl" style={{ marginRight: '4px' }}>
              {t('home.tags.products')}
            </span>
            {person.memberships.length === 0 && pending.length === 0 && (
              <span class="faint">{t('home.tags.products.none')}</span>
            )}
            {person.memberships.map((m) => (
              <span key={m.product.code} class="badge">
                {m.product.name}
              </span>
            ))}
            {pending.map((j) => (
              <span key={j.id} class="badge warn">
                <span class="dot pulse" />
                {t('home.tags.pending', { name: j.product.name })}
              </span>
            ))}
            <button
              type="button"
              class="btn ghost icon"
              onClick={() => setDialog('join')}
              aria-label={t('home.join.title')}
              title={t('home.join.title')}
            >
              <Icon name="plus" />
            </button>
          </span>
        </div>
      </div>
      {props.fresh && (
        <div class="row">
          <a class="btn lg" href={href('/connect')}>
            <Icon name="plug" />
            {t('home.connect')}
          </a>
        </div>
      )}
      {dialog === 'profile' && <ProfileDialog onClose={() => setDialog(null)} />}
      {dialog === 'join' && (
        <JoinDialog
          pending={pending.map((j) => j.product.code)}
          onClose={() => setDialog(null)}
          onDone={props.reload}
        />
      )}
    </div>
  )
}

// ---- 四项核心指标 (administrators and product owners) ----

function Pillars(props: { catalog: Item[] }) {
  const { route } = useLocation()
  const figures = useLoad(async () => {
    const [installs, feedback, adapters] = await Promise.all([
      optional(api.GET('/api/v1/installs/summary')),
      optional(api.GET('/api/v1/feedback/summary')),
      role.value === 'admin' ? optional(api.GET('/api/v1/adapters')) : Promise.resolve(null),
    ])
    return { installs, feedback, adapters }
  })
  const f = figures.data
  const byKind = KINDS.map((k) => [k, props.catalog.filter((r) => r.kind === k).length] as const).filter(
    ([, n]) => n,
  )
  const required = props.catalog.filter((r) => r.policy === 'required').length
  const workflows = props.catalog.filter((r) => r.kind === 'workflow').length
  const rc = f?.installs?.required_complete
  const pct = rc && rc.of > 0 ? Math.round((rc.people / rc.of) * 100) : null
  const tools = f?.adapters
    ? f.adapters.items.filter((a) => a.enabled).map((a) => a.name)
    : (f?.installs?.by_tool ?? []).map((x) => toolName(x.tool))
  const open = f?.feedback?.open
  const byStatus = f?.feedback?.by_status
  const none = <span class="faint">—</span>

  const pillar = (
    n: number,
    title: string,
    value: ComponentChildren,
    unit: string,
    sub: string,
    to: string,
    art: ComponentChildren,
  ) => (
    <button type="button" class="pillar" onClick={() => route(href(to))}>
      <span class="t">
        <b>{String(n).padStart(2, '0')}</b>
        <span class="lbl" style={{ color: 'var(--ink-2)' }}>
          {title}
        </span>
        <Icon name="out" />
      </span>
      <span class="vrow">
        <span class="v">
          {value}
          {value !== none && <small>{unit}</small>}
        </span>
        {art}
      </span>
      <span class="s">{sub}</span>
    </button>
  )

  return (
    <div class="pillars">
      {pillar(
        1,
        t('home.pillar.resources'),
        fmtNumber(props.catalog.length),
        t('home.pillar.resources.unit'),
        t('home.pillar.resources.sub', { kinds: byKind.length, required }),
        role.value === 'member' ? '/catalog' : '/manage/resources',
        <span class="compo" title={byKind.map(([k, n]) => `${kindName(k)} ${n}`).join(t('ui.list_sep'))}>
          {byKind.map(([k, n]) => (
            <i key={k} data-k={k} style={{ flex: n }} />
          ))}
        </span>,
      )}
      {pillar(
        2,
        t('home.pillar.flow'),
        pct === null ? none : pct,
        '%',
        t('home.pillar.flow.sub', { count: workflows }),
        '/catalog',
        <span
          class="cells"
          title={rc ? t('home.pillar.flow.title', { people: rc.people, of: rc.of }) : undefined}
        >
          {Array.from({ length: 20 }, (_, i) => (
            <i key={i} class={pct !== null && i < Math.round(pct / 5) ? 'on' : undefined} />
          ))}
        </span>,
      )}
      {pillar(
        3,
        t('home.pillar.feedback'),
        open === undefined ? none : open,
        t('home.pillar.feedback.unit'),
        byStatus
          ? t('home.pillar.feedback.sub', { count: byStatus.resolved })
          : t('home.pillar.feedback.sub.none'),
        '/feedback',
        <span class="compo">
          {byStatus &&
            (['new', 'accepted', 'in_progress', 'resolved'] as const).map((s) => (
              <i key={s} class={`fb-${s}`} style={{ flex: byStatus[s] || 0 }} />
            ))}
        </span>,
      )}
      {pillar(
        4,
        t('home.pillar.tools'),
        tools.length ? tools.length : none,
        t('home.pillar.tools.unit'),
        f?.installs
          ? t('home.pillar.tools.sub', { people: f.installs.people_connected })
          : t('home.pillar.tools.sub.none'),
        role.value === 'admin' ? '/admin/tools' : '/connect',
        <span class="bays8" title={tools.join(t('ui.list_sep'))}>
          {tools.map((name) => (
            <i key={name} />
          ))}
        </span>,
      )}
    </div>
  )
}

// ---- 新手指引 ----

function Guide(props: { data: HomeData; fresh: boolean }) {
  const person = me.value
  const [off, dismiss] = useFlag('guide.off')
  const [tried, setTried] = useFlag('guide.tried')
  const [used, setUsed] = useFlag('guide.used')
  if (!person) return null
  const { devices, secrets, catalog } = props.data
  const f = props.fresh
  const tools = [...new Set((devices ?? []).flatMap((x) => x.tools.map((tool) => toolName(tool.tool))))]
  const lastSync = (devices ?? [])
    .map((x) => x.last_sync?.at)
    .filter((x): x is string => !!x)
    .sort()
    .at(-1)
  const missing = secrets.filter((s) => !s.configured)
  const steps: [string, string, boolean][] = [
    [
      t('home.guide.s1'),
      tools.length
        ? t('home.guide.s1.done', { tools: tools.join(t('ui.list_sep')) })
        : t('home.guide.s1.todo'),
      !f && tools.length > 0,
    ],
    [
      t('home.guide.s2'),
      f || !devices
        ? t('home.guide.s2.todo')
        : t('home.guide.s2.done', {
            count: devices.length,
            when: lastSync ? fmtRelative(lastSync) : t('home.guide.s2.never'),
          }),
      !f && devices !== null,
    ],
    [t('home.guide.s3'), t('home.guide.s3.desc'), !f && tried],
    [
      t('home.guide.s4'),
      missing.length ? t('home.guide.s4.todo', { count: missing.length }) : t('home.guide.s4.done'),
      !f && missing.length === 0,
    ],
    [
      t('home.guide.s5'),
      t('home.guide.s5.desc', { stages: person.focus_stages.map(stageName).join(t('ui.list_sep')) || '—' }),
      !f && used,
    ],
  ]
  const done = steps.filter((s) => s[2]).length
  if (off || done === steps.length) {
    if (missing.length === 0 || f) return null
    return (
      <div class="callout mb">
        <Icon name="key" />
        <strong style={{ color: 'var(--warn)' }}>{t('home.missing.title', { count: missing.length })}</strong>
        <span class="dim grow">
          {t('home.missing.why', {
            used: [...new Set(missing.flatMap((m) => m.used_by.map((u) => u.name)))].join(t('ui.list_sep')),
          })}
        </span>
        <a class="btn" href={href('/keys')}>
          {t('home.missing.go')}
          <span class="arr">
            <Icon name="arrow" />
          </span>
        </a>
      </div>
    )
  }
  const cur = f ? 1 : steps.findIndex((s) => !s[2])
  const tryRes = catalog.find((r) => r.stages.some((s) => person.focus_stages.includes(s)) && r.enabled)
  const body = [
    () => <p class="dim t13">{t('home.guide.b1')}</p>,
    () => (
      <>
        <p class="dim t13">{t('home.guide.b2', { cli: BRAND.cli })}</p>
        <Cmd text={quickInstall(props.data.downloads)} />
        <div class="row">
          <a class="btn primary" href={href('/connect')}>
            {t('home.guide.b2.go')}
            <span class="arr">
              <Icon name="arrow" />
            </span>
          </a>
          <span class="faint t12">{t('home.guide.b2.time')}</span>
        </div>
      </>
    ),
    () => (
      <>
        <p class="dim t13">{t('home.guide.b3')}</p>
        <AiTip say={t('home.guide.say')} label={t('home.guide.say.label')} />
        <div class="row">
          <span class="faint t12">{t('home.guide.b3.note')}</span>
          <button type="button" class="btn ghost" onClick={setTried}>
            {t('home.guide.tried')}
          </button>
        </div>
      </>
    ),
    () => (
      <>
        <p class="dim t13">
          {t('home.guide.b4', {
            used: [...new Set(missing.flatMap((m) => m.used_by.map((u) => u.name)))].join(t('ui.list_sep')),
          })}
        </p>
        <SelfTip>{t('home.guide.b4.self')}</SelfTip>
        <div class="row">
          <a class="btn primary" href={href('/keys')}>
            <Icon name="key" />
            {t('home.missing.go')}
          </a>
        </div>
      </>
    ),
    () => (
      <>
        <p class="dim t13">{t('home.guide.b5')}</p>
        {tryRes && <TryTip r={tryRes} />}
        <div class="row">
          <a class="btn" href={href('/catalog')}>
            {t('home.guide.b5.more')}
            <span class="arr">
              <Icon name="arrow" />
            </span>
          </a>
          <button type="button" class="btn ghost" onClick={setUsed}>
            {t('home.guide.used')}
          </button>
        </div>
      </>
    ),
  ][cur]
  const step = steps[cur]
  return (
    <section class="card guide" aria-label={t('home.guide.label')}>
      <div class="guide-l">
        <div class="guide-h">
          <span class="lbl">
            {t('home.guide.label')}{' '}
            <span class="num">{t('home.guide.progress', { done, all: steps.length })}</span>
          </span>
          {f && (
            <span class="badge warn">
              <span class="dot pulse" />
              {t('home.guide.not_connected')}
            </span>
          )}
          <span class="gbar" aria-hidden="true">
            {steps.map((s, i) => (
              <i key={i} class={s[2] ? 'on' : i === cur ? 'cur' : undefined} />
            ))}
          </span>
          <button
            type="button"
            class="btn ghost icon"
            onClick={() => {
              dismiss()
              toast(t('home.guide.dismissed'))
            }}
            aria-label={t('home.guide.dismiss')}
            title={t('home.guide.dismiss')}
          >
            <Icon name="x" />
          </button>
        </div>
        <strong style={{ fontSize: '17px', fontWeight: 600, letterSpacing: '-.01em' }}>
          {t('home.guide.step', { n: cur + 1, title: step?.[0] ?? '' })}
        </strong>
        {body?.()}
      </div>
      <div class="guide-r">
        <ol class="steps">
          {steps.map(([title, desc, ok], i) => (
            <li
              key={i}
              class={ok ? 'done' : i === cur ? 'cur' : undefined}
              aria-current={i === cur ? 'step' : undefined}
            >
              <span class="n">{ok ? <Icon name="check" /> : String(i + 1).padStart(2, '0')}</span>
              <div>
                <b>{title}</b>
                <span>{desc}</span>
              </div>
            </li>
          ))}
        </ol>
      </div>
    </section>
  )
}

/** TryTip is a resource's "试一试" sentence, loaded from its details. */
function TryTip(props: { r: Item }) {
  const detail = useLoad(
    async () => unwrap(await api.GET('/api/v1/catalog/{code}', { params: { path: { code: props.r.code } } })),
    [props.r.code],
  )
  const say = detail.data?.try
  if (!say) return null
  return (
    <AiTip say={say} label={t('catalog.detail.try')} note={t('home.guide.b5.from', { name: props.r.name })} />
  )
}

// ---- 按环节浏览 ----

function Browse(props: { catalog: Item[]; fresh: boolean; open: (code: string) => void }) {
  const person = me.value
  const stages = useStages().filter((s) => !s.hidden)
  const [aud, setAud] = useState<'rd' | 'mgmt'>('rd')
  const first = person?.focus_stages.find((s) => stages.some((x) => x.id === s)) ?? stages[0]?.id
  const [picked, setPicked] = useState<string | undefined>(undefined)
  const stage = picked ?? first
  const rd = props.catalog.filter((r) => r.audience !== 'mgmt')
  const mgmt = props.catalog.filter((r) => r.audience === 'mgmt')
  const list = aud === 'mgmt' ? mgmt : rd.filter((r) => stage !== undefined && r.stages.includes(stage))
  const kinds = [...KINDS, ...new Set(list.map((r) => r.kind).filter((k) => !KINDS.includes(k)))]
  const groups = kinds.map((k) => [k, list.filter((r) => r.kind === k)] as const).filter(([, l]) => l.length)
  const caption = stages.find((s) => s.id === stage)?.description
  const manager = role.value !== 'member'

  return (
    <section class="card pad" aria-label={t('home.browse.label')}>
      <div class="row" style={{ marginBottom: '14px', justifyContent: 'space-between' }}>
        <h3 style={{ margin: 0 }}>{t('home.browse.title')}</h3>
        <div class="seg">
          <button
            type="button"
            class={aud === 'rd' ? 'on' : undefined}
            aria-pressed={aud === 'rd'}
            onClick={() => setAud('rd')}
          >
            {t('home.browse.rd')} <span class="faint num">{rd.length}</span>
          </button>
          <button
            type="button"
            class={aud === 'mgmt' ? 'on' : undefined}
            aria-pressed={aud === 'mgmt'}
            onClick={() => setAud('mgmt')}
          >
            {t('home.browse.mgmt')} <span class="faint num">{mgmt.length}</span>
          </button>
        </div>
      </div>
      {aud === 'mgmt' ? (
        <p class="faint t12">{t('home.browse.mgmt.what')}</p>
      ) : (
        <>
          <div class="stages" role="tablist" aria-label={t('home.browse.stages')}>
            {stages.map((s, i) => (
              <button
                key={s.id}
                type="button"
                class={s.id === stage ? 'stg on' : 'stg'}
                role="tab"
                aria-selected={s.id === stage}
                onClick={() => setPicked(s.id)}
                title={s.description}
              >
                <small>
                  {String(i + 1).padStart(2, '0')}
                  {person?.focus_stages.includes(s.id) && <em>{t('home.browse.focus')}</em>}
                </small>
                <b>{s.name}</b>
                <span class="c">
                  {t('home.browse.count', { count: rd.filter((r) => r.stages.includes(s.id)).length })}
                </span>
              </button>
            ))}
          </div>
          {caption && (
            <p class="faint t12" style={{ marginTop: '8px' }}>
              {caption}
            </p>
          )}
        </>
      )}
      {groups.length ? (
        groups.map(([k, l]) => (
          <div key={k}>
            <div class="kgroup" data-k={k}>
              <KindShape kind={k} />
              {kindName(k)}
              <span class="n">{l.length}</span>
            </div>
            <div class="list rows">
              {l.map((r) => (
                <div
                  key={r.code}
                  class="li"
                  role="button"
                  style={{ cursor: 'pointer' }}
                  aria-label={t('catalog.open', { name: r.name })}
                  {...clickable(() => props.open(r.code))}
                >
                  <KindIcon kind={r.kind} />
                  <div class="grow">
                    <div class="name">
                      {r.name}
                      <span class="mono">{codeLabel(r)}</span>
                    </div>
                    <div class="desc">{r.summary}</div>
                  </div>
                  <InstallBadge r={r} fresh={props.fresh} />
                  <span class="chev">
                    <Icon name="chev" />
                  </span>
                </div>
              ))}
            </div>
          </div>
        ))
      ) : (
        <Empty
          title={aud === 'mgmt' ? t('home.browse.empty.mgmt') : t('home.browse.empty')}
          why={manager ? t('home.browse.empty.why.manager') : t('home.browse.empty.why')}
        >
          {manager ? (
            <a class="btn" href={href('/manage/resources')}>
              <Icon name="plus" />
              {t('home.browse.empty.manage')}
            </a>
          ) : (
            <a class="btn" href={href('/catalog')}>
              <Icon name="box" />
              {t('home.browse.empty.catalog')}
            </a>
          )}
        </Empty>
      )}
    </section>
  )
}

// ---- 接入状态 ----

function Connection(props: { devices: Schemas['Device'][] | null; fresh: boolean }) {
  const devices = props.devices ?? []
  const latest = [...devices]
    .sort((a, b) => (a.last_sync?.at ?? '').localeCompare(b.last_sync?.at ?? ''))
    .at(-1)
  const tools = [...new Set(devices.flatMap((x) => x.tools.map((tool) => toolName(tool.tool))))]
  const versions = [...new Set(devices.map((x) => x.cli_version))]
  return (
    <section class="card pad">
      <h3>
        {t('home.conn.title')}
        <a
          class="btn ghost icon"
          href={href('/connect')}
          aria-label={t('home.conn.guide')}
          title={t('home.conn.guide')}
        >
          <Icon name="chev" />
        </a>
      </h3>
      <div>
        {props.devices === null ? (
          <p class="faint t13">{t('home.conn.unknown')}</p>
        ) : props.fresh ? (
          <>
            <StatLi label={t('home.conn.cli')} tone="warn">
              {t('home.conn.cli.none')}
            </StatLi>
            <StatLi label={t('home.conn.tools')} tone="off">
              {t('home.conn.tools.later')}
            </StatLi>
            <StatLi label={t('home.conn.last')} tone="off">
              {t('home.conn.last.never')}
            </StatLi>
          </>
        ) : (
          <>
            <StatLi label={t('home.conn.cli')} tone="ok">
              {t('home.conn.cli.on')} · <span class="mono">{versions.map((v) => 'v' + v).join(' · ')}</span>
            </StatLi>
            <StatLi label={t('home.conn.devices')}>
              {t('home.conn.devices.count', { count: devices.length })}
            </StatLi>
            <StatLi label={t('home.conn.tools')}>
              {tools.length ? tools.join(' · ') : t('home.conn.tools.none')}
            </StatLi>
            <StatLi
              label={t('home.conn.last')}
              tone={
                latest?.last_sync?.result === 'failed'
                  ? 'bad'
                  : latest?.last_sync?.result === 'partial'
                    ? 'warn'
                    : undefined
              }
            >
              {latest?.last_sync
                ? t('home.conn.last.at', {
                    when: fmtRelative(latest.last_sync.at),
                    count: latest.last_sync.changes ?? 0,
                  })
                : t('home.conn.last.never')}
            </StatLi>
          </>
        )}
      </div>
    </section>
  )
}

// ---- 我的反馈 ----

function MyFeedback() {
  const list = useLoad(
    async () =>
      unwrap(await api.GET('/api/v1/feedback', { params: { query: { mine: true, limit: 2 } } })).items,
  )
  return (
    <section class="card pad">
      <h3>
        {t('home.fb.title')}
        <a
          class="btn ghost icon"
          href={href('/feedback')}
          aria-label={t('home.fb.all')}
          title={t('home.fb.all')}
        >
          <Icon name="chev" />
        </a>
      </h3>
      {list.state === 'loading' && <Loading />}
      {list.state === 'failed' && (
        <p class="faint t13" role="alert">
          {list.error instanceof ApiError && list.error.status === 403
            ? t('ui.load.denied.title')
            : t('home.fb.failed')}{' '}
          <button type="button" class="link" onClick={list.reload}>
            {t('ui.retry')}
          </button>
        </p>
      )}
      {list.data && list.data.length === 0 && <p class="faint t13">{t('home.fb.none')}</p>}
      {list.data && list.data.length > 0 && (
        <div class="list rows">
          {list.data.map((x) => (
            <a
              key={x.number}
              class="li"
              href={href(`/feedback/${x.number}`)}
              style={{ color: 'inherit', textDecoration: 'none' }}
            >
              <div class="grow">
                <div class="name" style={{ fontWeight: 400 }}>
                  {x.title}
                </div>
                <div class="desc">
                  <span class="mono">{fbNumber(x.number)}</span> · {fmtRelative(x.created_at)}
                </div>
              </div>
              <StatusBadge status={x.status} />
            </a>
          ))}
        </div>
      )}
    </section>
  )
}

// ---- 刚部署、没有资源 ----

function HomeEmpty() {
  const person = me.value
  const admin = role.value === 'admin'
  const steps: [string, string, ComponentChildren][] = [
    [
      t('home.empty.s1'),
      t('home.empty.s1.desc'),
      <>
        {admin && (
          <a class="btn primary" href={href('/admin/settings')}>
            <Icon name="download" />
            {t('home.empty.s1.repo')}
          </a>
        )}
        <a class="btn" href={href('/manage/resources')}>
          {t('home.empty.s1.manage')}
        </a>
      </>,
    ],
    [
      t('home.empty.s2'),
      t('home.empty.s2.desc'),
      <>
        <a class="btn" href={href('/products')}>
          <Icon name="users" />
          {t('nav.products')}
        </a>
        {admin && (
          <a class="btn" href={href('/admin/users')}>
            <Icon name="send" />
            {t('home.empty.s2.invite')}
          </a>
        )}
      </>,
    ],
    [
      t('home.empty.s3'),
      t('home.empty.s3.desc'),
      <a class="btn" href={href('/connect')}>
        <Icon name="plug" />
        {t('nav.connect')}
      </a>,
    ],
  ]
  return (
    <>
      <div class="hero">
        <div>
          <Eyebrow />
          <h1>{t('home.empty.hello', { name: person?.display_name ?? '' })}</h1>
          <p class="hero-sub">{t('home.empty.sub')}</p>
        </div>
      </div>
      <div class="pillars three">
        {steps.map(([title, desc, actions], i) => (
          <div key={i} class="pillar static">
            <span class="t">
              <b>{String(i + 1).padStart(2, '0')}</b>
              <span class="tag" style={{ color: 'var(--ink-2)' }}>
                {t('home.empty.next')}
              </span>
            </span>
            <strong style={{ fontSize: '15px' }}>{title}</strong>
            <span class="dim t13">{desc}</span>
            <span class="row" style={{ marginTop: '6px' }}>
              {actions}
            </span>
          </div>
        ))}
      </div>
      <div class="card">
        <Empty title={t('home.empty.title')} why={t('home.empty.why')}>
          <a class="btn" href={href('/manage/resources')}>
            <Icon name="plus" />
            {t('home.empty.new')}
          </a>
        </Empty>
      </div>
    </>
  )
}
