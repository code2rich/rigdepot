// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The sign-in page, from the prototype (V.login in app.js): what the platform is on the left, the ways to
// sign in on the right. The ways come from /api/v1/meta: each identity provider is a button that starts its
// browser flow on the server, a local account is the form. Comes back to ?next= afterwards; a provider that
// failed comes back here with ?error=<code>.

import { useEffect, useRef, useState } from 'preact/hooks'
import { useLocation } from 'preact-iso'
import { api, unwrap } from '../../api/client'
import { chooseLocale, setLocale, t, type MessageKey } from '../../i18n'
import { href } from '../../router/paths'
import { me, meta } from '../../shell/session'
import { Lockup } from '../../ui/brand'
import { failText } from '../../ui/fail'
import { Field } from '../../ui/field'
import { Icon } from '../../ui/icons'
import { safeNext } from './next'
import './login.css'

/** The stages of delivery the left side walks through, with what each is for. */
const STAGES: readonly [MessageKey, MessageKey][] = [
  ['login.stage.requirement', 'login.stage.requirement.cap'],
  ['login.stage.architecture', 'login.stage.architecture.cap'],
  ['login.stage.design', 'login.stage.design.cap'],
  ['login.stage.develop', 'login.stage.develop.cap'],
  ['login.stage.test', 'login.stage.test.cap'],
  ['login.stage.scm', 'login.stage.scm.cap'],
  ['login.stage.release', 'login.stage.release.cap'],
  ['login.stage.ops', 'login.stage.ops.cap'],
]

const POINTS: readonly [MessageKey, MessageKey][] = [
  ['login.point.repo', 'login.point.repo.desc'],
  ['login.point.flow', 'login.point.flow.desc'],
  ['login.point.loop', 'login.point.loop.desc'],
  ['login.point.tools', 'login.point.tools.desc'],
]

/** The AI tools this build has an adapter for (builtin/adapters). */
const TOOL_COUNT = 3

function Intro() {
  return (
    <div class="auth-l">
      <div class="row" style={{ justifyContent: 'space-between' }}>
        <Lockup />
        <span class="tag">{location.host}</span>
      </div>
      <div>
        <div class="row pills">
          <span class="tag">{t('login.pill.what')}</span>
          <span class="tag">{t('login.pill.open')}</span>
        </div>
        <h1>
          {t('login.hero.1')}
          <br />
          {t('login.hero.2')}
        </h1>
        <p class="lead">{t('login.lead')}</p>
        <ul class="auth-pts">
          {POINTS.map(([title, desc]) => (
            <li key={title}>
              <b>{t(title)}</b>
              {t(desc)}
            </li>
          ))}
        </ul>
        <div class="trail" role="list" aria-label={t('login.trail.label')}>
          {STAGES.map(([name, cap], i) => (
            <div key={name} role="listitem" title={t(cap)}>
              <small>{String(i + 1).padStart(2, '0')}</small>
              {t(name)}
            </div>
          ))}
        </div>
      </div>
      <div class="auth-foot">
        <div>
          <b>{STAGES.length}</b>
          <span class="tag">{t('login.stat.stages')}</span>
        </div>
        <div>
          <b>{TOOL_COUNT}</b>
          <span class="tag">{t('login.stat.tools')}</span>
        </div>
        <div>
          <b>0</b>
          <span class="tag">{t('login.stat.silent')}</span>
        </div>
      </div>
    </div>
  )
}

export default function Login() {
  const { query, route } = useLocation()
  const next = safeNext(query.next)
  const methods = meta.value?.login_methods ?? []
  const local = methods.some((m) => m.kind === 'local')
  const providers = methods.filter((m) => m.kind !== 'local')
  const [username, setUsername] = useState('')
  const [error, setError] = useState<string | null>(
    query.error ? t('login.error.provider', { code: query.error.slice(0, 64) }) : null,
  )
  const [busy, setBusy] = useState(false)
  const password = useRef<HTMLInputElement>(null)

  // Someone already signed in has nothing to do here.
  useEffect(() => {
    if (me.value) route(href(me.value.status === 'pending' ? '/apply' : next), true)
  }, [])

  const providerStart = (id: string) => {
    const back = encodeURIComponent(href(next))
    location.assign(`${href(`/api/v1/auth/providers/${encodeURIComponent(id)}/start`)}?return_to=${back}`)
  }

  const submit = async (e: Event) => {
    e.preventDefault()
    const field = password.current
    const secret = field?.value ?? ''
    if (!username.trim() || !secret) {
      setError(t('login.error.empty'))
      return
    }
    setBusy(true)
    setError(null)
    try {
      const person = unwrap(
        await api.POST('/api/v1/auth/login', { body: { username: username.trim(), password: secret } }),
      )
      if (field) field.value = ''
      me.value = person
      await setLocale(chooseLocale(person.locale))
      route(href(person.status === 'pending' ? '/apply' : next))
    } catch (err) {
      if (field) {
        field.value = ''
        field.focus()
      }
      setError(
        failText(err, {
          'auth.invalid_credentials': t('login.error.invalid'),
          'auth.locked': t('login.error.locked'),
          'rate.limited': t('login.error.locked'),
          'user.disabled': t('login.error.disabled'),
        }),
      )
    } finally {
      setBusy(false)
    }
  }

  const org = meta.value?.organization
  return (
    <div class="auth">
      <Intro />
      <div class="auth-r">
        <form class="auth-card" onSubmit={submit} noValidate>
          <div>
            <p class="tag" style={{ marginBottom: '10px' }}>
              {org ? `${org} · ${location.host}` : location.host}
            </p>
            <h2>{t('login.title')}</h2>
          </div>
          {providers.map((m, i) => (
            <button
              key={m.id}
              type="button"
              class={i === 0 ? 'btn primary lg' : 'btn lg'}
              onClick={() => providerStart(m.id)}
            >
              {i === 0 && <Icon name="shield" />}
              {t('login.with', { name: m.name })}
            </button>
          ))}
          {local && providers.length > 0 && <div class="or tag">{t('login.or')}</div>}
          {local && (
            <>
              <Field
                id="lg-u"
                label={t('login.username')}
                placeholder={t('login.username.placeholder')}
                autocomplete="username"
                autocapitalize="off"
                spellcheck={false}
                value={username}
                onInput={(e) => setUsername(e.currentTarget.value)}
              />
              <Field
                id="lg-p"
                label={t('login.password')}
                type="password"
                placeholder={t('login.password.placeholder')}
                autocomplete="current-password"
                inputRef={password}
              />
            </>
          )}
          <div class="t12 lg-err" role="alert">
            {error}
          </div>
          {local && (
            <button class="btn lg" type="submit" disabled={busy} aria-busy={busy}>
              {busy ? t('login.submitting') : t('login.submit')}
              <span class="arr">
                <Icon name="arrow" />
              </span>
            </button>
          )}
          {methods.length === 0 && <p class="dim t13">{t('login.none')}</p>}
          <p class="faint t11 lg-foot">
            {local && (
              <>
                {t('login.foot.invite')}
                <br />
              </>
            )}
            {t('login.foot.methods')}
          </p>
        </form>
      </div>
    </div>
  )
}
