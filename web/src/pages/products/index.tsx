// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// C-01 产品与成员 (prototype V.products in app.js). Who sees which resources depends on the products they are
// in. Platform administrators see every product and create new ones; product owners see the products they
// own. Both decide on requests to join, add and remove people and make them owners. The server checks every
// change; a 403 is explained where it happens.

import { useState } from 'preact/hooks'
import { ApiError, api, unwrap, type Schemas } from '../../api/client'
import { stageName, useStages } from '../../ui/resource'
import { useLoad } from '../../ui/load'
import { t } from '../../i18n'
import { fmtNumber, fmtRelative } from '../../i18n/format'
import { role } from '../../shell/session'
import { initial } from '../../shell/user-menu'
import { Seg } from '../../ui/controls'
import { AiTip } from '../../ui/say'
import { Empty } from '../../ui/empty'
import { Icon } from '../../ui/icons'
import { Modal } from '../../ui/overlay'
import { PageHeader } from '../../ui/page-header'
import { Fail, failText } from '../../ui/fail'
import { LoadError } from '../../ui/load'
import { toast } from '../../ui/toast'
import { PeoplePicker } from './people'

type Product = Schemas['Product']
type Member = Schemas['Member']
type Request = Schemas['JoinRequest']

type Dialog =
  | { kind: 'create' }
  | { kind: 'add'; product: Product; members: readonly Member[] }
  | { kind: 'reject'; request: Request }

export default function Products() {
  const admin = role.value === 'admin'
  const products = useLoad(async () => unwrap(await api.GET('/api/v1/products')).items, [])
  const requests = useLoad(async () => unwrap(await api.GET('/api/v1/join-requests')).items, [])
  const [picked, setPicked] = useState<string | null>(null)
  const [dialog, setDialog] = useState<Dialog | null>(null)

  // Administrators manage every product, owners the ones they own.
  const mine = (products.data ?? []).filter((p) => admin || p.my_role === 'owner')
  const current = mine.find((p) => p.code === picked) ?? mine[0]

  const create = admin && (
    <button type="button" class="btn primary" onClick={() => setDialog({ kind: 'create' })}>
      <Icon name="plus" />
      {t('products.create')}
    </button>
  )

  let body
  if (products.state === 'failed' && !products.data) {
    body = (
      <div class="card">
        <LoadError error={products.error} retry={products.reload} />
      </div>
    )
  } else if (!products.data) {
    body = <div class="card" aria-busy="true" style={{ minHeight: '240px' }} />
  } else if (mine.length === 0) {
    body = (
      <div class="card">
        {admin ? (
          <Empty title={t('products.empty.title')} why={t('products.empty.why')}>
            {create}
          </Empty>
        ) : (
          <Empty title={t('products.empty_owner.title')} why={t('products.empty_owner.why')} />
        )}
      </div>
    )
  } else {
    body = (
      <div class="grid g-main">
        <ProductTable list={mine} current={current} onPick={setPicked} />
        {current && (
          <Members
            key={current.code}
            product={current}
            onAdd={(members) => setDialog({ kind: 'add', product: current, members })}
            onChanged={products.reload}
          />
        )}
      </div>
    )
  }

  const pending = (requests.data ?? []).filter((q) => q.status === 'pending')

  return (
    <>
      <PageHeader page="products" title={t('nav.products')} lead={t('products.lead')} actions={create} />
      <div class="mb">
        <AiTip say={t('products.aitip.say')} note={t('products.aitip.note')} />
      </div>
      {requests.state === 'failed' &&
        !(requests.error instanceof ApiError && requests.error.status === 403) && (
          <div class="callout bad mb" role="alert">
            <span class="grow">{t('products.requests.failed', { why: failText(requests.error) })}</span>
            <button type="button" class="btn sm" onClick={requests.reload}>
              {t('ui.retry')}
            </button>
          </div>
        )}
      {pending.length > 0 && (
        <JoinRequests
          list={pending}
          onReject={(request) => setDialog({ kind: 'reject', request })}
          onDecided={() => {
            void requests.reload()
            void products.reload()
          }}
        />
      )}
      {body}
      {dialog?.kind === 'create' && (
        <CreateProduct
          onClose={() => setDialog(null)}
          onCreated={(p) => {
            setDialog(null)
            setPicked(p.code)
            void products.reload()
            toast(t('products.create.done', { name: p.name }))
          }}
        />
      )}
      {dialog?.kind === 'add' && (
        <AddMembers
          product={dialog.product}
          members={dialog.members}
          onClose={() => setDialog(null)}
          onAdded={() => {
            setDialog(null)
            void products.reload()
            setPicked(dialog.product.code)
            // The members card reloads when the product's counts change.
          }}
        />
      )}
      {dialog?.kind === 'reject' && (
        <RejectRequest
          request={dialog.request}
          onClose={() => setDialog(null)}
          onRejected={() => {
            setDialog(null)
            void requests.reload()
            toast(t('products.requests.rejected'))
          }}
        />
      )}
    </>
  )
}

function ProductTable(props: { list: Product[]; current?: Product; onPick: (code: string) => void }) {
  return (
    <section class="card">
      <div class="tbl-wrap" tabIndex={0}>
        <table>
          <thead>
            <tr>
              <th>{t('products.col.product')}</th>
              <th>{t('products.col.line')}</th>
              <th>{t('products.col.owners')}</th>
              <th class="r">{t('products.col.members')}</th>
              <th class="r">{t('products.col.resources')}</th>
            </tr>
          </thead>
          <tbody>
            {props.list.map((p) => {
              const on = p.code === props.current?.code
              return (
                <tr
                  key={p.code}
                  class={on ? 'click sel' : 'click'}
                  tabIndex={0}
                  aria-current={on ? 'true' : undefined}
                  onClick={() => props.onPick(p.code)}
                  onKeyDown={(e) => {
                    if (e.target === e.currentTarget && (e.key === 'Enter' || e.key === ' ')) {
                      e.preventDefault()
                      props.onPick(p.code)
                    }
                  }}
                >
                  <td>
                    <div class="nm">{p.name}</div>
                    <div class="sub mono">{p.code}</div>
                  </td>
                  <td class="dim">{p.line || <span class="faint">—</span>}</td>
                  <td>
                    {p.owners.length ? (
                      p.owners.map((o) => o.display_name).join(t('ui.list_sep'))
                    ) : (
                      <span class="badge warn">{t('products.no_owner')}</span>
                    )}
                  </td>
                  <td class="r num">{fmtNumber(p.member_count)}</td>
                  <td class="r num">{fmtNumber(p.resource_count)}</td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    </section>
  )
}

function Members(props: {
  product: Product
  onAdd: (members: readonly Member[]) => void
  onChanged: () => void
}) {
  const p = props.product
  // Loads the stages' names once; stageName reads them.
  useStages()
  // Reload whenever the product's counts change (someone was added, a request approved).
  const members = useLoad(
    async () =>
      unwrap(await api.GET('/api/v1/products/{product}/members', { params: { path: { product: p.code } } }))
        .items,
    [p.code, p.member_count, p.owners.length],
  )
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState<string | null>(null)

  const setRole = async (m: Member, next: Schemas['Role']) => {
    setBusy(m.user.id)
    setError(null)
    const res = await api
      .PUT('/api/v1/products/{product}/members/{user_id}', {
        params: { path: { product: p.code, user_id: m.user.id } },
        body: { role: next },
      })
      .then(unwrap)
      .catch((err: unknown) => err)
    setBusy(null)
    if (res instanceof Error) {
      setError(res)
      void members.reload()
      return
    }
    toast(
      next === 'owner'
        ? t('products.members.made_owner', { name: m.user.display_name, product: p.name })
        : t('products.members.made_member', { name: m.user.display_name, product: p.name }),
    )
    void members.reload()
    props.onChanged()
  }

  const remove = async (m: Member) => {
    setBusy(m.user.id)
    setError(null)
    const res = await api
      .DELETE('/api/v1/products/{product}/members/{user_id}', {
        params: { path: { product: p.code, user_id: m.user.id } },
      })
      .then(unwrap)
      .catch((err: unknown) => err)
    setBusy(null)
    if (res instanceof Error) {
      setError(res)
      return
    }
    toast(t('products.members.removed', { name: m.user.display_name, product: p.name }))
    void members.reload()
    props.onChanged()
  }

  const list = members.data ?? []
  return (
    <section class="card">
      <div class="card-h">
        <h3>
          {t('products.members.title', { product: p.name })}
          <span class="tag" style={{ marginLeft: '6px' }}>
            {fmtNumber(list.length)}
          </span>
        </h3>
        <button type="button" class="btn sm" onClick={() => props.onAdd(list)} disabled={!members.data}>
          <Icon name="plus" />
          {t('products.members.add')}
        </button>
      </div>
      {error !== null && (
        <div style={{ padding: '12px 18px 0' }}>
          <Fail error={error} />
        </div>
      )}
      {members.state === 'failed' && !members.data ? (
        <LoadError error={members.error} retry={members.reload} />
      ) : !members.data ? (
        <div aria-busy="true" style={{ minHeight: '120px' }} />
      ) : list.length === 0 ? (
        <Empty title={t('products.members.empty.title')} why={t('products.members.empty.why')}>
          <button type="button" class="btn" onClick={() => props.onAdd(list)}>
            <Icon name="plus" />
            {t('products.members.add')}
          </button>
        </Empty>
      ) : (
        <div class="list" style={{ padding: '2px 18px' }}>
          {list.map((m) => (
            <div class="li" key={m.user.id}>
              <span class="avatar">{initial(m.user.display_name)}</span>
              <div class="grow">
                <div class="name">{m.user.display_name}</div>
                <div class="desc">{memberLine(m)}</div>
              </div>
              <select
                class="field sm"
                style={{ width: 'auto' }}
                aria-label={t('products.members.role_label', { name: m.user.display_name })}
                value={m.role}
                disabled={busy === m.user.id}
                onChange={(e) => void setRole(m, e.currentTarget.value as Schemas['Role'])}
              >
                <option value="owner">{t('products.role.owner')}</option>
                <option value="member">{t('products.role.member')}</option>
              </select>
              <button
                type="button"
                class="btn ghost icon"
                disabled={busy === m.user.id}
                onClick={() => void remove(m)}
                aria-label={t('products.members.remove', { name: m.user.display_name })}
                title={t('products.members.remove', { name: m.user.display_name })}
              >
                <Icon name="x" />
              </button>
            </div>
          ))}
        </div>
      )}
      <p class="card-f">{t('products.members.note')}</p>
    </section>
  )
}

function memberLine(m: Member): string {
  const focus = (m.focus_stages ?? []).map((s) => stageName(s)).join(t('ui.list_sep'))
  if (m.job && focus) return t('products.members.line', { job: m.job, focus })
  return m.job || (focus ? t('products.members.focus', { focus }) : '')
}

function JoinRequests(props: { list: Request[]; onReject: (q: Request) => void; onDecided: () => void }) {
  // Loads the stages' names once; stageName reads them.
  useStages()
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const approve = async (q: Request) => {
    setBusy(q.id)
    setError(null)
    const res = await api
      .POST('/api/v1/join-requests/{request_id}/approve', { params: { path: { request_id: q.id } } })
      .then(unwrap)
      .catch((err: unknown) => err)
    setBusy(null)
    if (res instanceof Error) {
      setError(res)
      return
    }
    toast(t('products.requests.approved', { name: q.user.display_name, product: q.product.name }))
    props.onDecided()
  }
  return (
    <section class="card mb">
      <div class="card-h">
        <h3>{t('products.requests.title')}</h3>
        <span class="badge warn">
          <span class="dot pulse" />
          {t('products.requests.pending', { count: props.list.length })}
        </span>
      </div>
      {error !== null && (
        <div style={{ padding: '12px 18px 0' }}>
          <Fail error={error} />
        </div>
      )}
      <div style={{ padding: '2px 18px' }}>
        <div class="list">
          {props.list.map((q) => {
            const focus = (q.focus_stages ?? []).map((s) => stageName(s)).join(t('ui.list_sep'))
            const bits = [
              q.job,
              focus && t('products.members.focus', { focus }),
              q.identity && t('products.requests.via', { identity: q.identity }),
              fmtRelative(q.created_at),
            ].filter(Boolean)
            return (
              // The buttons move under the request when there is no room beside it (a phone, longer text).
              <div class="li" key={q.id} style={{ alignItems: 'flex-start', flexWrap: 'wrap' }}>
                <span class="avatar">{initial(q.user.display_name)}</span>
                <div class="grow" style={{ flexBasis: '180px' }}>
                  <div class="name">
                    {q.user.display_name} <span class="mono">{q.user.username}</span>
                  </div>
                  <div class="desc" style={{ whiteSpace: 'normal', lineHeight: 1.6 }}>
                    {t('products.requests.wants')} <b style={{ color: 'var(--ink)' }}>{q.product.name}</b>
                    {bits.map((b) => ` · ${b}`).join('')}
                    {q.note && (
                      <>
                        <br />
                        {t('products.requests.note', { note: q.note })}
                      </>
                    )}
                  </div>
                </div>
                <div class="row" style={{ flexWrap: 'nowrap', gap: '4px', marginLeft: 'auto' }}>
                  <button
                    type="button"
                    class="btn sm"
                    disabled={busy === q.id}
                    onClick={() => props.onReject(q)}
                  >
                    {t('products.requests.reject')}
                  </button>
                  <button
                    type="button"
                    class="btn sm ink"
                    disabled={busy === q.id}
                    onClick={() => void approve(q)}
                  >
                    <Icon name="check" />
                    {t('products.requests.approve')}
                  </button>
                </div>
              </div>
            )
          })}
        </div>
      </div>
    </section>
  )
}

function CreateProduct(props: { onClose: () => void; onCreated: (p: Product) => void }) {
  const [name, setName] = useState('')
  const [code, setCode] = useState('')
  const [line, setLine] = useState('')
  const [owners, setOwners] = useState<Schemas['UserRef'][]>([])
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const codeOk = /^[a-z0-9]+(-[a-z0-9]+)*$/.test(code) && code.length >= 2 && code.length <= 32
  const submit = async (e: Event) => {
    e.preventDefault()
    if (!name.trim() || !codeOk) return
    setBusy(true)
    setError(null)
    const res = await api
      .POST('/api/v1/products', {
        body: { name: name.trim(), code, line: line.trim() || undefined, owners: owners.map((o) => o.id) },
      })
      .then(unwrap)
      .catch((err: unknown) => err)
    setBusy(false)
    if (res instanceof Error) setError(res)
    else props.onCreated(res as Product)
  }
  const codeProblem =
    error instanceof ApiError &&
    (error.code === 'product.code_reserved' || error.code === 'product.code_taken')
  return (
    <Modal
      title={t('products.create.title')}
      onClose={props.onClose}
      foot={
        <>
          <button type="button" class="btn ghost" onClick={props.onClose}>
            {t('ui.cancel')}
          </button>
          <button
            type="submit"
            form="d-product"
            class="btn primary"
            disabled={busy || !name.trim() || !codeOk}
          >
            {t('products.create.submit')}
          </button>
        </>
      }
    >
      <form id="d-product" onSubmit={submit} style={{ display: 'contents' }}>
        <div class="grid g2" style={{ gap: '10px' }}>
          <div>
            <label class="fl" for="d-pn">
              {t('products.create.name')}
            </label>
            <input
              class="field"
              id="d-pn"
              autofocus
              maxLength={64}
              value={name}
              onInput={(e) => setName(e.currentTarget.value)}
              placeholder={t('products.create.name_ph')}
            />
          </div>
          <div>
            <label class="fl" for="d-pc">
              {t('products.create.code')}
            </label>
            <input
              class="field mono"
              id="d-pc"
              maxLength={32}
              value={code}
              aria-invalid={(code !== '' && !codeOk) || codeProblem}
              onInput={(e) => setCode(e.currentTarget.value.trim().toLowerCase())}
              placeholder={t('products.create.code_ph')}
            />
          </div>
        </div>
        <p class="hint" style={{ marginTop: '-8px' }}>
          {code !== '' && !codeOk ? t('products.create.code_bad') : t('products.create.code_hint')}
        </p>
        <div>
          <label class="fl" for="d-pl">
            {t('products.create.line')}
          </label>
          <input
            class="field"
            id="d-pl"
            maxLength={64}
            value={line}
            onInput={(e) => setLine(e.currentTarget.value)}
            placeholder={t('products.create.line_ph')}
          />
        </div>
        <PeoplePicker id="d-po" label={t('products.create.owners')} picked={owners} onChange={setOwners} />
        <Fail error={error} />
      </form>
    </Modal>
  )
}

function AddMembers(props: {
  product: Product
  members: readonly Member[]
  onClose: () => void
  onAdded: () => void
}) {
  const [people, setPeople] = useState<Schemas['UserRef'][]>([])
  const [as, setAs] = useState<Schemas['Role']>('member')
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const taken = new Map(props.members.map((m) => [m.user.id, t('products.add.already')]))
  const submit = async () => {
    setBusy(true)
    setError(null)
    const added: string[] = []
    for (const u of people) {
      const res = await api
        .PUT('/api/v1/products/{product}/members/{user_id}', {
          params: { path: { product: props.product.code, user_id: u.id } },
          body: { role: as },
        })
        .then(unwrap)
        .catch((err: unknown) => err)
      if (res instanceof Error) {
        setError(res)
        setPeople(people.filter((p) => !added.includes(p.id)))
        setBusy(false)
        return
      }
      added.push(u.id)
    }
    setBusy(false)
    toast(t('products.add.done', { count: added.length, product: props.product.name }))
    props.onAdded()
  }
  return (
    <Modal
      title={t('products.add.title', { product: props.product.name })}
      onClose={props.onClose}
      foot={
        <>
          <button type="button" class="btn ghost" onClick={props.onClose}>
            {t('ui.cancel')}
          </button>
          <button type="button" class="btn primary" disabled={busy || people.length === 0} onClick={submit}>
            {t('products.add.submit')}
          </button>
        </>
      }
    >
      <PeoplePicker
        id="d-m"
        label={t('products.add.people')}
        picked={people}
        onChange={setPeople}
        taken={taken}
      />
      <div>
        <div class="fl">{t('products.add.role')}</div>
        <Seg
          value={as}
          onChange={setAs}
          label={t('products.add.role')}
          items={[
            ['member', t('products.role.member')],
            ['owner', t('products.role.owner')],
          ]}
        />
        <p class="hint">{t('products.add.hint')}</p>
      </div>
      <Fail error={error} />
    </Modal>
  )
}

function RejectRequest(props: { request: Request; onClose: () => void; onRejected: () => void }) {
  const [reason, setReason] = useState('')
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const submit = async () => {
    setBusy(true)
    setError(null)
    const res = await api
      .POST('/api/v1/join-requests/{request_id}/reject', {
        params: { path: { request_id: props.request.id } },
        body: { reason: reason.trim() },
      })
      .then(unwrap)
      .catch((err: unknown) => err)
    setBusy(false)
    if (res instanceof Error) setError(res)
    else props.onRejected()
  }
  return (
    <Modal
      title={t('products.reject.title')}
      onClose={props.onClose}
      foot={
        <>
          <button type="button" class="btn ghost" onClick={props.onClose}>
            {t('ui.cancel')}
          </button>
          <button type="button" class="btn primary" disabled={busy || !reason.trim()} onClick={submit}>
            {t('products.reject.submit')}
          </button>
        </>
      }
    >
      <p class="dim t13">
        {t('products.reject.who', {
          name: props.request.user.display_name,
          product: props.request.product.name,
        })}
      </p>
      <div>
        <label class="fl" for="d-rj">
          {t('products.reject.reason')} <span class="faint">{t('products.reject.reason_note')}</span>
        </label>
        <textarea
          class="field"
          id="d-rj"
          rows={3}
          maxLength={500}
          autofocus
          value={reason}
          onInput={(e) => setReason(e.currentTarget.value)}
          placeholder={t('products.reject.reason_ph')}
        />
      </div>
      <Fail error={error} />
    </Modal>
  )
}
