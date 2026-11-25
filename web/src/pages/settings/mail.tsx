// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Outgoing mail and the organisation's name: both are platform settings (/api/v1/settings), changed with the
// ETag last seen. The mail password is write-only: the page only knows whether one is set, and the field is
// emptied after saving.

import { useEffect, useState } from 'preact/hooks'
import { ApiError, api, unwrap, type Schemas } from '../../api/client'
import type { Load } from '../../ui/load'
import { t } from '../../i18n'
import { me, meta } from '../../shell/session'
import { Switch } from '../../ui/controls'
import { Modal } from '../../ui/overlay'
import { Fail } from '../../ui/fail'
import { LoadError } from '../../ui/load'
import { toast } from '../../ui/toast'

type Settings = Schemas['PlatformSettings']
type Props = { settings: Load<Settings>; reload: () => Promise<void>; onSaved: (s: Settings) => void }

async function patch(etag: string, body: Schemas['UpdateSettingsRequest']): Promise<Settings> {
  return unwrap(await api.PATCH('/api/v1/settings', { params: { header: { 'If-Match': etag } }, body }))
}

interface Mail {
  enabled: boolean
  host: string
  port: string
  security: 'tls' | 'starttls'
  username: string
  from: string
}

const mailOf = (m: Schemas['MailSettings']): Mail => ({
  enabled: m.enabled,
  host: m.host ?? '',
  port: m.port ? String(m.port) : '',
  security: m.security ?? 'tls',
  username: m.username ?? '',
  from: m.from ?? '',
})

export function MailCard(props: Props) {
  const s = props.settings.data
  const [form, setForm] = useState<Mail | null>(null)
  const [password, setPassword] = useState('')
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const [testing, setTesting] = useState(false)

  useEffect(() => {
    if (s) setForm(mailOf(s.mail))
  }, [s?.etag])

  if (!s || !form) {
    return (
      <section class="card pad">
        <h3>{t('settings.mail.title')}</h3>
        {props.settings.state === 'failed' ? (
          <LoadError error={props.settings.error} retry={props.reload} />
        ) : (
          <div aria-busy="true" style={{ minHeight: '120px' }} />
        )}
      </section>
    )
  }

  const before = mailOf(s.mail)
  const dirty = JSON.stringify(before) !== JSON.stringify(form) || password !== ''
  const port = Number(form.port)
  const portOk = form.port === '' || (Number.isInteger(port) && port >= 1 && port <= 65535)
  const set = (p: Partial<Mail>) => setForm({ ...form, ...p })

  const save = async (e: Event) => {
    e.preventDefault()
    if (!dirty || !portOk) return
    setBusy(true)
    setError(null)
    const out = await patch(s.etag, {
      mail: {
        enabled: form.enabled,
        host: form.host.trim(),
        port: form.port ? port : undefined,
        security: form.security,
        username: form.username.trim(),
        from: form.from.trim() || undefined,
        ...(password ? { password } : {}),
      },
    }).catch((err: unknown) => err)
    setBusy(false)
    if (out instanceof Error) {
      setError(out)
      if (out instanceof ApiError && out.status === 412) await props.reload()
      return
    }
    setPassword('')
    props.onSaved(out as Settings)
    toast(t('settings.saved'))
  }

  return (
    <section class="card pad">
      <h3>
        {t('settings.mail.title')}
        <span class={s.mail.enabled ? 'badge ok' : 'badge'}>
          {s.mail.enabled ? t('settings.mail.on') : t('settings.mail.off')}
        </span>
      </h3>
      <form onSubmit={save} style={{ display: 'grid', gap: '14px' }}>
        <div class="li" style={{ padding: 0, borderBottom: 0 }}>
          <div class="grow">
            <div class="name">{t('settings.mail.send')}</div>
            <div class="desc" style={{ whiteSpace: 'normal' }}>
              {t('settings.mail.send_desc')}
            </div>
          </div>
          <Switch
            on={form.enabled}
            label={t('settings.mail.send')}
            onChange={(enabled) => set({ enabled })}
          />
        </div>
        <div class="grid g2" style={{ gap: '10px' }}>
          <div>
            <label class="fl" for="s-host">
              {t('settings.mail.host')}
            </label>
            <input
              class="field mono"
              id="s-host"
              maxLength={255}
              value={form.host}
              placeholder="smtp.example.com"
              onInput={(e) => set({ host: e.currentTarget.value })}
            />
          </div>
          <div class="grid" style={{ gridTemplateColumns: 'minmax(0,1fr) minmax(0,1fr)', gap: '10px' }}>
            <div>
              <label class="fl" for="s-port">
                {t('settings.mail.port')}
              </label>
              <input
                class="field mono"
                id="s-port"
                inputMode="numeric"
                value={form.port}
                aria-invalid={!portOk}
                placeholder="465"
                onInput={(e) => set({ port: e.currentTarget.value.trim() })}
              />
            </div>
            <div>
              <label class="fl" for="s-sec">
                {t('settings.mail.security')}
              </label>
              <select
                class="field"
                id="s-sec"
                value={form.security}
                onChange={(e) => set({ security: e.currentTarget.value as Mail['security'] })}
              >
                <option value="tls">TLS</option>
                <option value="starttls">STARTTLS</option>
              </select>
            </div>
          </div>
          <div>
            <label class="fl" for="s-user">
              {t('settings.mail.username')}
            </label>
            <input
              class="field mono"
              id="s-user"
              maxLength={255}
              autocomplete="off"
              value={form.username}
              onInput={(e) => set({ username: e.currentTarget.value })}
            />
          </div>
          <div>
            <label class="fl" for="s-pw">
              {t('settings.mail.password')}
            </label>
            <input
              class="field mono"
              id="s-pw"
              type="password"
              autocomplete="off"
              maxLength={1024}
              value={password}
              placeholder={
                s.mail.has_password ? t('settings.mail.password_set') : t('settings.mail.password_ph')
              }
              onInput={(e) => setPassword(e.currentTarget.value)}
            />
          </div>
        </div>
        <div>
          <label class="fl" for="s-from">
            {t('settings.mail.from')}
          </label>
          <input
            class="field mono"
            id="s-from"
            type="email"
            value={form.from}
            placeholder="hub@example.com"
            onInput={(e) => set({ from: e.currentTarget.value })}
          />
          <p class="hint">{!portOk ? t('settings.mail.port_bad') : t('settings.mail.hint')}</p>
        </div>
        <Fail error={error} />
        <div class="row">
          <button type="submit" class="btn primary" disabled={busy || !dirty || !portOk}>
            {t('ui.save')}
          </button>
          <button
            type="button"
            class="btn"
            disabled={dirty || !s.mail.enabled}
            title={dirty ? t('settings.mail.test_save_first') : undefined}
            onClick={() => setTesting(true)}
          >
            {t('settings.mail.test')}
          </button>
        </div>
      </form>
      {testing && <TestMail onClose={() => setTesting(false)} />}
    </section>
  )
}

function TestMail(props: { onClose: () => void }) {
  const [to, setTo] = useState(me.value?.email ?? '')
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const send = async () => {
    setBusy(true)
    setError(null)
    const out = await api
      .POST('/api/v1/settings/mail/test', { body: { to: to.trim() } })
      .then(unwrap)
      .catch((err: unknown) => err)
    setBusy(false)
    if (out instanceof Error) {
      setError(out)
      return
    }
    props.onClose()
    toast(t('settings.mail.test_sent', { to: to.trim() }))
  }
  return (
    <Modal
      title={t('settings.mail.test')}
      onClose={props.onClose}
      foot={
        <>
          <button type="button" class="btn ghost" onClick={props.onClose}>
            {t('ui.cancel')}
          </button>
          <button
            type="button"
            class="btn primary"
            disabled={busy || !to.includes('@')}
            onClick={() => void send()}
          >
            {busy ? t('settings.mail.test_sending') : t('settings.mail.test_send')}
          </button>
        </>
      }
    >
      <div>
        <label class="fl" for="d-to">
          {t('settings.mail.test_to')}
        </label>
        <input
          class="field mono"
          id="d-to"
          type="email"
          autofocus
          value={to}
          onInput={(e) => setTo(e.currentTarget.value)}
        />
        <p class="hint">{t('settings.mail.test_hint')}</p>
      </div>
      <Fail error={error} />
    </Modal>
  )
}

export function OrganizationCard(props: Props) {
  const s = props.settings.data
  const [name, setName] = useState<string | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  if (!s) {
    return (
      <section class="card pad">
        <h3>{t('settings.org.title')}</h3>
        {props.settings.state === 'failed' ? (
          <LoadError error={props.settings.error} retry={props.reload} />
        ) : (
          <div aria-busy="true" style={{ minHeight: '60px' }} />
        )}
      </section>
    )
  }
  const value = name ?? s.organization.name
  const dirty = value.trim() !== s.organization.name
  const save = async (e: Event) => {
    e.preventDefault()
    if (!dirty) return
    setBusy(true)
    setError(null)
    const out = await patch(s.etag, { organization: { name: value.trim() } }).catch((err: unknown) => err)
    setBusy(false)
    if (out instanceof Error) {
      setError(out)
      if (out instanceof ApiError && out.status === 412) await props.reload()
      return
    }
    setName(null)
    props.onSaved(out as Settings)
    if (meta.value)
      meta.value = { ...meta.value, organization: (out as Settings).organization.name || undefined }
    toast(t('settings.saved'))
  }
  return (
    <section class="card pad">
      <h3>{t('settings.org.title')}</h3>
      <form onSubmit={save} style={{ display: 'grid', gap: '10px' }}>
        <div>
          <label class="fl" for="s-org">
            {t('settings.org.name')}
          </label>
          <div class="row" style={{ flexWrap: 'nowrap' }}>
            <input
              class="field"
              id="s-org"
              maxLength={64}
              value={value}
              onInput={(e) => setName(e.currentTarget.value)}
            />
            <button type="submit" class="btn" disabled={busy || !dirty}>
              {t('ui.save')}
            </button>
          </div>
          <p class="hint">{t('settings.org.hint')}</p>
        </div>
        <Fail error={error} />
      </form>
    </section>
  )
}
