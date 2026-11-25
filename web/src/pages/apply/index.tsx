// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Applying for access, from the prototype (V.apply in app.js). Someone who signed in through SSO or GitLab
// for the first time has an account in status "pending": they pick a job, the stages they care about and
// the products to join, and each product's owners decide. Until one says yes they can see nothing else.
// The job and stages are saved on their profile (PATCH /me); they decide defaults only, never access.

import { useEffect, useState } from 'preact/hooks'
import { useLocation } from 'preact-iso'
import { api, unwrap, type Schemas } from '../../api/client'
import { signInPath } from '../../app'
import { t } from '../../i18n'
import { href } from '../../router/paths'
import { me } from '../../shell/session'
import { initial } from '../../shell/user-menu'
import { CenterCard } from '../../ui/center-card'
import { Empty } from '../../ui/empty'
import { Fail } from '../../ui/fail'
import { FieldShell } from '../../ui/field'
import { Icon } from '../../ui/icons'
import { JOBS, focusFor, jobOf, latestPerProduct } from './jobs'
import './apply.css'

type Loaded = {
  stages: Schemas['Stage'][]
  products: Schemas['Product'][]
  requests: Schemas['JoinRequest'][]
}

async function load(): Promise<Loaded> {
  const [stages, products, requests] = await Promise.all([
    api.GET('/api/v1/stages').then(unwrap),
    api.GET('/api/v1/products').then(unwrap),
    api.GET('/api/v1/me/join-requests').then(unwrap),
  ])
  return { stages: stages.items.filter((s) => !s.hidden), products: products.items, requests: requests.items }
}

async function signOut() {
  try {
    await api.POST('/api/v1/auth/logout')
  } finally {
    me.value = null
    location.assign(href('/login'))
  }
}

function Who(props: { person: Schemas['Me']; via?: string | undefined }) {
  return (
    <div class="li ap-who">
      <span class="avatar">{initial(props.person.display_name)}</span>
      <div class="grow">
        <div class="name">{props.person.display_name}</div>
        <div class="desc mono">{props.person.username}</div>
      </div>
      {props.via && <span class="badge">{t('apply.via', { name: props.via })}</span>}
    </div>
  )
}

export default function Apply() {
  const { route } = useLocation()
  const person = me.value
  const [data, setData] = useState<Loaded | null>(null)
  const [loadError, setLoadError] = useState<unknown>(null)
  const [again, setAgain] = useState(false)
  const [job, setJob] = useState(() => jobOf(person?.job ?? '', t))
  const [focus, setFocus] = useState<string[]>(() => person?.focus_stages ?? [])
  const [picked, setPicked] = useState<string[]>([])
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)

  const start = () => {
    setLoadError(null)
    load().then(setData, setLoadError)
  }

  useEffect(() => {
    if (!person) route(signInPath('/apply'), true)
    else if (person.status !== 'pending') route(href('/'), true)
    else start()
  }, [person?.id, person?.status])

  if (!person || person.status !== 'pending') return null

  const latest = data ? latestPerProduct(data.requests) : []
  const pending = latest.filter((r) => r.status === 'pending')
  const rejected = latest.filter((r) => r.status === 'rejected')
  const step = !data || again ? 'form' : pending.length ? 'pending' : rejected.length ? 'rejected' : 'form'
  const via = data?.requests.find((r) => r.identity)?.identity
  const owners = (code: string) =>
    data?.products
      .find((p) => p.code === code)
      ?.owners.map((o) => o.display_name)
      .join(t('apply.list_sep')) ?? ''

  const back = (
    <button type="button" class="btn ghost" onClick={signOut}>
      {t('apply.back')}
    </button>
  )

  if (step !== 'form') {
    const list = step === 'pending' ? pending : rejected
    const checkAgain = async () => {
      setError(null)
      try {
        const now = unwrap(await api.GET('/api/v1/me'))
        me.value = now
        if (now.status !== 'pending') route(href('/'))
        else start()
      } catch (err) {
        setError(err)
      }
    }
    return (
      <CenterCard
        tag={step === 'pending' ? t('apply.tag.pending') : t('apply.tag.rejected')}
        foot={
          <>
            {back}
            {step === 'rejected' ? (
              <button
                type="button"
                class="btn primary"
                onClick={() => {
                  setPicked(rejected.map((r) => r.product.code))
                  setAgain(true)
                }}
              >
                {t('apply.again')}
              </button>
            ) : (
              <button type="button" class="btn" onClick={checkAgain}>
                <Icon name="reset" />
                {t('apply.check')}
              </button>
            )}
          </>
        }
      >
        <div class="cc-body">
          <div>
            <h1>{step === 'pending' ? t('apply.pending.title') : t('apply.rejected.title')}</h1>
            <p class="dim t13 ap-lead">
              {step === 'pending' ? t('apply.pending.lead') : t('apply.rejected.lead')}
            </p>
          </div>
          <Who person={person} via={via} />
          <div>
            <div class="sect-t">{t('apply.requested')}</div>
            <div class="list">
              {list.map((r) => (
                <div class="li" key={r.id}>
                  <div class="grow">
                    <div class="name">{r.product.name}</div>
                    <div class="desc ap-why">
                      {r.status === 'rejected'
                        ? t('apply.rejected_by', {
                            name: r.decided_by?.display_name ?? t('apply.someone'),
                            reason: r.reason ?? '',
                          })
                        : owners(r.product.code)
                          ? t('apply.reviewers.any', { names: owners(r.product.code) })
                          : t('apply.reviewers.admins')}
                    </div>
                  </div>
                  {r.status === 'rejected' ? (
                    <span class="badge bad">
                      <span class="dot" />
                      {t('apply.status.rejected')}
                    </span>
                  ) : (
                    <span class="badge warn">
                      <span class="dot pulse" />
                      {t('apply.status.pending')}
                    </span>
                  )}
                </div>
              ))}
            </div>
          </div>
          {error !== null && <Fail error={error} />}
        </div>
      </CenterCard>
    )
  }

  const ready = job !== '' && focus.length > 0 && picked.length > 0
  const toggle = (list: string[], v: string) =>
    list.includes(v) ? list.filter((x) => x !== v) : [...list, v]
  const submit = async () => {
    const chosen = JOBS.find((j) => j.id === job)
    if (!ready || !chosen) return
    setBusy(true)
    setError(null)
    try {
      me.value = unwrap(
        await api.PATCH('/api/v1/me', { body: { job: t(chosen.label), focus_stages: focus } }),
      )
      unwrap(
        await api.POST('/api/v1/me/join-requests', {
          body: { products: picked, note: note.trim() || undefined },
        }),
      )
      setAgain(false)
      setNote('')
      start()
    } catch (err) {
      setError(err)
    } finally {
      setBusy(false)
    }
  }

  return (
    <CenterCard
      tag={t('apply.tag.form')}
      foot={
        <>
          {back}
          <button
            type="button"
            class="btn primary"
            disabled={!ready || busy}
            aria-busy={busy}
            title={ready ? undefined : t('apply.submit.why')}
            onClick={submit}
          >
            {busy ? t('apply.submitting') : t('apply.submit')}
          </button>
        </>
      }
    >
      <div class="cc-body">
        <div>
          <h1>{t('apply.title')}</h1>
          <p class="dim t13 ap-lead">{t('apply.lead')}</p>
        </div>
        <div>
          <Who person={person} via={via} />
          <p class="hint">{t('apply.who.hint')}</p>
        </div>
        {loadError !== null ? (
          <Fail error={loadError} retry={start} />
        ) : !data ? (
          <p class="faint t13" aria-busy="true">
            {t('apply.loading')}
          </p>
        ) : (
          <>
            <div role="group" aria-labelledby="ap-job">
              <div class="fl" id="ap-job">
                {t('apply.job')}
              </div>
              <div class="seg">
                {JOBS.map((j) => (
                  <button
                    type="button"
                    key={j.id}
                    class={job === j.id ? 'on' : ''}
                    aria-pressed={job === j.id}
                    onClick={() => {
                      setJob(j.id)
                      setFocus(focusFor(j, data.stages))
                    }}
                  >
                    {t(j.label)}
                  </button>
                ))}
              </div>
            </div>
            <div role="group" aria-labelledby="ap-focus">
              <div class="fl" id="ap-focus">
                {t('apply.focus')} <span class="faint">{t('apply.focus.note')}</span>
              </div>
              <div class="row ap-chips">
                {data.stages.map((s) => (
                  <button
                    type="button"
                    key={s.id}
                    class="badge chip"
                    aria-pressed={focus.includes(s.id)}
                    onClick={() => setFocus(toggle(focus, s.id))}
                  >
                    {s.name}
                  </button>
                ))}
              </div>
              <p class="hint">{t('apply.focus.hint')}</p>
            </div>
            <div role="group" aria-labelledby="ap-products">
              <div class="fl" id="ap-products">
                {t('apply.products')} <span class="faint">{t('apply.products.note')}</span>
              </div>
              {data.products.length === 0 ? (
                <div class="card">
                  <Empty title={t('apply.products.empty.title')} why={t('apply.products.empty.why')} />
                </div>
              ) : (
                <div class="ap-picks">
                  {data.products.map((p) => {
                    const on = picked.includes(p.code)
                    const names = p.owners.map((o) => o.display_name).join(t('apply.list_sep'))
                    return (
                      <button
                        type="button"
                        key={p.code}
                        class={on ? 'pick on' : 'pick'}
                        aria-pressed={on}
                        onClick={() => setPicked(toggle(picked, p.code))}
                      >
                        <span class="grow">
                          <span class="pk-t">{p.name}</span>
                          <span class="pk-d">
                            {[p.line, names ? t('apply.reviewers', { names }) : t('apply.reviewers.admins')]
                              .filter(Boolean)
                              .join(' · ')}
                          </span>
                        </span>
                        {on && <Icon name="check" />}
                      </button>
                    )
                  })}
                </div>
              )}
              <p class="hint">{t('apply.products.hint')}</p>
            </div>
            <FieldShell id="ap-n" label={t('apply.note')} note={t('apply.note.optional')}>
              <textarea
                class="field"
                id="ap-n"
                rows={2}
                maxLength={500}
                placeholder={t('apply.note.placeholder')}
                value={note}
                onInput={(e) => setNote(e.currentTarget.value)}
              />
            </FieldShell>
          </>
        )}
        {error !== null && (
          <Fail error={error} known={{ 'join_request.already_member': t('apply.error.already_member') }} />
        )}
      </div>
    </CenterCard>
  )
}
