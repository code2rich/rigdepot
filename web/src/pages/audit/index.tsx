// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// D-03 审计 (prototype V.audit in app.js). The append-only log, newest first, one tab per category: platform
// operations and sign-ins. Optional modules bring their own categories (connection details, SQL, servers,
// browser actions in the prototype) with their own pages. Platform administrators see everything; anyone
// else who opens it sees what they did and what was about them. "导出当前筛选" downloads the same selection
// as CSV, oldest first; the export itself is audited.

import { useState } from 'preact/hooks'
import { api, unwrap, type Schemas } from '../../api/client'
import { useLoad } from '../../ui/load'
import { t } from '../../i18n'
import { fmtDateTime } from '../../i18n/format'
import { role } from '../../shell/session'
import { Seg } from '../../ui/controls'
import { Empty } from '../../ui/empty'
import { Icon } from '../../ui/icons'
import { PageHeader } from '../../ui/page-header'
import { Fail } from '../../ui/fail'
import { toolName } from '../../ui/resource'
import { LoadError } from '../../ui/load'
import { toast } from '../../ui/toast'
import './audit.css'
import { changeText } from './change'

type Entry = Schemas['AuditEntry']
type Category = Schemas['AuditCategory']

export default function Audit() {
  const admin = role.value === 'admin'
  const [category, setCategory] = useState<Category>('platform')
  const page = (cursor?: string) =>
    api
      .GET('/api/v1/audit', { params: { query: { category, limit: 50, ...(cursor ? { cursor } : {}) } } })
      .then(unwrap)
  // The first page; "加载更多" appends the next ones without reloading what is shown.
  const log = useLoad(() => page(), [category])
  const [more, setMore] = useState<{ items: Entry[]; next?: string; busy: boolean; error?: unknown }>({
    items: [],
    busy: false,
  })
  const [exporting, setExporting] = useState(false)
  const [exportError, setExportError] = useState<unknown>(null)

  const pick = (c: Category) => {
    setCategory(c)
    setMore({ items: [], busy: false })
  }
  const next = more.items.length ? more.next : log.data?.next_cursor
  const loadMore = async () => {
    if (!next) return
    setMore({ ...more, busy: true, error: undefined })
    const out = await page(next).catch((err: unknown) => err)
    if (out instanceof Error) setMore({ ...more, busy: false, error: out })
    else {
      const p = out as Schemas['AuditPage']
      setMore({ items: [...more.items, ...p.items], next: p.next_cursor, busy: false })
    }
  }

  const exportCSV = async () => {
    setExporting(true)
    setExportError(null)
    const out = await api
      .GET('/api/v1/audit/export', { params: { query: { category } }, parseAs: 'blob' })
      .then((res) => unwrap(res) as unknown as Blob)
      .catch((err: unknown) => err)
    setExporting(false)
    if (!(out instanceof Blob)) {
      setExportError(out)
      return
    }
    const url = URL.createObjectURL(out)
    const a = document.createElement('a')
    a.href = url
    a.download = `audit-${category}-${new Date().toISOString().slice(0, 10)}.csv`
    document.body.append(a)
    a.click()
    a.remove()
    setTimeout(() => URL.revokeObjectURL(url), 1000)
    toast(t('audit.exported'))
  }

  const entries = [...(log.data?.items ?? []), ...more.items]
  return (
    <>
      <PageHeader
        page="audit"
        title={t('nav.audit')}
        lead={t('audit.lead')}
        actions={
          <button type="button" class="btn" disabled={exporting} onClick={() => void exportCSV()}>
            <Icon name="download" />
            {exporting ? t('audit.exporting') : t('audit.export')}
          </button>
        }
      />
      {exportError !== null && (
        <div class="mb">
          <Fail error={exportError} />
        </div>
      )}
      {!admin && (
        <div class="callout info mb">
          <span>{t('audit.only_mine')}</span>
        </div>
      )}
      <div class="mb">
        <Seg
          lined
          value={category}
          onChange={pick}
          label={t('audit.tabs')}
          items={[
            ['platform', t('audit.tab.platform')],
            ['login', t('audit.tab.login')],
          ]}
        />
      </div>
      <section class="card">
        {log.state === 'failed' && !log.data ? (
          <LoadError error={log.error} retry={log.reload} />
        ) : log.data && entries.length === 0 ? (
          <Empty
            title={t('audit.empty.title')}
            why={category === 'login' ? t('audit.empty.login') : t('audit.empty.platform')}
          />
        ) : (
          <div class="tbl-wrap" tabIndex={0}>
            {category === 'platform' ? (
              <table class="au-tbl" aria-busy={!log.data}>
                <thead>
                  <tr>
                    <th>{t('audit.col.at')}</th>
                    <th>{t('audit.col.actor')}</th>
                    <th>{t('audit.col.origin')}</th>
                    <th>{t('audit.col.action')}</th>
                    <th>{t('audit.col.object')}</th>
                    <th>{t('audit.col.change')}</th>
                    <th>{t('audit.col.result')}</th>
                  </tr>
                </thead>
                <tbody>
                  {entries.map((e) => (
                    <tr key={e.seq}>
                      <td class="dim num t12" style={{ whiteSpace: 'nowrap' }}>
                        <When at={e.at} />
                      </td>
                      <td style={{ whiteSpace: 'nowrap' }}>{actorName(e)}</td>
                      <td>{originText(e)}</td>
                      <td>{e.action_label}</td>
                      <td>
                        {e.object.label}
                        {e.object.id !== e.object.label && <div class="sub mono">{e.object.id}</div>}
                      </td>
                      <td>{changeText(e.change, t('audit.replaced'))}</td>
                      <td>
                        <Result e={e} />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            ) : (
              <table class="au-tbl login" aria-busy={!log.data}>
                <thead>
                  <tr>
                    <th>{t('audit.col.at')}</th>
                    <th>{t('audit.col.actor')}</th>
                    <th>{t('audit.col.method')}</th>
                    <th>{t('audit.col.from')}</th>
                    <th>{t('audit.col.result')}</th>
                  </tr>
                </thead>
                <tbody>
                  {entries.map((e) => (
                    <tr key={e.seq}>
                      <td class="dim num t12" style={{ whiteSpace: 'nowrap' }}>
                        <When at={e.at} />
                      </td>
                      <td style={{ whiteSpace: 'nowrap' }}>
                        {e.actor ? e.actor.display_name : e.object.label}
                      </td>
                      <td>{methodText(e)}</td>
                      <td class={e.device ? 'mono' : undefined}>{e.device?.name ?? originName(e.origin)}</td>
                      <td>
                        <Result e={e} />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>
        )}
        {next && (
          <div class="row" style={{ padding: '10px 18px', borderTop: '1px solid var(--line)' }}>
            <button type="button" class="btn sm" disabled={more.busy} onClick={() => void loadMore()}>
              {t('ui.load.more')}
            </button>
          </div>
        )}
        {more.error !== undefined && (
          <div style={{ padding: '0 18px 12px' }}>
            <Fail error={more.error} retry={() => void loadMore()} />
          </div>
        )}
        <p class="card-f">{category === 'login' ? t('audit.note.login') : t('audit.note.platform')}</p>
      </section>
    </>
  )
}

function When(props: { at: string }) {
  return (
    <time dateTime={props.at} title={fmtDateTime(props.at, { dateStyle: 'full', timeStyle: 'medium' })}>
      {fmtDateTime(props.at, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })}
    </time>
  )
}

function actorName(e: Entry): string {
  if (e.actor) return e.actor.display_name
  return e.origin === 'system' ? t('audit.system') : '—'
}

function originName(o: Entry['origin']): string {
  switch (o) {
    case 'web':
      return t('audit.origin.web')
    case 'cli':
      return t('audit.origin.cli')
    case 'system':
      return t('audit.origin.system')
  }
}

function originText(e: Entry): string {
  return [originName(e.origin), e.via_tool && toolName(e.via_tool), e.device?.name]
    .filter(Boolean)
    .join(' · ')
}

function methodText(e: Entry): string {
  const m = e.method
  const how =
    m === 'local'
      ? t('audit.method.local')
      : m === 'oidc'
        ? t('audit.method.oidc')
        : m === 'gitlab'
          ? t('audit.method.gitlab')
          : m === 'device_code'
            ? t('audit.method.device_code')
            : m === 'access_token'
              ? t('audit.method.access_token')
              : m
  return how ? `${originName(e.origin)} · ${how}` : originName(e.origin)
}

function Result(props: { e: Entry }) {
  const e = props.e
  if (e.result === 'denied') {
    return (
      <span class="badge bad">
        <span class="dot" />
        {e.reason ? t('audit.result.denied_why', { reason: e.reason }) : t('audit.result.denied')}
      </span>
    )
  }
  if (e.result === 'pending') {
    return (
      <span class="badge warn">
        <span class="dot" />
        {e.reason ? t('audit.result.pending_why', { reason: e.reason }) : t('audit.result.pending')}
      </span>
    )
  }
  return (
    <span class="ok-t">
      <span class="sq ok" />
      {t('audit.result.ok')}
    </span>
  )
}
