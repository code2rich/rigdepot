// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// A-05 问题反馈 (the prototype's V.feedback): the feedback I can see - my own; product owners also what is
// about their products' resources; platform administrators everything (the server decides) - with the
// status tabs, and one piece in detail at /feedback/<number>: the context it came with, the timeline,
// replying, handling (status, assignee) and rating once it is resolved. Friction clues are M2.

import { Fragment } from 'preact'
import { useState } from 'preact/hooks'
import { useLocation, useRoute } from 'preact-iso'
import { api, unwrap, type Schemas } from '../../api/client'
import { fmtDateTime, fmtRelative } from '../../i18n/format'
import { t, type MessageKey } from '../../i18n'
import { href } from '../../router/paths'
import { openFeedback } from '../../shell/feedback-dialog'
import { me, role } from '../../shell/session'
import { initial } from '../../shell/user-menu'
import { Empty } from '../../ui/empty'
import { STATUSES, StatusBadge, fbNumber, originName, statusName, typeName } from '../../ui/feedback'
import { Icon } from '../../ui/icons'
import { LoadError, Loading, useLoad } from '../../ui/load'
import { failText } from '../../ui/fail'
import { PageHeader } from '../../ui/page-header'
import { toolName } from '../../ui/resource'
import { AiTip } from '../../ui/say'
import { toast } from '../../ui/toast'
import './feedback.css'

type Status = Schemas['FeedbackStatus']
type Item = Schemas['FeedbackListItem']
type Feedback = Schemas['Feedback']

const PAGE = 50

export default function FeedbackPage() {
  const { route } = useLocation()
  const { params } = useRoute()
  const [filter, setFilter] = useState<Status | 'all'>('all')
  const [more, setMore] = useState<Item[]>([])
  const [cursor, setCursor] = useState<string | undefined>(undefined)
  const [loadingMore, setLoadingMore] = useState(false)
  const member = role.value === 'member'

  const summary = useLoad(async () => unwrap(await api.GET('/api/v1/feedback/summary')))
  const list = useLoad(async () => {
    const page = unwrap(
      await api.GET('/api/v1/feedback', {
        params: { query: { limit: PAGE, ...(filter === 'all' ? {} : { status: filter }) } },
      }),
    )
    setMore([])
    setCursor(page.next_cursor)
    return page.items
  }, [filter])
  const items = [...(list.data ?? []), ...more]
  const selected = params.number ? Number(params.number) : items[0]?.number

  const loadMore = async () => {
    if (!cursor) return
    setLoadingMore(true)
    try {
      const page = unwrap(
        await api.GET('/api/v1/feedback', {
          params: { query: { limit: PAGE, cursor, ...(filter === 'all' ? {} : { status: filter }) } },
        }),
      )
      setMore((m) => [...m, ...page.items])
      setCursor(page.next_cursor)
    } catch (err) {
      toast(failText(err))
    } finally {
      setLoadingMore(false)
    }
  }

  const changed = () => {
    list.reload()
    summary.reload()
  }

  const counts = summary.data?.by_status
  const tabs: (Status | 'all')[] = [
    'all',
    'new',
    'accepted',
    'in_progress',
    'resolved',
    ...(counts?.wont_fix ? (['wont_fix'] as const) : []),
  ]

  return (
    <>
      <PageHeader
        page="feedback"
        title={t('nav.feedback')}
        lead={member ? t('feedback.lead.member') : t('feedback.lead.manager')}
        actions={
          <button type="button" class="btn primary" onClick={() => openFeedback()}>
            <Icon name="plus" />
            {t('shell.feedback')}
          </button>
        }
      />
      <div class="mb">
        <AiTip
          say={t('connect.ai.feedback.say')}
          note={t('feedback.ai.note')}
          label={t('feedback.ai.label')}
        />
      </div>
      <div class="mb">
        <div class="seg lined" role="group" aria-label={t('feedback.filter')}>
          {tabs.map((s) => (
            <button
              key={s}
              type="button"
              class={filter === s ? 'on' : undefined}
              aria-pressed={filter === s}
              onClick={() => {
                setFilter(s)
                if (params.number) route(href('/feedback'))
              }}
            >
              {s === 'all' ? t('feedback.filter.all') : statusName(s)}
              {s !== 'all' && counts && <span class="faint num"> {counts[s]}</span>}
            </button>
          ))}
        </div>
      </div>
      <div class="grid g-fb">
        <section class="card" aria-label={t('feedback.list')}>
          {list.state === 'loading' && <Loading />}
          {list.state === 'failed' && <LoadError error={list.error} retry={list.reload} />}
          {list.state === 'ready' && items.length === 0 && (
            <Empty
              title={filter === 'all' ? t('feedback.empty') : t('feedback.empty.filtered')}
              why={member ? t('feedback.empty.why.member') : t('feedback.empty.why')}
            >
              {filter === 'all' && (
                <button type="button" class="btn" onClick={() => openFeedback()}>
                  <Icon name="plus" />
                  {t('shell.feedback')}
                </button>
              )}
            </Empty>
          )}
          {items.length > 0 && (
            <div class="list rows" style={{ padding: '6px 16px' }}>
              {items.map((x) => {
                const on = x.number === selected
                return (
                  <a
                    key={x.number}
                    class={on ? 'li sel' : 'li'}
                    href={href(`/feedback/${x.number}`)}
                    aria-current={on ? 'true' : undefined}
                    style={{ alignItems: 'flex-start', color: 'inherit', textDecoration: 'none' }}
                  >
                    <div class="grow">
                      <div class="name">{x.title}</div>
                      <div class="desc">
                        <span class="mono">{fbNumber(x.number)}</span> · {typeName(x.type)} ·{' '}
                        {originName(x.origin)}
                        {x.submitter && ` · ${x.submitter.display_name}`} · {fmtRelative(x.created_at)}
                      </div>
                    </div>
                    <StatusBadge status={x.status} />
                  </a>
                )
              })}
              {cursor && (
                <div class="row" style={{ justifyContent: 'center', padding: '10px 0' }}>
                  <button type="button" class="btn sm" onClick={loadMore} disabled={loadingMore}>
                    {t('feedback.more')}
                  </button>
                </div>
              )}
            </div>
          )}
        </section>
        {selected !== undefined && <Detail key={selected} number={selected} onChanged={changed} />}
      </div>
    </>
  )
}

const SEVERITY: Record<Schemas['FeedbackSeverity'], MessageKey> = {
  low: 'feedback.severity.low',
  medium: 'feedback.severity.medium',
  high: 'feedback.severity.high',
}

const FRICTION: Record<Schemas['FrictionCount']['type'], MessageKey> = {
  tool_error: 'feedback.friction.tool_error',
  waiting_approval: 'feedback.friction.waiting_approval',
  repeated_correction: 'feedback.friction.repeated_correction',
  missing_secret: 'feedback.friction.missing_secret',
  interrupted: 'feedback.friction.interrupted',
  gate_denied: 'feedback.friction.gate_denied',
  sync_failed: 'feedback.friction.sync_failed',
}

const SCORE: MessageKey[] = [
  'feedback.score.1',
  'feedback.score.2',
  'feedback.score.3',
  'feedback.score.4',
  'feedback.score.5',
]

/** contextRows is the context a piece of feedback came with, as label and value. */
function contextRows(f: Feedback): [string, string][] {
  const c = f.context
  const rows: [string, string][] = []
  if (c.trigger) rows.push([t('feedback.ctx.trigger'), c.trigger])
  if (f.resource)
    rows.push([t('feedback.ctx.resource'), `${f.resource.name} · v${f.resource_version ?? '?'}`])
  if (c.page) rows.push([t('feedback.ctx.page'), c.page])
  if (c.browser) rows.push([t('feedback.ctx.browser'), c.browser])
  if (c.tool)
    rows.push([t('feedback.ctx.tool'), [toolName(c.tool), c.tool_version].filter(Boolean).join(' ')])
  if (c.cli_version || c.os)
    rows.push([t('feedback.ctx.cli'), [c.cli_version, c.os].filter(Boolean).join(' · ')])
  if (c.last_sync) rows.push([t('feedback.ctx.last_sync'), c.last_sync])
  if (c.frictions && c.frictions.length > 0) {
    rows.push([
      t('feedback.ctx.frictions'),
      c.frictions
        .map((x) => t('feedback.ctx.friction', { what: t(FRICTION[x.type]), count: x.count }))
        .join(' · '),
    ])
  }
  return rows
}

function Stars(props: { score: number }) {
  return (
    <span class="stars" role="img" aria-label={t('feedback.score', { count: props.score })}>
      {[1, 2, 3, 4, 5].map((i) => (
        <span key={i} class={i <= props.score ? 'on' : undefined}>
          <Icon name="star" />
        </span>
      ))}
    </span>
  )
}

function Detail(props: { number: number; onChanged: () => void }) {
  const data = useLoad(
    async () =>
      unwrap(await api.GET('/api/v1/feedback/{number}', { params: { path: { number: props.number } } })),
    [props.number],
  )
  const [current, setCurrent] = useState<Feedback | null>(null)
  const f = current ?? data.data

  if (!f) {
    return (
      <section class="card" aria-label={t('feedback.detail')}>
        {data.state === 'failed' ? <LoadError error={data.error} retry={data.reload} /> : <Loading />}
      </section>
    )
  }
  const updated = (next: Feedback) => {
    setCurrent(next)
    props.onChanged()
  }
  const mine = f.submitter?.id === me.value?.id
  const rows = contextRows(f)
  return (
    <section class="card" aria-label={t('feedback.detail')}>
      <div class="fb-head">
        <div class="row" style={{ marginBottom: '10px', gap: '6px' }}>
          <StatusBadge status={f.status} />
          <span class="badge">{typeName(f.type)}</span>
          <span class={f.severity === 'high' ? 'badge bad' : 'badge'}>
            {t('feedback.severity', { level: t(SEVERITY[f.severity]) })}
          </span>
          {f.resource && (
            <a class="badge brand mono" href={href(`/catalog/${f.resource.code}`)}>
              {f.resource.code}
            </a>
          )}
        </div>
        <h2>{f.title}</h2>
        <p class="faint t12" style={{ marginTop: '4px' }}>
          <span class="mono">{fbNumber(f.number)}</span> ·{' '}
          {t('feedback.meta', {
            who: f.submitter?.display_name ?? t('feedback.system'),
            when: fmtDateTime(f.created_at),
            origin: originName(f.origin),
          })}{' '}
          ·{' '}
          {f.assignee ? t('feedback.assignee', { name: f.assignee.display_name }) : t('feedback.unassigned')}
        </p>
      </div>
      <div class="fb-body">
        {rows.length > 0 && (
          <div>
            <div class="sect-t">{t('feedback.ctx')}</div>
            <dl class="kv lined">
              {rows.map(([k, v]) => (
                <Fragment key={k}>
                  <dt>{k}</dt>
                  <dd>{v}</dd>
                </Fragment>
              ))}
            </dl>
          </div>
        )}
        <div>
          <div class="sect-t">{t('feedback.timeline')}</div>
          <Timeline f={f} />
        </div>
        {f.status === 'resolved' && f.type !== 'friction' && <Rating f={f} mine={mine} onRated={updated} />}
        <Reply f={f} mine={mine} onChanged={updated} />
      </div>
    </section>
  )
}

function Timeline(props: { f: Feedback }) {
  const { f } = props
  return (
    <div class="timeline">
      {f.events.map((e) => {
        const who = e.by?.display_name ?? t('feedback.system')
        if (e.kind === 'created' || e.kind === 'comment') {
          const body = e.kind === 'created' ? f.body : (e.body ?? '')
          if (e.kind === 'created' && !e.by) {
            return (
              <div key={e.id} class="tl">
                <span class="mk">
                  <i />
                </span>
                <div class="sys">
                  {t('feedback.ev.system_created')}
                  <div class="bubble" style={{ marginTop: '6px', color: 'var(--ink)' }}>
                    {body}
                  </div>
                </div>
              </div>
            )
          }
          return (
            <div key={e.id} class="tl">
              <span class="avatar">{initial(who)}</span>
              <div class="bubble">
                <div class="who-l">
                  {who} · <span title={fmtDateTime(e.at)}>{fmtRelative(e.at)}</span>
                  {e.kind === 'created' && f.origin === 'ai' && ` · ${t('feedback.ev.ai')}`}
                </div>
                {body}
              </div>
            </div>
          )
        }
        const text =
          e.kind === 'status' && e.status
            ? t('feedback.ev.status', { who, status: statusName(e.status) })
            : e.kind === 'assigned'
              ? t('feedback.ev.assigned', { who, to: e.assignee?.display_name ?? '' })
              : e.kind === 'rated'
                ? t('feedback.ev.rated', { who, count: e.score ?? 0 })
                : t('feedback.ev.reopened')
        return (
          <div key={e.id} class="tl">
            <span class="mk">
              <i />
            </span>
            <div class="sys">
              {text}
              {e.kind === 'status' && e.body && `：${e.body}`}
              <span class="faint"> · {fmtRelative(e.at)}</span>
            </div>
          </div>
        )
      })}
    </div>
  )
}

function Rating(props: { f: Feedback; mine: boolean; onRated: (f: Feedback) => void }) {
  const { f } = props
  const [score, setScore] = useState(0)
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  if (f.score) {
    return (
      <div class="callout ok" style={{ display: 'block' }}>
        <div class="sect-t" style={{ marginBottom: '6px' }}>
          {t('feedback.rate.title')}
        </div>
        <div class="row">
          <Stars score={f.score} />
          <span class="dim t13">
            {t('feedback.rate.result', { count: f.score, what: t(SCORE[f.score - 1] ?? 'feedback.score.3') })}
            {f.score_note && t('feedback.rate.note', { note: f.score_note })}
          </span>
        </div>
      </div>
    )
  }
  if (!props.mine) {
    return (
      <div class="callout plain" style={{ display: 'block' }}>
        <div class="sect-t" style={{ marginBottom: '6px' }}>
          {t('feedback.rate.title')}
        </div>
        <p class="dim t13">{t('feedback.rate.waiting')}</p>
      </div>
    )
  }
  const send = async () => {
    if (!score) return
    setBusy(true)
    try {
      const next = unwrap(
        await api.POST('/api/v1/feedback/{number}/rate', {
          params: { path: { number: f.number } },
          body: { score, ...(note.trim() ? { note: note.trim() } : {}) },
        }),
      )
      toast(score <= 2 ? t('feedback.rate.reopened') : t('feedback.rate.thanks'))
      props.onRated(next)
    } catch (err) {
      toast(failText(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <div class="callout plain" style={{ display: 'block' }}>
      <div class="sect-t" style={{ marginBottom: '6px' }}>
        {t('feedback.rate.title')}
      </div>
      <p class="dim t13" style={{ marginBottom: '8px' }}>
        {t('feedback.rate.ask')}
      </p>
      <div class="row">
        <span class="stars" role="radiogroup" aria-label={t('feedback.rate.title')}>
          {[1, 2, 3, 4, 5].map((i) => (
            <button
              key={i}
              type="button"
              role="radio"
              aria-checked={score === i}
              class={i <= score ? 'on' : undefined}
              onClick={() => setScore(i)}
              aria-label={t('feedback.score', { count: i })}
              title={t(SCORE[i - 1] ?? 'feedback.score.3')}
            >
              <Icon name="star" />
            </button>
          ))}
        </span>
        {score > 0 && <span class="dim t13">{t(SCORE[score - 1] ?? 'feedback.score.3')}</span>}
      </div>
      <div class="row" style={{ marginTop: '8px', flexWrap: 'nowrap' }}>
        <input
          class="field"
          maxLength={500}
          value={note}
          onInput={(e) => setNote(e.currentTarget.value)}
          placeholder={t('feedback.rate.note.placeholder')}
          aria-label={t('feedback.rate.note.label')}
        />
        <button type="button" class="btn primary sm" onClick={send} disabled={!score || busy}>
          {t('feedback.rate.submit')}
        </button>
      </div>
    </div>
  )
}

function Reply(props: { f: Feedback; mine: boolean; onChanged: (f: Feedback) => void }) {
  const { f } = props
  const handler = role.value !== 'member'
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const users = useLoad(async () => {
    if (role.value !== 'admin') return null
    const res = await api.GET('/api/v1/users')
    return res.data?.items.filter((u) => u.status === 'active') ?? null
  })

  const run = async (fn: () => Promise<Feedback | null>, done: string) => {
    setBusy(true)
    setError('')
    try {
      const next = await fn()
      toast(done)
      setText('')
      if (next) props.onChanged(next)
    } catch (err) {
      setError(failText(err))
    } finally {
      setBusy(false)
    }
  }

  const reload = async () =>
    unwrap(await api.GET('/api/v1/feedback/{number}', { params: { path: { number: f.number } } }))

  const reply = () => {
    if (!text.trim()) {
      setError(t('feedback.reply.required'))
      return
    }
    void run(async () => {
      unwrap(
        await api.POST('/api/v1/feedback/{number}/comments', {
          params: { path: { number: f.number } },
          body: { body: text.trim() },
        }),
      )
      return reload()
    }, t('feedback.reply.sent'))
  }

  const setStatus = (status: Status) => {
    if (status === 'wont_fix' && !text.trim()) {
      setError(t('feedback.status.reason_required'))
      return
    }
    void run(
      async () =>
        unwrap(
          await api.PATCH('/api/v1/feedback/{number}', {
            params: { path: { number: f.number } },
            body: { status, ...(status === 'wont_fix' ? { status_reason: text.trim() } : {}) },
          }),
        ),
      t('feedback.status.changed', { status: statusName(status) }),
    )
  }

  const assign = (id: string) => {
    if (!id) return
    void run(
      async () =>
        unwrap(
          await api.PATCH('/api/v1/feedback/{number}', {
            params: { path: { number: f.number } },
            body: { assignee: id },
          }),
        ),
      t('feedback.assigned'),
    )
  }

  const self = me.value
  const candidates = users.data ?? (self ? [{ id: self.id, display_name: self.display_name }] : [])
  return (
    <div>
      <label class="fl" for="fb-reply">
        {t('feedback.reply')}
      </label>
      {error && (
        <div class="callout bad" role="alert" style={{ marginBottom: '8px' }}>
          {error}
        </div>
      )}
      <textarea
        class="field"
        id="fb-reply"
        rows={3}
        maxLength={5000}
        value={text}
        onInput={(e) => setText(e.currentTarget.value)}
        placeholder={handler ? t('feedback.reply.placeholder.handler') : t('feedback.reply.placeholder.mine')}
      />
      <div class="row" style={{ marginTop: '10px', justifyContent: 'flex-end' }}>
        {handler && (
          <>
            <select
              class="field sm"
              style={{ width: 'auto', marginRight: 'auto' }}
              aria-label={t('feedback.assign')}
              value=""
              onChange={(e) => assign(e.currentTarget.value)}
              disabled={busy}
            >
              <option value="" disabled>
                {t('feedback.assign.pick')}
              </option>
              {candidates.map((u) => (
                <option key={u.id} value={u.id}>
                  {u.display_name}
                </option>
              ))}
            </select>
            {STATUSES.filter((s) => s !== f.status && s !== 'new').map((s) => (
              <button key={s} type="button" class="btn sm" onClick={() => setStatus(s)} disabled={busy}>
                {t('feedback.status.set', { status: statusName(s) })}
              </button>
            ))}
          </>
        )}
        <button type="button" class="btn primary sm" onClick={reply} disabled={busy}>
          {t('feedback.reply.send')}
        </button>
      </div>
      {props.mine && !handler && <p class="hint">{t('feedback.reply.hint.mine')}</p>}
    </div>
  )
}
