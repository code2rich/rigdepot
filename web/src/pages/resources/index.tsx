// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// C-02 资源管理 (prototype V.resources and editDrawer in app.js). The resources the signed-in person manages:
// platform administrators all of them, product owners their products', maintainers what they maintain.
// Opening one (/manage/resources/<code>) shows the editing drawer; "新建资源" the kind picker. What the
// prototype calls 成员申请共享 and the 达标 column come with later milestones (personal resources, insights).

import { useState } from 'preact/hooks'
import { useLocation, useRoute } from 'preact-iso'
import { api, unwrap } from '../../api/client'
import { useLoad } from '../../ui/load'
import { t } from '../../i18n'
import { href } from '../../router/paths'
import { role } from '../../shell/session'
import { Empty } from '../../ui/empty'
import { Icon } from '../../ui/icons'
import { KindIcon, PolicyBadge, kindName, scopeName, stageName, useStages } from '../../ui/resource'
import { KindShape } from '../../ui/kind-shape'
import { PageHeader } from '../../ui/page-header'
import { LoadError } from '../../ui/load'
import { Create } from './create'
import { Editor } from './editor'
import './resources.css'
import {
  ChecksBadge,
  STATUS_FILTERS,
  WEB_KINDS,
  StatusBadges,
  matchesStatus,
  originName,
  statusFilterName,
  type StatusFilter,
} from './labels'

export default function Resources() {
  const { params } = useRoute()
  const { route } = useLocation()
  const open = params.code ? decodeURIComponent(params.code) : undefined
  const admin = role.value === 'admin'
  const stages = useStages()
  const list = useLoad(async () => unwrap(await api.GET('/api/v1/resources')).items, [])
  const products = useLoad(async () => unwrap(await api.GET('/api/v1/products')).items, [])
  const [kind, setKind] = useState<string>('all')
  const [status, setStatus] = useState<StatusFilter>('all')
  const [creating, setCreating] = useState(false)

  const go = (code?: string) =>
    route(href(code ? `/manage/resources/${encodeURIComponent(code)}` : '/manage/resources'))
  const all = list.data ?? []
  const shown = all.filter((r) => (kind === 'all' || r.kind === kind) && matchesStatus(r, status))
  const kinds = [...WEB_KINDS, ...new Set(all.map((r) => r.kind).filter((k) => !WEB_KINDS.includes(k)))]
  const productList = products.data ?? []

  const createButton = (cls: string) => (
    <button type="button" class={cls} onClick={() => setCreating(true)}>
      <Icon name="plus" />
      {t('resources.create')}
    </button>
  )

  let body
  if (list.state === 'failed' && !list.data) {
    body = (
      <div class="card">
        <LoadError error={list.error} retry={list.reload} />
      </div>
    )
  } else if (!list.data) {
    body = <div class="card" aria-busy="true" style={{ minHeight: '240px' }} />
  } else if (all.length === 0) {
    body = (
      <div class="card">
        {admin ? (
          <Empty title={t('resources.empty.title')} why={t('resources.empty.why')}>
            <a class="btn" href={href('/admin/settings')}>
              {t('resources.empty.connect')}
            </a>
            {createButton('btn primary')}
          </Empty>
        ) : (
          <Empty title={t('resources.empty_mine.title')} why={t('resources.empty_mine.why')}>
            {createButton('btn primary')}
          </Empty>
        )}
      </div>
    )
  } else {
    body = (
      <>
        <div class="row mb" style={{ gap: '10px' }}>
          <div class="seg" role="group" aria-label={t('resources.filter.kind')}>
            <button
              type="button"
              class={kind === 'all' ? 'on' : undefined}
              aria-pressed={kind === 'all'}
              onClick={() => setKind('all')}
            >
              {t('resources.filter.all')}
            </button>
            {kinds.map((k) => (
              <button
                key={k}
                type="button"
                class={kind === k ? 'on' : undefined}
                aria-pressed={kind === k}
                onClick={() => setKind(k)}
                data-k={k}
              >
                <KindShape kind={k} />
                {kindName(k)}
              </button>
            ))}
          </div>
          <span class="grow" />
          <select
            class="field"
            style={{ width: 'auto' }}
            aria-label={t('resources.filter.status')}
            value={status}
            onChange={(e) => setStatus(e.currentTarget.value as StatusFilter)}
          >
            {STATUS_FILTERS.map((f) => (
              <option key={f} value={f}>
                {statusFilterName(f)}
              </option>
            ))}
          </select>
        </div>
        <section class="card">
          <div class="tbl-wrap" tabIndex={0}>
            <table class="rs-tbl">
              <thead>
                <tr>
                  <th>{t('resources.col.resource')}</th>
                  <th>{t('resources.col.stages')}</th>
                  <th>{t('resources.col.scope')}</th>
                  <th>{t('resources.col.policy')}</th>
                  <th>{t('resources.col.origin')}</th>
                  <th>{t('resources.col.checks')}</th>
                  <th>{t('resources.col.status')}</th>
                </tr>
              </thead>
              <tbody>
                {shown.map((r) => (
                  <tr
                    key={r.code}
                    class={r.status === 'disabled' ? 'click off' : 'click'}
                    tabIndex={0}
                    onClick={() => go(r.code)}
                    onKeyDown={(e) => {
                      if (e.target === e.currentTarget && (e.key === 'Enter' || e.key === ' ')) {
                        e.preventDefault()
                        go(r.code)
                      }
                    }}
                  >
                    <td>
                      <div class="row" style={{ flexWrap: 'nowrap', gap: '12px' }}>
                        <KindIcon kind={r.kind} />
                        <div>
                          <div class="nm" style={{ whiteSpace: 'nowrap' }}>
                            {r.name}
                          </div>
                          <div class="sub mono">{r.code}</div>
                        </div>
                      </div>
                    </td>
                    <td>
                      <div class="row" style={{ gap: '4px' }}>
                        {r.stages.length ? (
                          r.stages.map((s) => (
                            <span key={s} class="badge">
                              {stageName(s)}
                            </span>
                          ))
                        ) : (
                          <span class="faint">—</span>
                        )}
                      </div>
                    </td>
                    <td class="dim" style={{ whiteSpace: 'nowrap' }}>
                      {scopeName(r.scope, productList)}
                    </td>
                    <td>
                      <PolicyBadge policy={r.policy} />
                    </td>
                    <td class="dim" style={{ whiteSpace: 'nowrap' }}>
                      {originName(r.origin)}
                    </td>
                    <td>
                      <ChecksBadge checks={r.checks} />
                    </td>
                    <td>
                      <div class="row" style={{ gap: '4px', flexWrap: 'nowrap' }}>
                        <StatusBadges r={r} />
                      </div>
                    </td>
                  </tr>
                ))}
                {shown.length === 0 && (
                  <tr>
                    <td colSpan={7}>
                      <Empty title={t('resources.filter.none.title')} why={t('resources.filter.none.why')}>
                        <button
                          type="button"
                          class="btn"
                          onClick={() => {
                            setKind('all')
                            setStatus('all')
                          }}
                        >
                          {t('resources.filter.clear')}
                        </button>
                      </Empty>
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </section>
      </>
    )
  }

  return (
    <>
      <PageHeader
        page="resources"
        title={t('nav.resources')}
        lead={t('resources.lead')}
        actions={createButton('btn primary')}
      />
      {body}
      {open && (
        <Editor
          key={open}
          code={open}
          products={productList}
          stages={stages}
          onClose={() => go()}
          onChanged={() => void list.reload()}
        />
      )}
      {creating && (
        <Create
          products={productList}
          stages={stages}
          onClose={() => setCreating(false)}
          onCreated={(code: string) => {
            setCreating(false)
            void list.reload()
            go(code)
          }}
        />
      )}
    </>
  )
}
