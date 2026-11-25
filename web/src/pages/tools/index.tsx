// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// D-02 工具适配 (prototype V.tools in app.js). Each AI tool has a built-in adapter profile: where each kind of
// resource lands, in what format, and what it becomes when the tool has no place for it. Everything on the
// page comes from those profiles (/api/v1/adapters) except the organisation-wide switch and how many people
// have the tool. Below: the fallback rules and the command-line versions the server accepts (/api/v1/cli).
//
// The prototype's 新增工具 button is left out: a tool is added by writing an adapter profile in the code
// repository, not on this page.

import { useState } from 'preact/hooks'
import { api, unwrap, type Schemas } from '../../api/client'
import { useLoad } from '../../ui/load'
import { t } from '../../i18n'
import { fmtDateTime, fmtNumber } from '../../i18n/format'
import { BRAND } from '../../brand.gen'
import { Switch } from '../../ui/controls'
import { PageHeader } from '../../ui/page-header'
import { Fail } from '../../ui/fail'
import { LoadError } from '../../ui/load'
import { toast } from '../../ui/toast'
import './tools.css'

type Adapter = Schemas['Adapter']

const KINDS = ['skill', 'mcp', 'command', 'agent'] as const

export default function Tools() {
  const adapters = useLoad(async () => unwrap(await api.GET('/api/v1/adapters')).items, [])
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const list = adapters.data ?? []
  const hooks = list.some((a) => a.targets.hook)

  const toggle = async (a: Adapter, enabled: boolean) => {
    setBusy(a.id)
    setError(null)
    const out = await api
      .PATCH('/api/v1/adapters/{adapter_id}', { params: { path: { adapter_id: a.id } }, body: { enabled } })
      .then(unwrap)
      .catch((err: unknown) => err)
    setBusy(null)
    if (out instanceof Error) {
      setError(out)
      return
    }
    adapters.set(list.map((x) => (x.id === a.id ? (out as Adapter) : x)))
    toast(enabled ? t('tools.enabled', { tool: a.name }) : t('tools.disabled', { tool: a.name }))
  }

  return (
    <>
      <PageHeader page="tools" title={t('nav.tools')} lead={t('tools.lead')} />
      <div class="grid g3 mb tl-base">
        {(
          [
            [t('tools.base1.title'), `${BRAND.cli} sync --plan`, true],
            [t('tools.base2.title'), `${BRAND.cli} uninstall`, true],
            [t('tools.base3.title'), t('tools.base3.desc'), false],
          ] as const
        ).map(([title, desc, mono], i) => (
          <div key={i}>
            <span class="tag">{t('tools.base', { n: i + 1 })}</span>
            <div class="tl-base-t">{title}</div>
            <div class={mono ? 'mono faint' : 't12 faint'}>{desc}</div>
          </div>
        ))}
      </div>
      <section class="card">
        {error !== null && (
          <div style={{ padding: '12px 18px 0' }}>
            <Fail error={error} />
          </div>
        )}
        {adapters.state === 'failed' && !adapters.data ? (
          <LoadError error={adapters.error} retry={adapters.reload} />
        ) : (
          <div class="tbl-wrap" tabIndex={0}>
            <table class="tl-tbl" aria-busy={!adapters.data}>
              <thead>
                <tr>
                  <th>{t('tools.col.tool')}</th>
                  <th>{t('tools.col.skill')}</th>
                  <th>{t('tools.col.mcp')}</th>
                  <th>{t('tools.col.command')}</th>
                  <th>{t('tools.col.agent')}</th>
                  {hooks && <th>{t('tools.col.hook')}</th>}
                  <th class="r">{t('tools.col.people')}</th>
                  <th class="r">{t('tools.col.enabled')}</th>
                </tr>
              </thead>
              <tbody>
                {list.map((a) => (
                  <tr key={a.id} class={a.enabled ? undefined : 'off'}>
                    <td style={{ whiteSpace: 'nowrap' }}>
                      <div class="nm">{a.name}</div>
                      <div class="sub">
                        {a.verified ? (
                          <span title={t('tools.verified_title', { version: a.verified.version })}>
                            {t('tools.verified', {
                              version: a.verified.version,
                              // A date without a time: shown as written, not moved into the browser's time zone.
                              date: fmtDateTime(a.verified.at, { dateStyle: 'medium', timeZone: 'UTC' }),
                            })}
                          </span>
                        ) : (
                          <span style={{ color: 'var(--warn)' }}>{t('tools.unverified')}</span>
                        )}
                      </div>
                    </td>
                    {KINDS.map((k) => (
                      <td key={k} class="code tl-loc">
                        <Target target={a.targets[k]} />
                      </td>
                    ))}
                    {hooks && (
                      <td>
                        <Hook target={a.targets.hook} />
                      </td>
                    )}
                    <td class="r num">{a.people ? fmtNumber(a.people) : <span class="faint">—</span>}</td>
                    <td class="r">
                      <Switch
                        on={a.enabled}
                        disabled={busy === a.id}
                        label={t('tools.switch', { tool: a.name })}
                        onChange={(on) => void toggle(a, on)}
                      />
                    </td>
                  </tr>
                ))}
                {adapters.data && list.length === 0 && (
                  <tr>
                    <td colSpan={hooks ? 8 : 7} class="faint">
                      {t('tools.none')}
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        )}
        <p class="card-f">{t('tools.note')}</p>
      </section>
      <div class="grid g2" style={{ marginTop: '16px' }}>
        <section class="card pad">
          <h3>{t('tools.fallback.title')}</h3>
          <div class="list">
            {(
              [
                [t('tools.fallback.1.when'), t('tools.fallback.1.then')],
                [t('tools.fallback.2.when'), t('tools.fallback.2.then')],
                [t('tools.fallback.3.when'), t('tools.fallback.3.then')],
                [t('tools.fallback.4.when'), t('tools.fallback.4.then')],
                [t('tools.fallback.5.when'), t('tools.fallback.5.then')],
              ] as const
            ).map(([when, then], i) => (
              <div key={i} class="li" style={{ padding: '9px 0', alignItems: 'flex-start' }}>
                <span class="tag" style={{ width: '20px', paddingTop: '2px' }}>
                  {String(i + 1).padStart(2, '0')}
                </span>
                <div class="grow">
                  <div class="name">{when}</div>
                  <div class="desc" style={{ whiteSpace: 'normal' }}>
                    {then}
                  </div>
                </div>
              </div>
            ))}
          </div>
        </section>
        <CLIVersions />
      </div>
    </>
  )
}

function Target(props: { target?: Schemas['AdapterTarget'] }) {
  const x = props.target
  if (x?.location) {
    return (
      <>
        {x.location}
        {x.detail && <span class="faint">{t('tools.detail', { detail: x.detail })}</span>}
      </>
    )
  }
  if (x?.fallback) {
    return (
      <span class="badge warn tl-fb">
        {x.fallback === 'skill' ? t('tools.as_skill') : t('tools.as_kind', { kind: x.fallback })}
      </span>
    )
  }
  return <span class="faint tl-fb">{t('tools.unsupported')}</span>
}

function Hook(props: { target?: Schemas['AdapterTarget'] }) {
  const x = props.target
  if (!x?.location) return <span class="badge">{t('tools.hook.none')}</span>
  return x.opt_in ? (
    <span class="badge ok">{t('tools.hook.opt_in')}</span>
  ) : (
    <span class="code">{x.location}</span>
  )
}

/** The newest command line, the oldest one allowed to sync, and who runs which. */
function CLIVersions() {
  const policy = useLoad(async () => unwrap(await api.GET('/api/v1/cli')), [])
  const [min, setMin] = useState<string | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const p = policy.data
  const value = min ?? p?.min_version ?? ''
  const valid = /^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/.test(value)

  const save = async (e: Event) => {
    e.preventDefault()
    if (!p || !valid || value === p.min_version) return
    setBusy(true)
    setError(null)
    const out = await api
      .PATCH('/api/v1/cli', { body: { min_version: value } })
      .then(unwrap)
      .catch((err: unknown) => err)
    setBusy(false)
    if (out instanceof Error) {
      setError(out)
      return
    }
    policy.set(out as Schemas['CLIPolicy'])
    setMin(null)
    toast(t('tools.cli.saved', { version: value }))
  }

  const total = (p?.distribution ?? []).reduce((n, d) => n + d.people, 0)
  return (
    <section class="card pad">
      <h3>{t('tools.cli.title')}</h3>
      {policy.state === 'failed' && !p ? (
        <LoadError error={policy.error} retry={policy.reload} />
      ) : !p ? (
        <div aria-busy="true" style={{ minHeight: '120px' }} />
      ) : (
        <>
          <dl class="kv lined">
            <dt>{t('tools.cli.latest')}</dt>
            <dd class="mono">{p.latest_version}</dd>
            <dt>
              <label for="cli-min">{t('tools.cli.min')}</label>
            </dt>
            <dd>
              <form class="row" style={{ gap: '6px' }} onSubmit={save}>
                <input
                  class="field mono sm tl-min"
                  id="cli-min"
                  value={value}
                  aria-invalid={!valid}
                  aria-describedby="cli-min-hint"
                  onInput={(e) => setMin(e.currentTarget.value.trim())}
                />
                {value !== p.min_version && (
                  <button type="submit" class="btn sm" disabled={busy || !valid}>
                    {t('ui.save')}
                  </button>
                )}
                <span class="faint t12" id="cli-min-hint">
                  {t('tools.cli.min_hint', { floor: p.min_version_floor })}
                </span>
              </form>
            </dd>
            <dt>{t('tools.cli.spread')}</dt>
            <dd>
              {total === 0 ? (
                <span class="faint t12">{t('tools.cli.spread_none')}</span>
              ) : (
                p.distribution.map((d) => (
                  <div key={d.version} class="ibar" style={{ marginBottom: '4px' }}>
                    <i
                      style={{
                        width: `${(d.people / total) * 100}%`,
                        background: older(d.version, p.latest_version) ? 'var(--warn)' : undefined,
                      }}
                    />
                    <span>{t('tools.cli.people', { version: d.version, count: d.people })}</span>
                  </div>
                ))
              )}
            </dd>
          </dl>
          <Fail error={error} />
        </>
      )}
    </section>
  )
}

/** older says whether version a is before b, comparing MAJOR.MINOR.PATCH numerically. */
function older(a: string, b: string): boolean {
  const pa = a.split(/[.-]/).map(Number)
  const pb = b.split(/[.-]/).map(Number)
  for (let i = 0; i < 3; i++) {
    const x = pa[i] ?? 0
    const y = pb[i] ?? 0
    if (x !== y) return x < y
  }
  return false
}
