// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Confirming a computer's sign-in: `rgd login` shows a code and opens /device?code=<code> in the browser
// where the person is signed in (docs/architecture/06-server.md, the device code flow). They compare the
// code and the computer with what their terminal shows and confirm or refuse. The content is the
// prototype's confirmation dialog (dialog 'confirm' in app.js); here it is a page of its own.

import { useEffect, useState } from 'preact/hooks'
import { useLocation } from 'preact-iso'
import { ApiError, api, unwrap, type Schemas } from '../../api/client'
import { signInPath } from '../../app'
import { BRAND } from '../../brand.gen'
import { t } from '../../i18n'
import { fmtRelative } from '../../i18n/format'
import { href } from '../../router/paths'
import { me } from '../../shell/session'
import { CenterCard } from '../../ui/center-card'
import { Fail } from '../../ui/fail'
import { Field } from '../../ui/field'
import { Icon } from '../../ui/icons'
import { rich } from '../../ui/rich'
import { normalizeCode, osName } from './code'
import './device.css'

type State =
  | { at: 'loading' }
  | { at: 'missing' }
  | { at: 'failed'; error: unknown }
  | { at: 'ready'; request: Schemas['DeviceRequest'] }

function EnterCode(props: { typed?: string; notFound?: boolean; retry?: () => void }) {
  const { route } = useLocation()
  const [value, setValue] = useState(props.typed ?? '')
  const [error, setError] = useState<string | null>(null)
  const submit = (e: Event) => {
    e.preventDefault()
    const code = normalizeCode(value)
    if (!code) {
      setError(t('device.enter.invalid'))
      return
    }
    if (props.retry && code === normalizeCode(props.typed)) props.retry()
    else route(`${href('/device')}?code=${code}`)
  }
  return (
    <form onSubmit={submit} noValidate>
      <div class="cc-body">
        <div>
          <h1>{props.notFound ? t('device.missing.title') : t('device.enter.title')}</h1>
          <p class="dim t13" style={{ marginTop: '6px' }}>
            {props.notFound
              ? rich('device.missing.why', { cmd: <span class="mono">{`${BRAND.cli} login`}</span> })
              : rich('device.enter.lead', { cmd: <span class="mono">{`${BRAND.cli} login`}</span> })}
          </p>
        </div>
        <div class="dv-code">
          <Field
            id="dv-c"
            label={t('device.enter.label')}
            placeholder="XXXX-XXXX"
            autocomplete="off"
            autocapitalize="characters"
            spellcheck={false}
            maxLength={12}
            value={value}
            error={error}
            onInput={(e) => setValue(e.currentTarget.value)}
          />
        </div>
      </div>
      <div class="cc-foot">
        <button type="submit" class="btn primary">
          {t('device.enter.submit')}
          <span class="arr">
            <Icon name="arrow" />
          </span>
        </button>
      </div>
    </form>
  )
}

function Outcome(props: { status: 'approved' | 'denied' | 'expired' }) {
  const cmd = <span class="mono">{`${BRAND.cli} login`}</span>
  const [title, why] =
    props.status === 'approved'
      ? [t('device.approved.title'), t('device.approved.why')]
      : props.status === 'denied'
        ? [t('device.denied.title'), t('device.denied.why')]
        : [t('device.expired.title'), rich('device.expired.why', { cmd })]
  return (
    <div class="cc-body">
      <div class={props.status === 'approved' ? 'callout ok' : 'callout plain'} role="status">
        <Icon name={props.status === 'approved' ? 'check' : props.status === 'denied' ? 'ban' : 'alert'} />
        <div class="grow">
          <div style={{ fontWeight: 600 }}>{title}</div>
          <div class="dim t13">{why}</div>
        </div>
      </div>
    </div>
  )
}

export default function Device() {
  const { query, route } = useLocation()
  const typed = query.code
  const code = normalizeCode(typed)
  const person = me.value
  const [state, setState] = useState<State>({ at: 'loading' })
  const [busy, setBusy] = useState<'approve' | 'deny' | null>(null)
  const [error, setError] = useState<unknown>(null)

  const load = () => {
    if (!code) return
    setState({ at: 'loading' })
    setError(null)
    api.GET('/api/v1/device/requests/{user_code}', { params: { path: { user_code: code } } }).then(
      (res) => {
        try {
          setState({ at: 'ready', request: unwrap(res) })
        } catch (err) {
          setState(
            err instanceof ApiError && err.status === 404 ? { at: 'missing' } : { at: 'failed', error: err },
          )
        }
      },
      (error: unknown) => setState({ at: 'failed', error }),
    )
  }

  useEffect(() => {
    if (!person) route(signInPath(code ? `/device?code=${code}` : '/device'), true)
    else load()
  }, [person?.id, code])

  if (!person) return null
  if (!code) {
    return (
      <CenterCard class="dv" tag={t('page.device')}>
        <EnterCode typed={typed} />
      </CenterCard>
    )
  }
  if (state.at === 'missing') {
    return (
      <CenterCard class="dv" tag={t('page.device')}>
        <EnterCode typed={code} notFound retry={load} />
      </CenterCard>
    )
  }
  if (state.at !== 'ready') {
    return (
      <CenterCard class="dv" tag={t('page.device')}>
        <div class="cc-body">
          {state.at === 'loading' ? (
            <p class="faint t13" aria-busy="true">
              {t('device.loading')}
            </p>
          ) : (
            <Fail error={state.error} retry={load} />
          )}
        </div>
      </CenterCard>
    )
  }

  const request = state.request
  if (request.status !== 'pending') {
    return (
      <CenterCard class="dv" tag={t('page.device')}>
        <Outcome status={request.status} />
      </CenterCard>
    )
  }

  const decide = async (how: 'approve' | 'deny') => {
    setBusy(how)
    setError(null)
    try {
      const path = { user_code: request.user_code }
      const res =
        how === 'approve'
          ? await api.POST('/api/v1/device/requests/{user_code}/approve', { params: { path } })
          : await api.POST('/api/v1/device/requests/{user_code}/deny', { params: { path } })
      unwrap(res)
      setState({ at: 'ready', request: { ...request, status: how === 'approve' ? 'approved' : 'denied' } })
    } catch (err) {
      if (err instanceof ApiError && (err.status === 404 || err.status === 410)) {
        setState({ at: 'ready', request: { ...request, status: 'expired' } })
      } else {
        setError(err)
      }
    } finally {
      setBusy(null)
    }
  }

  return (
    <CenterCard
      class="dv"
      tag={t('page.device')}
      foot={
        <>
          <button type="button" class="btn ghost" disabled={busy !== null} onClick={() => decide('deny')}>
            {busy === 'deny' ? t('device.denying') : t('device.deny')}
          </button>
          <button
            type="button"
            class="btn primary"
            disabled={busy !== null}
            aria-busy={busy === 'approve'}
            onClick={() => decide('approve')}
          >
            {busy === 'approve' ? t('device.approving') : t('device.approve')}
          </button>
        </>
      }
    >
      <div class="cc-body">
        <h1>{t('device.title')}</h1>
        <div class="pl-code">
          <span class="tag">{t('device.code')}</span>
          <b>{request.user_code}</b>
        </div>
        <dl class="kv lined">
          <dt>{t('device.computer')}</dt>
          <dd>
            <span class="mono">{request.name}</span> · {osName(request.os)}
            <span class="faint"> ({request.arch})</span>
          </dd>
          <dt>{t('device.program')}</dt>
          <dd>{t('device.program.value', { version: request.cli_version })}</dd>
          <dt>{t('device.account')}</dt>
          <dd>
            {person.display_name} <span class="mono faint">{person.username}</span>
          </dd>
          <dt>{t('device.requested')}</dt>
          <dd>
            {t('device.requested.value', {
              when: fmtRelative(request.requested_at),
              expires: fmtRelative(request.expires_at),
            })}
          </dd>
        </dl>
        <p class="hint">{t('device.hint')}</p>
        {error !== null && <Fail error={error} />}
      </div>
    </CenterCard>
  )
}
