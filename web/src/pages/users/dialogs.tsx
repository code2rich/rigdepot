// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The dialogs of D-01, from the prototype (dialog() in app.js): inviting colleagues, the sign-in methods,
// and turning down a request to join.

import { useEffect, useRef, useState } from 'preact/hooks'
import { api, unwrap, type Schemas } from '../../api/client'
import { t } from '../../i18n'
import { href } from '../../router/paths'
import { Modal } from '../../ui/overlay'
import { Fail } from '../../ui/fail'
import { Field, FieldShell } from '../../ui/field'
import { Icon } from '../../ui/icons'
import { toast } from '../../ui/toast'
import { parseEmails } from './emails'

// ------------------------------------------------------------------ invite

export function InviteDialog(props: {
  products: Schemas['Product'][]
  onClose: () => void
  onSent: (invites: Schemas['Invite'][]) => void
}) {
  const [text, setText] = useState('')
  const [picked, setPicked] = useState<string[]>([])
  const [problem, setProblem] = useState<string | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const [links, setLinks] = useState<Schemas['Invite'][] | null>(null)

  if (links) {
    return (
      <Modal
        title={t('users.invite.links.title')}
        onClose={props.onClose}
        foot={
          <button type="button" class="btn primary" onClick={props.onClose}>
            {t('users.invite.links.done')}
          </button>
        }
      >
        <p class="dim t13">{t('users.invite.links.lead')}</p>
        {links.map((inv) => (
          <div key={inv.id}>
            <div class="fl mono">{inv.email}</div>
            <div class="us-link">
              <code>{inv.link}</code>
              <button
                type="button"
                class="btn ghost icon sm"
                aria-label={t('users.invite.links.copy', { email: inv.email })}
                title={t('users.invite.links.copy', { email: inv.email })}
                onClick={() => {
                  const done = () => toast(t('users.copied'))
                  navigator.clipboard?.writeText(inv.link ?? '').then(done, done)
                }}
              >
                <Icon name="copy" />
              </button>
            </div>
          </div>
        ))}
        <p class="hint">{t('users.invite.links.hint')}</p>
      </Modal>
    )
  }

  const send = async () => {
    const { valid, invalid } = parseEmails(text)
    if (valid.length === 0) {
      setProblem(t('users.invite.error.empty'))
      return
    }
    if (invalid.length) {
      setProblem(t('users.invite.error.invalid', { list: invalid.join(', ') }))
      return
    }
    setProblem(null)
    setError(null)
    setBusy(true)
    try {
      const res = unwrap(await api.POST('/api/v1/invites', { body: { emails: valid, products: picked } }))
      props.onSent(res.items)
      const withLink = res.items.filter((i) => i.link)
      if (withLink.length) setLinks(withLink)
      else {
        toast(t('users.invite.sent', { n: res.items.length }))
        props.onClose()
      }
    } catch (err) {
      setError(err)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={t('users.invite.title')}
      onClose={props.onClose}
      foot={
        <>
          <button type="button" class="btn ghost" onClick={props.onClose}>
            {t('users.cancel')}
          </button>
          <button type="button" class="btn primary" disabled={busy} aria-busy={busy} onClick={send}>
            {busy ? t('users.invite.sending') : t('users.invite.send')}
          </button>
        </>
      }
    >
      <FieldShell
        id="d-inv"
        label={t('users.invite.emails')}
        note={t('users.invite.emails.note')}
        error={problem}
      >
        <textarea
          class="field mono"
          id="d-inv"
          rows={3}
          placeholder="someone@example.com"
          aria-invalid={problem ? true : undefined}
          aria-describedby={problem ? 'd-inv-err' : undefined}
          value={text}
          onInput={(e) => setText(e.currentTarget.value)}
        />
      </FieldShell>
      <div role="group" aria-labelledby="d-inv-p">
        <div class="fl" id="d-inv-p">
          {t('users.invite.products')}
        </div>
        {props.products.length ? (
          <div class="row">
            {props.products.map((p) => {
              const on = picked.includes(p.code)
              return (
                <button
                  type="button"
                  key={p.code}
                  class={on ? 'badge chip brand' : 'badge chip'}
                  aria-pressed={on}
                  onClick={() => setPicked(on ? picked.filter((c) => c !== p.code) : [...picked, p.code])}
                >
                  {p.name}
                </button>
              )
            })}
          </div>
        ) : (
          <p class="faint t12">{t('users.invite.products.none')}</p>
        )}
      </div>
      <p class="hint">{t('users.invite.hint')}</p>
      {error !== null && (
        <Fail error={error} known={{ 'invite.account_exists': t('users.invite.error.exists') }} />
      )}
    </Modal>
  )
}

// ------------------------------------------------------------------ sign-in methods

/** methodDetail is what tells one sign-in method from another: its address, or how local accounts work. */
export function methodDetail(p: Schemas['IdentityProvider']): string {
  if (p.kind === 'local') return t('users.methods.local')
  const c = p.config as { issuer?: unknown; url?: unknown }
  const where = typeof c.issuer === 'string' ? c.issuer : typeof c.url === 'string' ? c.url : ''
  try {
    return where ? new URL(where).host : p.kind
  } catch {
    return where
  }
}

export function MethodsDialog(props: {
  onClose: () => void
  onSaved: (list: Schemas['IdentityProvider'][]) => void
}) {
  const [list, setList] = useState<Schemas['IdentityProvider'][] | null>(null)
  const [loadError, setLoadError] = useState<unknown>(null)
  const [on, setOn] = useState<Record<string, boolean>>({})
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const secrets = useRef<Record<string, HTMLInputElement | null>>({})

  const load = () => {
    setLoadError(null)
    api
      .GET('/api/v1/identity-providers')
      .then(unwrap)
      .then((res) => {
        setList(res.items)
        setOn(Object.fromEntries(res.items.map((p) => [p.id, p.enabled])))
      }, setLoadError)
  }
  useEffect(load, [])

  const save = async () => {
    if (!list) return
    setBusy(true)
    setError(null)
    try {
      const next = [...list]
      // Turn methods on before turning others off, so there is always one to sign in with.
      const order = [...list].sort((a, b) => Number(on[b.id]) - Number(on[a.id]))
      for (const p of order) {
        const secret = secrets.current[p.id]?.value ?? ''
        const enabled = !!on[p.id]
        if (enabled === p.enabled && !secret) continue
        const saved = unwrap(
          await api.PATCH('/api/v1/identity-providers/{provider_id}', {
            params: { path: { provider_id: p.id }, header: { 'If-Match': p.etag } },
            body: { enabled, client_secret: secret || undefined },
          }),
        )
        next[next.findIndex((x) => x.id === p.id)] = saved
      }
      props.onSaved(next)
      toast(t('users.methods.saved'))
      props.onClose()
    } catch (err) {
      setError(err)
    } finally {
      for (const el of Object.values(secrets.current)) if (el) el.value = ''
      setBusy(false)
    }
  }

  return (
    <Modal
      title={t('users.methods.title')}
      onClose={props.onClose}
      foot={
        <>
          <button type="button" class="btn ghost" onClick={props.onClose}>
            {t('users.cancel')}
          </button>
          <button type="button" class="btn primary" disabled={busy || !list} aria-busy={busy} onClick={save}>
            {t('users.methods.save')}
          </button>
        </>
      }
    >
      {loadError !== null && <Fail error={loadError} retry={load} />}
      {!list && loadError === null && (
        <p class="faint t13" aria-busy="true">
          {t('users.loading')}
        </p>
      )}
      {list && (
        <>
          <div class="list card us-methods">
            {list.map((p) => (
              <div class="li" key={p.id}>
                <div class="grow">
                  <div class="name" id={`d-m-${p.id}`}>
                    {p.name}
                  </div>
                  <div class="desc mono">{methodDetail(p)}</div>
                </div>
                <button
                  type="button"
                  class={on[p.id] ? 'switch on' : 'switch'}
                  role="switch"
                  aria-checked={!!on[p.id]}
                  aria-labelledby={`d-m-${p.id}`}
                  onClick={() => setOn({ ...on, [p.id]: !on[p.id] })}
                />
              </div>
            ))}
          </div>
          {list
            .filter((p) => p.kind !== 'local')
            .map((p) => (
              <Field
                key={p.id}
                id={`d-cs-${p.id}`}
                label={t('users.methods.secret', { name: p.name })}
                type="password"
                autocomplete="off"
                placeholder={
                  p.has_client_secret ? t('users.methods.secret.saved') : t('users.methods.secret.unset')
                }
                inputRef={(el: HTMLInputElement | null) => {
                  secrets.current[p.id] = el
                }}
                hint={t('users.methods.secret.hint')}
              />
            ))}
          <p class="hint">
            {t('users.methods.more')}{' '}
            <a class="link" href={href('/admin/settings')}>
              {t('nav.settings')}
            </a>
          </p>
        </>
      )}
      {error !== null && (
        <Fail
          error={error}
          known={{
            'identity.last_method': t('users.methods.error.last'),
            'precondition.failed': t('users.methods.error.changed'),
          }}
        />
      )}
    </Modal>
  )
}

// ------------------------------------------------------------------ reject a request

export function RejectDialog(props: {
  request: Schemas['JoinRequest']
  onClose: () => void
  onDone: (r: Schemas['JoinRequest']) => void
}) {
  const [reason, setReason] = useState('')
  const [problem, setProblem] = useState<string | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const reject = async () => {
    if (!reason.trim()) {
      setProblem(t('users.reject.error.empty'))
      return
    }
    setProblem(null)
    setBusy(true)
    setError(null)
    try {
      const done = unwrap(
        await api.POST('/api/v1/join-requests/{request_id}/reject', {
          params: { path: { request_id: props.request.id } },
          body: { reason: reason.trim() },
        }),
      )
      props.onDone(done)
      toast(t('users.reject.done'))
      props.onClose()
    } catch (err) {
      setError(err)
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      title={t('users.reject.title')}
      onClose={props.onClose}
      foot={
        <>
          <button type="button" class="btn ghost" onClick={props.onClose}>
            {t('users.cancel')}
          </button>
          <button type="button" class="btn primary" disabled={busy} aria-busy={busy} onClick={reject}>
            {t('users.reject.submit')}
          </button>
        </>
      }
    >
      <p class="dim t13">
        {t('users.reject.who', {
          name: props.request.user.display_name,
          product: props.request.product.name,
        })}
      </p>
      <FieldShell
        id="d-rj"
        label={t('users.reject.reason')}
        note={t('users.reject.reason.note')}
        error={problem}
      >
        <textarea
          class="field"
          id="d-rj"
          rows={3}
          maxLength={500}
          placeholder={t('users.reject.reason.placeholder')}
          aria-invalid={problem ? true : undefined}
          aria-describedby={problem ? 'd-rj-err' : undefined}
          value={reason}
          onInput={(e) => setReason(e.currentTarget.value)}
        />
      </FieldShell>
      {error !== null && (
        <Fail error={error} known={{ 'join_request.decided': t('users.requests.error.decided') }} />
      )}
    </Modal>
  )
}
