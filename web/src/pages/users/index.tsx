// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// D-01 Users & sign-in, from the prototype (V.users in app.js). People sign in with SSO, GitLab or a local
// account and ask for products themselves the first time; here platform administrators decide on those
// requests, make or unmake administrators, disable accounts, invite people to local accounts and switch
// sign-in methods. Product owners decide on requests for their own products in Products & members too.
//
// The prototype has two tabs; the third, invitations, lists the links not yet used so they can be
// withdrawn (revokeInvite), which the prototype has no place for.

import { useEffect, useState } from 'preact/hooks'
import { ApiError, api, unwrap, type Schemas } from '../../api/client'
import { t } from '../../i18n'
import { fmtDateTime, fmtNumber, fmtRelative } from '../../i18n/format'
import { me } from '../../shell/session'
import { initial } from '../../shell/user-menu'
import { Empty } from '../../ui/empty'
import { Fail } from '../../ui/fail'
import { Icon } from '../../ui/icons'
import { PageHeader } from '../../ui/page-header'
import { toast } from '../../ui/toast'
import { InviteDialog, MethodsDialog, RejectDialog } from './dialogs'
import './users.css'

type Tab = 'users' | 'requests' | 'invites'

type Dialog = { kind: 'invite' } | { kind: 'methods' } | { kind: 'reject'; request: Schemas['JoinRequest'] }

/** Optional data: the page works without it (a product owner may not read sign-in methods or invitations). */
type Maybe<T> = { ok: true; value: T } | { ok: false; error: unknown }

const maybe = <T,>(p: Promise<T>): Promise<Maybe<T>> =>
  p.then(
    (value) => ({ ok: true as const, value }),
    (error: unknown) => ({ ok: false as const, error }),
  )

type Data = {
  users: Schemas['User'][]
  requests: Maybe<Schemas['JoinRequest'][]>
  invites: Maybe<Schemas['Invite'][]>
  stages: Map<string, string>
  methods: Schemas['IdentityProvider'][]
  products: Schemas['Product'][]
}

async function load(): Promise<Data> {
  const [users, requests, invites, stages, methods, products] = await Promise.all([
    api.GET('/api/v1/users').then(unwrap),
    maybe(api.GET('/api/v1/join-requests').then(unwrap)),
    maybe(api.GET('/api/v1/invites').then(unwrap)),
    maybe(api.GET('/api/v1/stages').then(unwrap)),
    maybe(api.GET('/api/v1/identity-providers').then(unwrap)),
    maybe(api.GET('/api/v1/products').then(unwrap)),
  ])
  return {
    users: users.items,
    requests: requests.ok ? { ok: true, value: requests.value.items } : requests,
    invites: invites.ok ? { ok: true, value: invites.value.items } : invites,
    stages: new Map(stages.ok ? stages.value.items.map((s) => [s.id, s.name]) : []),
    methods: methods.ok ? methods.value.items : [],
    products: products.ok ? products.value.items : [],
  }
}

export default function Users() {
  const [data, setData] = useState<Data | null>(null)
  const [loadError, setLoadError] = useState<unknown>(null)
  const [tab, setTab] = useState<Tab>('users')
  const [dialog, setDialog] = useState<Dialog | null>(null)
  const [actionError, setActionError] = useState<unknown>(null)
  const [working, setWorking] = useState<string | null>(null)

  const start = () => {
    setLoadError(null)
    load().then(setData, setLoadError)
  }
  useEffect(start, [])

  const sep = t('users.list_sep')
  const focusNames = (ids: string[]) => ids.map((id) => data?.stages.get(id) ?? id).join(sep)
  const methodName = (id: string) =>
    data?.methods.find((m) => m.id === id)?.name ??
    (id === 'local' ? t('users.method.local') : t('users.method.other'))

  const patch = (u: Schemas['User'], body: Schemas['UpdateUserRequest'], done: string) => async () => {
    setWorking(u.id)
    setActionError(null)
    try {
      const saved = unwrap(
        await api.PATCH('/api/v1/users/{user_id}', { params: { path: { user_id: u.id } }, body }),
      )
      setData((d) => d && { ...d, users: d.users.map((x) => (x.id === saved.id ? saved : x)) })
      if (saved.id === me.value?.id) me.value = { ...me.value, platform_admin: saved.platform_admin }
      toast(done)
    } catch (err) {
      setActionError(err)
    } finally {
      setWorking(null)
    }
  }

  const decided = (r: Schemas['JoinRequest']) =>
    setData((d) =>
      d && d.requests.ok
        ? { ...d, requests: { ok: true, value: d.requests.value.filter((x) => x.id !== r.id) } }
        : d,
    )

  const approve = (r: Schemas['JoinRequest']) => async () => {
    setWorking(r.id)
    setActionError(null)
    try {
      unwrap(
        await api.POST('/api/v1/join-requests/{request_id}/approve', {
          params: { path: { request_id: r.id } },
        }),
      )
      decided(r)
      toast(t('users.requests.approved', { name: r.user.display_name, product: r.product.name }))
    } catch (err) {
      setActionError(err)
    } finally {
      setWorking(null)
    }
  }

  const revoke = (inv: Schemas['Invite']) => async () => {
    setWorking(inv.id)
    setActionError(null)
    try {
      unwrap(await api.DELETE('/api/v1/invites/{invite_id}', { params: { path: { invite_id: inv.id } } }))
      setData((d) =>
        d && d.invites.ok
          ? { ...d, invites: { ok: true, value: d.invites.value.filter((x) => x.id !== inv.id) } }
          : d,
      )
      toast(t('users.invites.revoked', { email: inv.email }))
    } catch (err) {
      setActionError(err)
    } finally {
      setWorking(null)
    }
  }

  const count = (m: Maybe<unknown[]> | undefined) => (m?.ok ? fmtNumber(m.value.length) : '—')
  const tabs: [Tab, string, string][] = [
    ['users', t('users.tab.users'), data ? fmtNumber(data.users.length) : '—'],
    ['requests', t('users.tab.requests'), count(data?.requests)],
    ['invites', t('users.tab.invites'), count(data?.invites)],
  ]

  const known = {
    'user.last_admin': t('users.error.last_admin'),
    'join_request.decided': t('users.requests.error.decided'),
  }

  return (
    <>
      <PageHeader
        page="users"
        title={t('nav.users')}
        lead={t('users.lead')}
        actions={
          <>
            <button type="button" class="btn" onClick={() => setDialog({ kind: 'methods' })}>
              <Icon name="key" />
              {t('users.methods.open')}
            </button>
            <button type="button" class="btn primary" onClick={() => setDialog({ kind: 'invite' })}>
              <Icon name="send" />
              {t('users.invite.open')}
            </button>
          </>
        }
      />
      <div class="mb">
        <div class="seg lined">
          {tabs.map(([id, label, n]) => (
            <button
              type="button"
              key={id}
              class={tab === id ? 'on' : ''}
              aria-pressed={tab === id}
              onClick={() => setTab(id)}
            >
              {label} <span class="faint num">{n}</span>
            </button>
          ))}
        </div>
      </div>

      {loadError !== null ? (
        <section class="card">
          {loadError instanceof ApiError && loadError.status === 403 ? (
            <Empty title={t('users.denied.title')} why={t('users.denied.why')} />
          ) : (
            <div class="us-fail" style={{ paddingBottom: '14px' }}>
              <Fail error={loadError} retry={start} />
            </div>
          )}
        </section>
      ) : !data ? (
        <section class="card">
          <p class="faint t13 us-list" aria-busy="true" style={{ padding: '18px' }}>
            {t('users.loading')}
          </p>
        </section>
      ) : tab === 'users' ? (
        <section class="card">
          {actionError !== null && (
            <div class="us-fail">
              <Fail error={actionError} known={known} />
            </div>
          )}
          {data.users.length === 0 ? (
            <Empty title={t('users.empty.title')} why={t('users.empty.why')} />
          ) : (
            <div class="tbl-wrap" tabIndex={0}>
              <table>
                <thead>
                  <tr>
                    <th>{t('users.col.user')}</th>
                    <th>{t('users.col.job')}</th>
                    <th>{t('users.col.products')}</th>
                    <th>{t('users.col.role')}</th>
                    <th>{t('users.col.method')}</th>
                    <th>{t('users.col.last')}</th>
                    <th>{t('users.col.status')}</th>
                    <th class="r">{t('users.col.actions')}</th>
                  </tr>
                </thead>
                <tbody>
                  {data.users.map((u) => {
                    const off = u.status === 'disabled'
                    const busy = working === u.id
                    const adminLabel = u.platform_admin ? t('users.action.unadmin') : t('users.action.admin')
                    const statusLabel = off ? t('users.action.restore') : t('users.action.disable')
                    return (
                      <tr key={u.id} class={off ? 'off' : ''}>
                        <td>
                          <div class="row us-user">
                            <span class="avatar">{initial(u.display_name)}</span>
                            <div>
                              <div class="nm us-nowrap">{u.display_name}</div>
                              <div class="sub mono">{u.username}</div>
                            </div>
                          </div>
                        </td>
                        <td class="us-nowrap">
                          <div>{u.job || '—'}</div>
                          <div class="sub">{u.focus_stages.length ? focusNames(u.focus_stages) : '—'}</div>
                        </td>
                        <td>
                          {u.memberships.length ? (
                            <div class="row us-prods">
                              {u.memberships.map((m) => (
                                <span
                                  key={m.product.code}
                                  class={m.role === 'owner' ? 'badge brand' : 'badge'}
                                >
                                  {m.role === 'owner'
                                    ? t('users.product.owner', { product: m.product.name })
                                    : m.product.name}
                                </span>
                              ))}
                            </div>
                          ) : (
                            <span class="faint">—</span>
                          )}
                        </td>
                        <td>
                          {u.platform_admin ? (
                            <span class="badge brand">{t('shell.role.admin')}</span>
                          ) : (
                            <span class="dim us-nowrap">{t('shell.account.regular')}</span>
                          )}
                        </td>
                        <td class="dim us-nowrap">
                          {u.identities.length
                            ? u.identities.map(methodName).join(sep)
                            : t('users.method.none')}
                        </td>
                        <td
                          class="dim num t12 us-nowrap"
                          title={u.last_login_at ? fmtDateTime(u.last_login_at) : undefined}
                        >
                          {u.last_login_at ? fmtRelative(u.last_login_at) : t('users.last.never')}
                        </td>
                        <td>
                          {u.status === 'active' ? (
                            <span class="badge ok">
                              <span class="dot" />
                              {t('users.status.active')}
                            </span>
                          ) : u.status === 'pending' ? (
                            <span class="badge warn">
                              <span class="dot" />
                              {t('users.status.pending')}
                            </span>
                          ) : (
                            <span class="badge">
                              <span class="dot" />
                              {t('users.status.disabled')}
                            </span>
                          )}
                        </td>
                        <td class="r us-acts">
                          <button
                            type="button"
                            class="btn ghost icon"
                            aria-label={`${adminLabel}: ${u.display_name}`}
                            title={adminLabel}
                            disabled={busy}
                            onClick={patch(
                              u,
                              { platform_admin: !u.platform_admin },
                              u.platform_admin
                                ? t('users.done.unadmin', { name: u.display_name })
                                : t('users.done.admin', { name: u.display_name }),
                            )}
                          >
                            <Icon name="shield" />
                          </button>
                          <button
                            type="button"
                            class="btn ghost icon"
                            aria-label={`${statusLabel}: ${u.display_name}`}
                            title={statusLabel}
                            disabled={busy}
                            onClick={patch(
                              u,
                              { status: off ? 'active' : 'disabled' },
                              off
                                ? t('users.done.restore', { name: u.display_name })
                                : t('users.done.disable', { name: u.display_name }),
                            )}
                          >
                            <Icon name={off ? 'reset' : 'ban'} />
                          </button>
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
          )}
          <p class="card-f">{t('users.foot')}</p>
        </section>
      ) : tab === 'requests' ? (
        <section class="card">
          {actionError !== null && (
            <div class="us-fail">
              <Fail error={actionError} known={known} />
            </div>
          )}
          {!data.requests.ok ? (
            <div class="us-fail" style={{ paddingBottom: '14px' }}>
              <Fail error={data.requests.error} retry={start} />
            </div>
          ) : data.requests.value.length === 0 ? (
            <Empty title={t('users.requests.empty.title')} why={t('users.requests.empty.why')} />
          ) : (
            <div class="us-list">
              <div class="list">
                {data.requests.value.map((r) => (
                  <div class="li us-req" key={r.id}>
                    <span class="avatar">{initial(r.user.display_name)}</span>
                    <div class="grow">
                      <div class="name">
                        {r.user.display_name} <span class="mono">{r.user.username}</span>
                      </div>
                      <div class="desc">
                        {t('users.requests.to')} <b>{r.product.name}</b>
                        {[
                          r.job,
                          r.focus_stages?.length
                            ? t('users.requests.focus', { stages: focusNames(r.focus_stages) })
                            : '',
                          r.identity ? t('users.requests.via', { name: r.identity }) : '',
                          fmtRelative(r.created_at),
                        ]
                          .filter(Boolean)
                          .map((x) => ` · ${x}`)
                          .join('')}
                        {r.note && (
                          <>
                            <br />
                            {t('users.requests.note', { note: r.note })}
                          </>
                        )}
                      </div>
                    </div>
                    <div class="row">
                      <button
                        type="button"
                        class="btn sm"
                        disabled={working === r.id}
                        onClick={() => setDialog({ kind: 'reject', request: r })}
                      >
                        {t('users.requests.reject')}
                      </button>
                      <button
                        type="button"
                        class="btn sm ink"
                        disabled={working === r.id}
                        onClick={approve(r)}
                      >
                        <Icon name="check" />
                        {t('users.requests.approve')}
                      </button>
                    </div>
                  </div>
                ))}
              </div>
            </div>
          )}
          <p class="card-f">{t('users.requests.foot')}</p>
        </section>
      ) : (
        <section class="card">
          {actionError !== null && (
            <div class="us-fail">
              <Fail error={actionError} known={known} />
            </div>
          )}
          {!data.invites.ok ? (
            <div class="us-fail" style={{ paddingBottom: '14px' }}>
              <Fail error={data.invites.error} retry={start} />
            </div>
          ) : data.invites.value.length === 0 ? (
            <Empty title={t('users.invites.empty.title')} why={t('users.invites.empty.why')}>
              <button type="button" class="btn" onClick={() => setDialog({ kind: 'invite' })}>
                <Icon name="send" />
                {t('users.invite.open')}
              </button>
            </Empty>
          ) : (
            <div class="us-list">
              <div class="list">
                {data.invites.value.map((inv) => (
                  <div class="li" key={inv.id}>
                    <div class="grow">
                      <div class="name mono">{inv.email}</div>
                      <div class="desc">
                        {[
                          inv.products.length
                            ? inv.products.map((p) => p.name).join(sep)
                            : t('users.invites.no_products'),
                          t('users.invites.by', { name: inv.created_by.display_name }),
                          t('users.invites.expires', { when: fmtRelative(inv.expires_at) }),
                        ].join(' · ')}
                      </div>
                    </div>
                    <button
                      type="button"
                      class="btn ghost icon"
                      aria-label={t('users.invites.revoke', { email: inv.email })}
                      title={t('users.invites.revoke', { email: inv.email })}
                      disabled={working === inv.id}
                      onClick={revoke(inv)}
                    >
                      <Icon name="trash" />
                    </button>
                  </div>
                ))}
              </div>
            </div>
          )}
          <p class="card-f">{t('users.invites.foot')}</p>
        </section>
      )}

      {dialog?.kind === 'invite' && (
        <InviteDialog
          products={data?.products ?? []}
          onClose={() => setDialog(null)}
          onSent={(sent) =>
            setData((d) =>
              d && d.invites.ok ? { ...d, invites: { ok: true, value: [...sent, ...d.invites.value] } } : d,
            )
          }
        />
      )}
      {dialog?.kind === 'methods' && (
        <MethodsDialog
          onClose={() => setDialog(null)}
          onSaved={(methods) => setData((d) => d && { ...d, methods })}
        />
      )}
      {dialog?.kind === 'reject' && (
        <RejectDialog request={dialog.request} onClose={() => setDialog(null)} onDone={decided} />
      )}
    </>
  )
}
