// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// A-04 密钥与设备 (the prototype's V.keys): my personal secrets (write-only: set, replace, delete; nobody
// reads a value back), the computers signed in as me, and access tokens for programs. A secret's value is
// read from the field when saving and the field is cleared at once; it is never kept in page state, never
// logged. The "采集范围" column of the computers is M2.

import { useRef, useState } from 'preact/hooks'
import { api, unwrap, type Schemas } from '../../api/client'
import { BRAND } from '../../brand.gen'
import { fmtDateTime, fmtRelative } from '../../i18n/format'
import { t } from '../../i18n'
import { href } from '../../router/paths'
import { countMissing, missingSecrets } from '../../shell/counts'
import { me, meta } from '../../shell/session'
import { Empty } from '../../ui/empty'
import { Icon } from '../../ui/icons'
import { LoadError, Loading, useLoad } from '../../ui/load'
import { failText } from '../../ui/fail'
import { Modal } from '../../ui/overlay'
import { PageHeader } from '../../ui/page-header'
import { Cmd, SelfTip } from '../../ui/say'
import { toast } from '../../ui/toast'

const CLI = BRAND.cli
const DAY = 86_400_000
/** A secret expiring within this many days is flagged. */
const SOON_DAYS = 14

type Secret = Schemas['MySecret']
type Device = Schemas['Device']
type Token = Schemas['AccessToken']

function daysLeft(iso: string): number {
  return Math.ceil((Date.parse(iso) - Date.now()) / DAY)
}

/** newer says whether version a is older than b (MAJOR.MINOR.PATCH, pre-release ignored). */
function older(a: string, b: string): boolean {
  const pa = a.split('-')[0]?.split('.').map(Number) ?? []
  const pb = b.split('-')[0]?.split('.').map(Number) ?? []
  for (let i = 0; i < 3; i++) {
    const x = pa[i] ?? 0
    const y = pb[i] ?? 0
    if (x !== y) return x < y
  }
  return false
}

function IconButton(props: { icon: 'edit' | 'trash' | 'ban'; label: string; onClick: () => void }) {
  return (
    <button
      type="button"
      class="btn ghost icon"
      onClick={props.onClick}
      aria-label={props.label}
      title={props.label}
    >
      <Icon name={props.icon} />
    </button>
  )
}

/** Confirm is a yes-or-no dialog for something that cannot be undone. */
function Confirm(props: {
  title: string
  text: string
  ok: string
  onClose: () => void
  run: () => Promise<void>
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const go = async () => {
    setBusy(true)
    setError('')
    try {
      await props.run()
      props.onClose()
    } catch (err) {
      setError(failText(err))
      setBusy(false)
    }
  }
  return (
    <Modal
      title={props.title}
      onClose={props.onClose}
      foot={
        <>
          <button type="button" class="btn ghost" onClick={props.onClose}>
            {t('ui.cancel')}
          </button>
          <button type="button" class="btn primary" onClick={go} disabled={busy}>
            {props.ok}
          </button>
        </>
      }
    >
      {error && (
        <div class="callout bad" role="alert">
          {error}
        </div>
      )}
      <p class="t13">{props.text}</p>
    </Modal>
  )
}

export default function Keys() {
  return (
    <>
      <PageHeader page="keys" title={t('nav.keys')} lead={t('keys.lead')} />
      <div class="mb">
        <SelfTip>
          {t('keys.self.1')} <span class="mono">{`${CLI} secret set <NAME>`}</span>
          {t('keys.self.2')}
        </SelfTip>
      </div>
      <Secrets />
      <div class="grid g-side">
        <Devices />
        <Tokens />
      </div>
    </>
  )
}

// ---- 个人密钥 ----

function Secrets() {
  const list = useLoad(async () => {
    const items = unwrap(await api.GET('/api/v1/me/secrets')).items
    missingSecrets.value = countMissing(items)
    return items
  })
  const [editing, setEditing] = useState<Secret | null>(null)
  const [removing, setRemoving] = useState<Secret | null>(null)
  const items = list.data ?? []
  const weight = (s: Secret) =>
    !s.configured ? 0 : s.expires_at && daysLeft(s.expires_at) <= SOON_DAYS ? 1 : 2
  const sorted = [...items].sort((a, b) => weight(a) - weight(b))
  const done = items.filter((s) => s.configured).length

  return (
    <section class="card mb">
      <div class="card-h">
        <h3>{t('keys.secrets.title')}</h3>
        {items.length > 0 && <span class="tag">{t('keys.secrets.count', { done, all: items.length })}</span>}
        <p>{t('keys.secrets.desc')}</p>
      </div>
      {list.state === 'loading' && <Loading />}
      {list.state === 'failed' && <LoadError error={list.error} retry={list.reload} />}
      {list.state === 'ready' && items.length === 0 && (
        <Empty title={t('keys.secrets.empty')} why={t('keys.secrets.empty.why')} />
      )}
      {items.length > 0 && (
        <div class="tbl-wrap">
          <table>
            <thead>
              <tr>
                <th>{t('keys.col.name')}</th>
                <th>{t('keys.col.used')}</th>
                <th>{t('keys.col.howto')}</th>
                <th>{t('keys.col.state')}</th>
                <th>{t('keys.col.expires')}</th>
                <th class="r" aria-label={t('keys.col.actions')} />
              </tr>
            </thead>
            <tbody>
              {sorted.map((s) => {
                const left = s.expires_at ? daysLeft(s.expires_at) : null
                return (
                  <tr key={s.name}>
                    <td>
                      <div class="nm">{s.label}</div>
                      <div class="sub mono">{s.name}</div>
                    </td>
                    <td class="dim">{s.used_by.map((u) => u.name).join(t('ui.list_sep'))}</td>
                    <td class="dim t12" style={{ minWidth: '200px' }}>
                      {s.howto ?? '—'}
                    </td>
                    <td>
                      {s.configured ? (
                        <span class="badge ok">
                          <span class="dot" />
                          {t('keys.secret.configured')}
                        </span>
                      ) : (
                        <span class="badge warn">
                          <span class="dot" />
                          {t('keys.secret.missing')}
                        </span>
                      )}
                    </td>
                    <td class="t12" style={{ whiteSpace: 'nowrap' }}>
                      {!s.configured ? (
                        <span class="faint">—</span>
                      ) : left === null ? (
                        <span class="dim">{t('keys.secret.no_expiry')}</span>
                      ) : left <= 0 ? (
                        <span class="badge bad">
                          <Icon name="alert" />
                          {t('keys.secret.expired')}
                        </span>
                      ) : left <= SOON_DAYS ? (
                        <span
                          class="badge warn"
                          title={fmtDateTime(s.expires_at ?? '', { dateStyle: 'medium' })}
                        >
                          <Icon name="alert" />
                          {t('keys.secret.expires_soon', { count: left })}
                        </span>
                      ) : (
                        <span class="dim" title={fmtDateTime(s.expires_at ?? '', { dateStyle: 'medium' })}>
                          {t('keys.secret.expires_in', { count: left })}
                        </span>
                      )}
                    </td>
                    <td class="r" style={{ whiteSpace: 'nowrap' }}>
                      {s.configured ? (
                        <>
                          <IconButton
                            icon="edit"
                            label={t('keys.secret.replace')}
                            onClick={() => setEditing(s)}
                          />
                          <IconButton
                            icon="trash"
                            label={t('keys.secret.delete')}
                            onClick={() => setRemoving(s)}
                          />
                        </>
                      ) : (
                        <button type="button" class="btn primary sm" onClick={() => setEditing(s)}>
                          <Icon name="key" />
                          {t('keys.secret.configure')}
                        </button>
                      )}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
      {editing && <SecretDialog secret={editing} onClose={() => setEditing(null)} onSaved={list.reload} />}
      {removing && (
        <Confirm
          title={t('keys.secret.delete.title', { label: removing.label })}
          text={t('keys.secret.delete.text')}
          ok={t('keys.secret.delete')}
          onClose={() => setRemoving(null)}
          run={async () => {
            unwrap(
              await api.DELETE('/api/v1/me/secrets/{secret_name}', {
                params: { path: { secret_name: removing.name } },
              }),
            )
            toast(t('keys.secret.deleted'))
            list.reload()
          }}
        />
      )}
    </section>
  )
}

function SecretDialog(props: { secret: Secret; onClose: () => void; onSaved: () => void }) {
  const s = props.secret
  const value = useRef<HTMLInputElement>(null)
  const expiry = useRef<HTMLInputElement>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const save = async (e?: Event) => {
    e?.preventDefault()
    const field = value.current
    const v = field?.value ?? ''
    if (!v) {
      setError(t('keys.secret.dialog.required'))
      return
    }
    const date = expiry.current?.value
    if (field) field.value = ''
    setBusy(true)
    setError('')
    try {
      unwrap(
        await api.PUT('/api/v1/me/secrets/{secret_name}', {
          params: { path: { secret_name: s.name } },
          body: { value: v, ...(date ? { expires_at: new Date(date + 'T00:00:00Z').toISOString() } : {}) },
        }),
      )
      toast(t('keys.secret.saved'))
      props.onSaved()
      props.onClose()
    } catch (err) {
      setError(failText(err))
      setBusy(false)
    }
  }
  return (
    <Modal
      title={
        s.configured
          ? t('keys.secret.dialog.replace', { label: s.label })
          : t('keys.secret.dialog.title', { label: s.label })
      }
      onClose={props.onClose}
      foot={
        <>
          <button type="button" class="btn ghost" onClick={props.onClose}>
            {t('ui.cancel')}
          </button>
          <button type="submit" form="secret-form" class="btn primary" disabled={busy}>
            {t('ui.save')}
          </button>
        </>
      }
    >
      <form id="secret-form" onSubmit={save} style={{ display: 'contents' }} autocomplete="off">
        {error && (
          <div class="callout bad" role="alert">
            {error}
          </div>
        )}
        <p class="dim t13">
          {t('keys.secret.dialog.used', { used: s.used_by.map((u) => u.name).join(t('ui.list_sep')) })}
          {s.howto && ` ${t('keys.secret.dialog.howto', { howto: s.howto })}`}
        </p>
        <div>
          <label class="fl" for="d-sec">
            {t('keys.secret.dialog.value')}
          </label>
          <input
            class="field mono"
            id="d-sec"
            type="password"
            autocomplete="off"
            spellcheck={false}
            ref={value}
            placeholder={t('keys.secret.dialog.placeholder')}
          />
          <p class="hint">{t('keys.secret.dialog.hint')}</p>
        </div>
        <div>
          <label class="fl" for="d-exp">
            {t('keys.secret.dialog.expires')}{' '}
            <span class="faint">{t('keys.secret.dialog.expires.note')}</span>
          </label>
          <input class="field" id="d-exp" type="date" ref={expiry} style={{ maxWidth: '200px' }} />
        </div>
      </form>
    </Modal>
  )
}

// ---- 我的电脑 ----

const OS: Record<Device['os'], string> = { darwin: 'macOS', linux: 'Linux', windows: 'Windows' }

function Devices() {
  const list = useLoad(async () => unwrap(await api.GET('/api/v1/me/devices')).items)
  const [out, setOut] = useState<Device | null>(null)
  const [all, setAll] = useState(false)
  const latest = meta.value?.cli.latest_version
  const items = list.data ?? []
  return (
    <section class="card">
      <div class="card-h">
        <h3>{t('keys.devices.title')}</h3>
        <button type="button" class="btn sm" onClick={() => setAll(true)}>
          <Icon name="ban" />
          {t('keys.devices.all')}
        </button>
      </div>
      {list.state === 'loading' && <Loading />}
      {list.state === 'failed' && <LoadError error={list.error} retry={list.reload} />}
      {list.state === 'ready' && items.length === 0 && (
        <Empty title={t('keys.devices.empty')} why={t('keys.devices.empty.why')}>
          <a class="btn" href={href('/connect')}>
            <Icon name="plug" />
            {t('nav.connect')}
          </a>
        </Empty>
      )}
      {items.length > 0 && (
        <div class="tbl-wrap">
          <table>
            <thead>
              <tr>
                <th>{t('keys.col.device')}</th>
                <th>{t('keys.col.version')}</th>
                <th>{t('keys.col.sync')}</th>
                <th>{t('keys.col.seen')}</th>
                <th class="r" aria-label={t('keys.col.actions')} />
              </tr>
            </thead>
            <tbody>
              {items.map((d) => (
                <tr key={d.id}>
                  <td style={{ whiteSpace: 'nowrap' }}>
                    <div class="nm">
                      {d.name} {d.current && <span class="badge brand">{t('keys.device.current')}</span>}
                    </div>
                    <div class="sub">
                      {t('keys.device.kind', { os: OS[d.os] })}
                      {d.key_storage === 'file' && (
                        <span title={t('keys.device.file.why')}> · {t('keys.device.file')}</span>
                      )}
                    </div>
                  </td>
                  <td class="code" style={{ whiteSpace: 'nowrap' }}>
                    {d.cli_version}{' '}
                    {latest && older(d.cli_version, latest) && (
                      <span class="badge warn" style={{ fontFamily: 'var(--sans)' }}>
                        {t('keys.device.upgrade')}
                      </span>
                    )}
                  </td>
                  <td style={{ whiteSpace: 'nowrap' }}>
                    {d.last_sync ? (
                      <span
                        class={
                          d.last_sync.result === 'ok'
                            ? 'badge'
                            : d.last_sync.result === 'partial'
                              ? 'badge warn'
                              : 'badge bad'
                        }
                        title={fmtDateTime(d.last_sync.at)}
                      >
                        {d.last_sync.result === 'ok'
                          ? t('keys.device.sync.ok')
                          : d.last_sync.result === 'partial'
                            ? t('keys.device.sync.partial')
                            : t('keys.device.sync.failed')}
                      </span>
                    ) : (
                      <span class="faint">{t('keys.device.sync.never')}</span>
                    )}
                  </td>
                  <td class="dim" style={{ whiteSpace: 'nowrap' }} title={fmtDateTime(d.last_seen_at)}>
                    {fmtRelative(d.last_seen_at)}
                  </td>
                  <td class="r">
                    <IconButton icon="ban" label={t('keys.device.out')} onClick={() => setOut(d)} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <p class="card-f">{t('keys.devices.foot')}</p>
      {out && (
        <Confirm
          title={t('keys.device.out.title', { name: out.name })}
          text={out.current ? t('keys.device.out.current') : t('keys.device.out.text', { cli: CLI })}
          ok={t('keys.device.out')}
          onClose={() => setOut(null)}
          run={async () => {
            unwrap(
              await api.DELETE('/api/v1/me/devices/{device_id}', { params: { path: { device_id: out.id } } }),
            )
            toast(t('keys.device.out.done', { cli: CLI }))
            list.reload()
          }}
        />
      )}
      {all && (
        <Confirm
          title={t('keys.devices.all.title')}
          text={t('keys.devices.all.text')}
          ok={t('keys.devices.all')}
          onClose={() => setAll(false)}
          run={async () => {
            unwrap(await api.POST('/api/v1/me/sign-out-all'))
            me.value = null
            location.assign(href('/login'))
          }}
        />
      )}
    </section>
  )
}

// ---- 访问令牌 ----

function Tokens() {
  const list = useLoad(async () => unwrap(await api.GET('/api/v1/me/tokens')).items)
  const [issuing, setIssuing] = useState(false)
  const [revoking, setRevoking] = useState<Token | null>(null)
  const items = list.data ?? []
  return (
    <section class="card">
      <div class="card-h">
        <h3>{t('keys.tokens.title')}</h3>
        <button type="button" class="btn sm" onClick={() => setIssuing(true)}>
          <Icon name="plus" />
          {t('keys.tokens.issue')}
        </button>
      </div>
      {list.state === 'loading' && <Loading />}
      {list.state === 'failed' && <LoadError error={list.error} retry={list.reload} />}
      {list.state === 'ready' && items.length === 0 && (
        <Empty title={t('keys.tokens.empty')} why={t('keys.tokens.empty.why')} />
      )}
      {items.length > 0 && (
        <div class="tbl-wrap">
          <table>
            <thead>
              <tr>
                <th>{t('keys.col.token')}</th>
                <th>{t('keys.col.prefix')}</th>
                <th>{t('keys.col.used_at')}</th>
                <th class="r" aria-label={t('keys.col.actions')} />
              </tr>
            </thead>
            <tbody>
              {items.map((x) => {
                const expired = Date.parse(x.expires_at) <= Date.now()
                return (
                  <tr key={x.id} class={expired ? 'off' : undefined}>
                    <td>
                      <div class="nm">{x.name}</div>
                      <div class="sub">
                        {x.scope === 'publish' ? t('keys.token.scope.publish') : t('keys.token.scope.read')} ·{' '}
                        {expired
                          ? t('keys.token.expired')
                          : t('keys.token.until', {
                              date: fmtDateTime(x.expires_at, { dateStyle: 'medium' }),
                            })}
                      </div>
                    </td>
                    <td class="code">{x.prefix}…</td>
                    <td class="dim" style={{ whiteSpace: 'nowrap' }}>
                      {x.last_used_at ? fmtRelative(x.last_used_at) : t('keys.token.never')}
                    </td>
                    <td class="r">
                      <IconButton icon="ban" label={t('keys.token.revoke')} onClick={() => setRevoking(x)} />
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
      <p class="card-f">{t('keys.tokens.foot')}</p>
      {issuing && <TokenDialog onClose={() => setIssuing(false)} onIssued={list.reload} />}
      {revoking && (
        <Confirm
          title={t('keys.token.revoke.title', { name: revoking.name })}
          text={t('keys.token.revoke.text')}
          ok={t('keys.token.revoke')}
          onClose={() => setRevoking(null)}
          run={async () => {
            unwrap(
              await api.DELETE('/api/v1/me/tokens/{token_id}', {
                params: { path: { token_id: revoking.id } },
              }),
            )
            toast(t('keys.token.revoked'))
            list.reload()
          }}
        />
      )}
    </section>
  )
}

function TokenDialog(props: { onClose: () => void; onIssued: () => void }) {
  const [name, setName] = useState('')
  const [scope, setScope] = useState<Schemas['TokenScope']>('read')
  const [days, setDays] = useState(90)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [plain, setPlain] = useState<string | null>(null)

  if (plain !== null) {
    return (
      <Modal
        title={t('keys.token.shown.title')}
        onClose={props.onClose}
        foot={
          <button type="button" class="btn primary" onClick={props.onClose}>
            {t('keys.token.shown.done')}
          </button>
        }
      >
        <div class="callout" role="status">
          <Icon name="alert" />
          {t('keys.token.shown.once')}
        </div>
        <Cmd text={plain} />
        <p class="hint" style={{ margin: 0 }}>
          {t('keys.token.shown.hint')}
        </p>
      </Modal>
    )
  }

  const issue = async (e?: Event) => {
    e?.preventDefault()
    if (!name.trim()) {
      setError(t('keys.token.dialog.required'))
      return
    }
    setBusy(true)
    setError('')
    try {
      const made = unwrap(
        await api.POST('/api/v1/me/tokens', { body: { name: name.trim(), scope, expires_in_days: days } }),
      )
      props.onIssued()
      setPlain(made.token)
    } catch (err) {
      setError(failText(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      title={t('keys.token.dialog.title')}
      onClose={props.onClose}
      foot={
        <>
          <button type="button" class="btn ghost" onClick={props.onClose}>
            {t('ui.cancel')}
          </button>
          <button type="submit" form="token-form" class="btn primary" disabled={busy}>
            {t('keys.tokens.issue')}
          </button>
        </>
      }
    >
      <form id="token-form" onSubmit={issue} style={{ display: 'contents' }}>
        {error && (
          <div class="callout bad" role="alert">
            {error}
          </div>
        )}
        <div>
          <label class="fl" for="d-tn">
            {t('keys.token.dialog.name')}
          </label>
          <input
            class="field"
            id="d-tn"
            maxLength={64}
            value={name}
            onInput={(e) => setName(e.currentTarget.value)}
            placeholder={t('keys.token.dialog.name.placeholder')}
          />
        </div>
        <div>
          <div class="fl">{t('keys.token.dialog.scope')}</div>
          <div class="seg" role="group" aria-label={t('keys.token.dialog.scope')}>
            {(
              [
                ['read', t('keys.token.dialog.read')],
                ['publish', t('keys.token.dialog.publish')],
              ] as [Schemas['TokenScope'], string][]
            ).map(([v, label]) => (
              <button
                key={v}
                type="button"
                class={scope === v ? 'on' : undefined}
                aria-pressed={scope === v}
                onClick={() => setScope(v)}
              >
                {label}
              </button>
            ))}
          </div>
        </div>
        <div>
          <label class="fl" for="d-te">
            {t('keys.token.dialog.expires')}
          </label>
          <select
            class="field"
            id="d-te"
            value={days}
            onChange={(e) => setDays(Number(e.currentTarget.value))}
          >
            <option value={30}>{t('keys.token.dialog.days', { count: 30 })}</option>
            <option value={90}>{t('keys.token.dialog.days', { count: 90 })}</option>
            <option value={365}>{t('keys.token.dialog.year')}</option>
          </select>
        </div>
      </form>
    </Modal>
  )
}
