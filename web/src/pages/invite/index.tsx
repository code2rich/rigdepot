// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Accepting an invitation: the one-time link from the invitation e-mail (/invite/<token>). The person picks
// a name, an account name and a password; that creates their local account, already in the products they
// were invited to, and signs them in. The prototype has only the dialog that sends invitations; this page
// is laid out like the other cards outside the shell (apply, setup).

import { useEffect, useRef, useState } from 'preact/hooks'
import { useLocation, useRoute } from 'preact-iso'
import { ApiError, api, unwrap, type Schemas } from '../../api/client'
import { chooseLocale, setLocale, t } from '../../i18n'
import { fmtDateTime } from '../../i18n/format'
import { href } from '../../router/paths'
import { me } from '../../shell/session'
import { CenterCard } from '../../ui/center-card'
import { Empty } from '../../ui/empty'
import { Fail, isCode } from '../../ui/fail'
import { Field } from '../../ui/field'
import { USERNAME, MIN_PASSWORD } from './rules'

type State =
  | { at: 'loading' }
  | { at: 'gone' }
  | { at: 'failed'; error: unknown }
  | { at: 'ready'; invite: Schemas['InvitePreview'] }

export default function Invite() {
  const { params } = useRoute()
  const { route } = useLocation()
  const token = params.token ?? ''
  const [state, setState] = useState<State>({ at: 'loading' })
  const [name, setName] = useState('')
  const [username, setUsername] = useState('')
  const [errors, setErrors] = useState<{
    name?: string
    username?: string
    password?: string
    again?: string
  }>({})
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const password = useRef<HTMLInputElement>(null)
  const again = useRef<HTMLInputElement>(null)

  const load = () => {
    setState({ at: 'loading' })
    api.GET('/api/v1/auth/invites/{invite_token}', { params: { path: { invite_token: token } } }).then(
      (res) => {
        try {
          setState({ at: 'ready', invite: unwrap(res) })
        } catch (err) {
          const gone =
            err instanceof ApiError && (err.status === 404 || err.status === 410 || err.status === 400)
          setState(gone ? { at: 'gone' } : { at: 'failed', error: err })
        }
      },
      (error: unknown) => setState({ at: 'failed', error }),
    )
  }
  useEffect(load, [token])

  if (state.at !== 'ready') {
    return (
      <CenterCard tag={t('invite.tag')}>
        <div class="cc-body">
          {state.at === 'loading' && (
            <p class="faint t13" aria-busy="true">
              {t('invite.loading')}
            </p>
          )}
          {state.at === 'gone' && (
            <Empty title={t('invite.gone.title')} why={t('invite.gone.why')}>
              <a class="btn" href={href('/login')}>
                {t('invite.to_login')}
              </a>
            </Empty>
          )}
          {state.at === 'failed' && <Fail error={state.error} retry={load} />}
        </div>
      </CenterCard>
    )
  }

  const invite = state.invite
  const submit = async (e: Event) => {
    e.preventDefault()
    const secret = password.current?.value ?? ''
    const next: typeof errors = {}
    if (!name.trim()) next.name = t('invite.error.name')
    if (!USERNAME.test(username)) next.username = t('invite.error.username')
    if (secret.length < MIN_PASSWORD) next.password = t('invite.error.password', { min: MIN_PASSWORD })
    else if (secret !== (again.current?.value ?? '')) next.again = t('invite.error.again')
    setErrors(next)
    setError(null)
    if (Object.keys(next).length) return
    setBusy(true)
    try {
      const person = unwrap(
        await api.POST('/api/v1/auth/invites/{invite_token}/accept', {
          params: { path: { invite_token: token } },
          body: { username, display_name: name.trim(), password: secret },
        }),
      )
      me.value = person
      await setLocale(chooseLocale(person.locale))
      route(href('/'))
    } catch (err) {
      if (isCode(err, 'invite.expired')) setState({ at: 'gone' })
      else if (isCode(err, 'user.username_taken')) setErrors({ username: t('invite.error.taken') })
      else setError(err)
    } finally {
      if (password.current) password.current.value = ''
      if (again.current) again.current.value = ''
      setBusy(false)
    }
  }

  return (
    <CenterCard tag={t('invite.tag')}>
      <form onSubmit={submit} noValidate>
        <div class="cc-body">
          <div>
            <h1>{t('invite.title')}</h1>
            <p class="dim t13" style={{ marginTop: '6px' }}>
              {t('invite.lead')}
            </p>
          </div>
          <dl class="kv lined">
            <dt>{t('invite.email')}</dt>
            <dd class="mono">{invite.email}</dd>
            <dt>{t('invite.products')}</dt>
            <dd>
              {invite.products.length ? (
                <span class="row" style={{ gap: '4px' }}>
                  {invite.products.map((p) => (
                    <span class="badge" key={p.code}>
                      {p.name}
                    </span>
                  ))}
                </span>
              ) : (
                <span class="faint">{t('invite.products.none')}</span>
              )}
            </dd>
            <dt>{t('invite.expires')}</dt>
            <dd class="num">{fmtDateTime(invite.expires_at)}</dd>
          </dl>
          <Field
            id="iv-n"
            label={t('invite.name')}
            autocomplete="name"
            maxLength={64}
            value={name}
            error={errors.name}
            onInput={(e) => setName(e.currentTarget.value)}
          />
          <Field
            id="iv-u"
            label={t('invite.username')}
            mono
            autocomplete="username"
            autocapitalize="off"
            spellcheck={false}
            maxLength={32}
            placeholder={invite.email
              .split('@')[0]
              ?.toLowerCase()
              .replace(/[^a-z0-9._-]/g, '')}
            hint={t('invite.username.hint')}
            value={username}
            error={errors.username}
            onInput={(e) => setUsername(e.currentTarget.value.trim().toLowerCase())}
          />
          <div class="grid g2" style={{ gap: '10px' }}>
            <Field
              id="iv-p"
              label={t('invite.password')}
              type="password"
              autocomplete="new-password"
              placeholder={t('invite.password.placeholder', { min: MIN_PASSWORD })}
              inputRef={password}
              error={errors.password}
            />
            <Field
              id="iv-p2"
              label={t('invite.password.again')}
              type="password"
              autocomplete="new-password"
              inputRef={again}
              error={errors.again}
            />
          </div>
          <p class="hint" style={{ marginTop: '-6px' }}>
            {t('invite.password.hint')}
          </p>
          {error !== null && (
            <Fail error={error} known={{ 'invite.account_exists': t('invite.error.account_exists') }} />
          )}
        </div>
        <div class="cc-foot">
          <a class="btn ghost" href={href('/login')}>
            {t('invite.to_login')}
          </a>
          <button type="submit" class="btn primary" disabled={busy} aria-busy={busy}>
            {busy ? t('invite.submitting') : t('invite.submit')}
          </button>
        </div>
      </form>
    </CenterCard>
  )
}
