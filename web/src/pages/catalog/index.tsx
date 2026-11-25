// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// A-02 资源目录 (the prototype's V.catalog): everything published that the person can use, as cards or a
// table, filtered by kind and text; the switch turns an optional or recommended resource on or off for all
// of their computers; /catalog/<code> opens the details drawer over it. "我自己的" (personal resources) is
// M2 (docs/architecture/13-open-questions.md D11), and so are the usage figures and quality badges.

import type { ComponentChildren } from 'preact'
import { useState } from 'preact/hooks'
import { useLocation, useRoute } from 'preact-iso'
import { api, unwrap, type Schemas } from '../../api/client'
import { BRAND } from '../../brand.gen'
import { fmtNumber } from '../../i18n/format'
import { t } from '../../i18n'
import { href } from '../../router/paths'
import { loadMissingSecrets } from '../../shell/counts'
import { Empty } from '../../ui/empty'
import { Icon, type IconName } from '../../ui/icons'
import { KindShape } from '../../ui/kind-shape'
import { LoadError, Loading, useLoad } from '../../ui/load'
import { failText } from '../../ui/fail'
import { PageHeader } from '../../ui/page-header'
import {
  KINDS,
  KindIcon,
  KindTag,
  PolicyBadge,
  StageBadges,
  codeLabel,
  kindName,
  policyName,
  scopeName,
  useStages,
} from '../../ui/resource'
import { toast } from '../../ui/toast'
import { ResourceDrawer } from './drawer'
import './catalog.css'

type Item = Schemas['CatalogItem']
type View = 'card' | 'list'

const VIEW_KEY = 'catalog.view'

function storedView(): View {
  try {
    return localStorage.getItem(VIEW_KEY) === 'list' ? 'list' : 'card'
  } catch {
    return 'card'
  }
}

/** missingKey says whether the resource is on but waits for a personal secret of mine. */
export function missingKey(r: Item): boolean {
  return (r.secrets ?? []).some((s) => !s.configured)
}

/** InstallBadge is the state of a resource for me: not connected yet, waiting for a secret, on or off. */
export function InstallBadge(props: { r: Item; fresh: boolean }) {
  const { r } = props
  if (props.fresh) return <span class="badge">{t('catalog.state.after_connect')}</span>
  if (r.enabled && missingKey(r)) return <span class="badge warn">{t('catalog.state.no_key')}</span>
  if (r.enabled) return <span class="badge ok">{t('catalog.state.on')}</span>
  return <span class="badge">{t('catalog.state.off')}</span>
}

/** useCatalog loads the catalog and whether the person has connected a computer yet. */
export function useCatalog() {
  return useLoad(async () => {
    const [list, devices] = await Promise.all([
      api.GET('/api/v1/catalog').then(unwrap),
      // Only the "not connected yet" badges depend on this; a failure here does not fail the page.
      api.GET('/api/v1/me/devices').then(
        (res) => res.data?.items ?? null,
        () => null,
      ),
    ])
    return { items: list.items, fresh: devices !== null && devices.length === 0 }
  })
}

export default function Catalog() {
  const { route } = useLocation()
  const { params } = useRoute()
  useStages()
  const data = useCatalog()
  const [items, setItems] = useState<Item[] | null>(null)
  const [kind, setKind] = useState('all')
  const [q, setQ] = useState('')
  const [view, setView] = useState<View>(storedView)
  const [busy, setBusy] = useState<string | null>(null)

  const all = items ?? data.data?.items ?? []
  const fresh = data.data?.fresh ?? false
  const text = q.trim().toLowerCase()
  const shown = all.filter(
    (r) =>
      (kind === 'all' || r.kind === kind) &&
      (!text || (r.name + r.code + (r.summary ?? '')).toLowerCase().includes(text)),
  )
  const kinds = [
    ...KINDS.filter((k) => all.some((r) => r.kind === k)),
    ...new Set(all.map((r) => r.kind).filter((k) => !KINDS.includes(k))),
  ]

  const pickView = (v: View) => {
    setView(v)
    try {
      localStorage.setItem(VIEW_KEY, v)
    } catch {
      // Not remembered in a private window; the choice lasts for this page.
    }
  }

  const toggle = async (r: Item) => {
    if (busy) return
    const next = !r.enabled
    const update = (enabled: boolean) =>
      setItems((cur) => (cur ?? all).map((x) => (x.code === r.code ? { ...x, enabled } : x)))
    update(next)
    setBusy(r.code)
    try {
      unwrap(
        await api.PUT('/api/v1/catalog/{code}/preference', {
          params: { path: { code: r.code } },
          body: { enabled: next },
        }),
      )
      toast(
        next
          ? t('catalog.toggle.on', { name: r.name, cli: BRAND.cli })
          : t('catalog.toggle.off', { name: r.name }),
      )
      loadMissingSecrets()
    } catch (err) {
      update(!next)
      toast(failText(err))
    } finally {
      setBusy(null)
    }
  }

  const open = (code: string) => route(href(`/catalog/${code}`))
  const close = () => route(href('/catalog'))

  const header = <PageHeader page="catalog" title={t('nav.catalog')} lead={t('catalog.lead')} />

  if (data.state === 'loading') {
    return (
      <>
        {header}
        <div class="card">
          <Loading />
        </div>
      </>
    )
  }
  if (data.state === 'failed') {
    return (
      <>
        {header}
        <div class="card">
          <LoadError error={data.error} retry={data.reload} />
        </div>
      </>
    )
  }

  return (
    <>
      {header}
      {all.length === 0 ? (
        <div class="card">
          <Empty title={t('catalog.empty.title')} why={t('catalog.empty.why')} />
        </div>
      ) : (
        <>
          <div class="row mb" style={{ gap: '10px' }}>
            <div class="seg" role="group" aria-label={t('catalog.filter.kind')}>
              <button
                type="button"
                class={kind === 'all' ? 'on' : undefined}
                aria-pressed={kind === 'all'}
                onClick={() => setKind('all')}
              >
                {t('catalog.filter.all')}
              </button>
              {kinds.map((k) => (
                <button
                  key={k}
                  type="button"
                  class={kind === k ? 'on' : undefined}
                  aria-pressed={kind === k}
                  onClick={() => setKind(k)}
                  data-k={k}
                >
                  <KindShape kind={k} />
                  {kindName(k)}
                </button>
              ))}
            </div>
            <span class="grow" />
            <label class="search">
              <span class="sr">{t('catalog.filter.search')}</span>
              <Icon name="search" />
              <input
                class="field"
                type="search"
                placeholder={t('catalog.filter.search.placeholder')}
                value={q}
                onInput={(e) => setQ(e.currentTarget.value)}
              />
            </label>
            <div class="seg" role="group" aria-label={t('catalog.view')}>
              {(
                [
                  ['list', 'rows', t('catalog.view.list')],
                  ['card', 'cards', t('catalog.view.card')],
                ] as [View, IconName, string][]
              ).map(([v, icon, label]) => (
                <button
                  key={v}
                  type="button"
                  class={view === v ? 'on view-btn' : 'view-btn'}
                  onClick={() => pickView(v)}
                  aria-label={label}
                  title={label}
                  aria-pressed={view === v}
                >
                  <Icon name={icon} />
                </button>
              ))}
            </div>
          </div>
          <p class="tag" style={{ margin: '-4px 0 12px' }}>
            {t('catalog.count', { count: shown.length })}
            {kind !== 'all' && ` · ${kindName(kind)}`}
            {text && ` · ${t('catalog.count.search', { q: q.trim() })}`}
          </p>
          {shown.length === 0 ? (
            <div class="card">
              <Empty title={t('catalog.none.title')} why={t('catalog.none.why')}>
                <button
                  type="button"
                  class="btn"
                  onClick={() => {
                    setKind('all')
                    setQ('')
                  }}
                >
                  {t('catalog.none.reset')}
                </button>
              </Empty>
            </div>
          ) : view === 'card' ? (
            <div class="cards">
              {shown.map((r) => (
                <Card
                  key={r.code}
                  r={r}
                  fresh={fresh}
                  busy={busy === r.code}
                  onOpen={open}
                  onToggle={toggle}
                />
              ))}
            </div>
          ) : (
            <div class="card">
              <div class="tbl-wrap" tabIndex={0}>
                <table>
                  <thead>
                    <tr>
                      <th>{t('catalog.col.resource')}</th>
                      <th>{t('catalog.col.stages')}</th>
                      <th>{t('catalog.col.scope')}</th>
                      <th>{t('catalog.col.policy')}</th>
                      <th class="r">{t('catalog.col.installed')}</th>
                      <th class="r">{t('catalog.col.enabled')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {shown.map((r) => (
                      <tr
                        key={r.code}
                        class="click"
                        {...clickable(() => open(r.code))}
                        aria-label={t('catalog.open', { name: r.name })}
                      >
                        <td>
                          <div class="row" style={{ flexWrap: 'nowrap', gap: '12px' }}>
                            <KindIcon kind={r.kind} />
                            <div style={{ minWidth: 0 }}>
                              <div class="nm">{r.name}</div>
                              <div class="sub">{r.summary}</div>
                            </div>
                          </div>
                        </td>
                        <td>
                          <div class="row" style={{ gap: '4px' }}>
                            <StageBadges stages={r.stages} />
                          </div>
                        </td>
                        <td class="dim" style={{ whiteSpace: 'nowrap' }}>
                          {scopeName(r.scope)}
                        </td>
                        <td>
                          <PolicyBadge policy={r.policy} />
                        </td>
                        <td class="r num">
                          {r.installed_people ? (
                            <>
                              {fmtNumber(r.installed_people)} <span class="faint">{t('catalog.people')}</span>
                            </>
                          ) : (
                            <span class="faint">—</span>
                          )}
                        </td>
                        <td class="r">
                          <Switch r={r} busy={busy === r.code} onToggle={toggle} />
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          )}
        </>
      )}
      {params.code && <ResourceDrawer code={params.code} fresh={fresh} onClose={close} />}
    </>
  )
}

/** clickable makes a card or a table row work from the keyboard too: Tab to it, Enter or Space opens it. */
export function clickable(open: () => void) {
  return {
    tabIndex: 0,
    onClick: open,
    onKeyDown: (e: KeyboardEvent) => {
      if (e.target === e.currentTarget && (e.key === 'Enter' || e.key === ' ')) {
        e.preventDefault()
        open()
      }
    },
  }
}

function Switch(props: { r: Item; busy: boolean; onToggle: (r: Item) => void }) {
  const { r } = props
  const required = r.policy === 'required'
  return (
    <button
      type="button"
      class={r.enabled ? 'switch on' : 'switch'}
      role="switch"
      aria-checked={r.enabled}
      aria-label={t('catalog.switch', { name: r.name })}
      title={required ? t('catalog.switch.required') : undefined}
      disabled={required || props.busy}
      onClick={(e) => {
        e.stopPropagation()
        props.onToggle(r)
      }}
      onKeyDown={(e) => e.stopPropagation()}
    />
  )
}

/** meta is the card's line about how the resource reaches the AI tool. */
function meta(r: Item): [IconName, ComponentChildren, ComponentChildren] {
  if (r.secrets && r.secrets.length > 0) {
    const missing = missingKey(r)
    return [
      'key',
      <span class="mono">{r.secrets.map((s) => s.name).join(' · ')}</span>,
      missing ? (
        <span class="badge warn">{t('catalog.secret.missing')}</span>
      ) : (
        <span class="badge ok">{t('catalog.secret.configured')}</span>
      ),
    ]
  }
  if (r.kind === 'workflow') return ['flow', <span class="mono">{codeLabel(r)}</span>, null]
  const origin =
    r.origin === 'repo'
      ? t('catalog.origin.repo')
      : r.origin === 'upload'
        ? t('catalog.origin.upload')
        : t('catalog.origin.web')
  return ['box', r.kind === 'skill' ? t('catalog.origin.skill', { origin }) : origin, null]
}

function Card(props: {
  r: Item
  fresh: boolean
  busy: boolean
  onOpen: (code: string) => void
  onToggle: (r: Item) => void
}) {
  const { r } = props
  const [icon, line, tail] = meta(r)
  return (
    <div class={r.enabled ? 'rc' : 'rc off'} data-k={r.kind} onClick={() => props.onOpen(r.code)}>
      <div class="rc-in">
        <div class="rc-top">
          <KindTag kind={r.kind} version={r.version} />
          <Switch r={r} busy={props.busy} onToggle={props.onToggle} />
        </div>
        <div>
          <div class="rc-n">
            {/* The card opens on a click anywhere; from the keyboard, through its name. Clicking the
                button reaches the card's handler too, so it has none of its own. */}
            <button
              type="button"
              class="rc-open"
              aria-label={t('catalog.open.card', { name: r.name, kind: kindName(r.kind) })}
            >
              {r.name}
            </button>
            <span class="mono">{codeLabel(r)}</span>
          </div>
        </div>
        <div class="rc-d">{r.summary}</div>
        <div class="rc-b">
          {r.audience === 'mgmt' && <span class="badge">{t('catalog.audience.mgmt')}</span>}
          {r.stages.length > 0 && <StageBadges stages={r.stages} />}
        </div>
        <div class="rc-m">
          <Icon name={icon} />
          <span class="grow">{line}</span>
          {tail}
        </div>
      </div>
      <div class="rc-stat">
        {r.installed_people > 0 ? (
          <div>
            <b>{fmtNumber(r.installed_people)}</b>
            {t('catalog.stat.installed')}
          </div>
        ) : (
          <div class="nil">{t('catalog.stat.none')}</div>
        )}
      </div>
      <div class="rc-f">
        <span>
          {scopeName(r.scope)} · {policyName(r.policy)}
        </span>
        <InstallBadge r={r} fresh={props.fresh} />
      </div>
    </div>
  )
}
