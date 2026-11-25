// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The editing drawer of one resource (prototype editDrawer), with three tabs:
//
//   配置  name, summary and how it is delivered. These are settings, not content: "保存" changes them at
//         once for everyone, without a new version (PATCH /resources/{code}). Changed on a repository
//         resource, a field becomes a platform override. Content comes from the repository, an uploaded
//         archive or the document filled in here, and always lands in the draft first.
//   检查  the check results of the draft, or of the live version when there is no draft.
//   版本  the published versions, what the draft changes, rolling back.
//
// Publishing takes the draft the person saw (its ETag), so nobody publishes content they have not seen.

import type { ComponentChildren } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { ApiError, api, unwrap, type Schemas } from '../../api/client'
import { useLoad } from '../../ui/load'
import { t } from '../../i18n'
import { fmtDateTime, fmtNumber, fmtRelative } from '../../i18n/format'
import { role } from '../../shell/session'
import { Seg } from '../../ui/controls'
import { Icon } from '../../ui/icons'
import { KindIcon, kindName, scopeName } from '../../ui/resource'
import { Drawer, Modal } from '../../ui/overlay'
import { Fail } from '../../ui/fail'
import { LoadError } from '../../ui/load'
import { toast } from '../../ui/toast'
import { isStale, parseDefinition, putDocument, tooBig, uploadBundle } from './content'
import { BasicFields, DeliveryFields, changes, settingsOf, type Settings } from './fields'
import { DIRECTORY_KINDS } from './labels'

type Resource = Schemas['ManagedResource']
type Draft = Schemas['Draft']
type Tab = 'config' | 'checks' | 'versions'

type Dialog =
  | { kind: 'narrow'; impact: Schemas['Impact']; patch: Schemas['UpdateResourceRequest']; publish: boolean }
  | { kind: 'disable' }
  | { kind: 'rollback'; version: Schemas['ResourceVersion'] }
  | { kind: 'waive'; finding: Schemas['Finding'] }

export function Editor(props: {
  code: string
  products: readonly Schemas['Product'][]
  stages: readonly Schemas['Stage'][]
  onClose: () => void
  onChanged: () => void
}) {
  const code = props.code
  const res = useLoad(
    async () => unwrap(await api.GET('/api/v1/resources/{code}', { params: { path: { code } } })),
    [code],
  )
  const r = res.data
  const draft = useLoad(
    async () =>
      r?.has_draft
        ? unwrap(await api.GET('/api/v1/resources/{code}/draft', { params: { path: { code } } }))
        : null,
    [code, r?.etag, r?.has_draft],
  )
  const [tab, setTab] = useState<Tab>('config')
  const [form, setForm] = useState<Settings | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const [dialog, setDialog] = useState<Dialog | null>(null)

  // The form starts from the resource as loaded, and again after every change that came back from the server.
  useEffect(() => {
    if (r) setForm(settingsOf(r))
  }, [r?.etag])

  const refresh = async () => {
    await res.reload()
    props.onChanged()
  }

  const failed = async (err: unknown) => {
    setError(err)
    if (isStale(err)) await refresh()
  }

  const base = r && settingsOf(r)
  const patch = base && form ? changes(base, form) : {}
  const dirty = Object.keys(patch).length > 0
  const d = draft.data ?? undefined
  const blocked = (d?.checks.blocking ?? 0) > 0

  const applyPatch = async (p: Schemas['UpdateResourceRequest'], publish: boolean) => {
    if (!r) return
    setBusy(true)
    setError(null)
    const out = await api
      .PATCH('/api/v1/resources/{code}', {
        params: { path: { code }, header: { 'If-Match': r.etag } },
        body: p,
      })
      .then(unwrap)
      .catch((err: unknown) => err)
    setBusy(false)
    if (out instanceof Error) {
      await failed(out)
      return
    }
    res.set(out as Resource)
    props.onChanged()
    if (publish) await doPublish()
    else toast(t('resources.edit.saved'))
  }

  // Saving a narrower scope takes the resource away from people: say how many before doing it.
  const save = async (publish: boolean) => {
    if (!r) return
    if (!dirty) {
      if (publish) await doPublish()
      return
    }
    if (patch.scope && r.status === 'published') {
      setBusy(true)
      const impact = await api
        .GET('/api/v1/resources/{code}/impact', { params: { path: { code }, query: { scope: patch.scope } } })
        .then(unwrap)
        .catch((err: unknown) => err)
      setBusy(false)
      if (impact instanceof Error) {
        setError(impact)
        return
      }
      if ((impact as Schemas['Impact']).people > 0) {
        setDialog({ kind: 'narrow', impact: impact as Schemas['Impact'], patch, publish })
        return
      }
    }
    await applyPatch(patch, publish)
  }

  const doPublish = async () => {
    if (!d) return
    setBusy(true)
    setError(null)
    const out = await api
      .POST('/api/v1/resources/{code}/publish', {
        params: { path: { code }, header: { 'If-Match': d.etag } },
        body: {},
      })
      .then(unwrap)
      .catch((err: unknown) => err)
    setBusy(false)
    if (out instanceof Error) {
      await failed(out)
      if (isStale(out)) await draft.reload()
      return
    }
    toast(t('resources.edit.published', { version: fmtNumber((out as Schemas['ResourceVersion']).number) }))
    await refresh()
  }

  const enable = async () => {
    if (!r) return
    setBusy(true)
    setError(null)
    const out = await api
      .POST('/api/v1/resources/{code}/enable', { params: { path: { code }, header: { 'If-Match': r.etag } } })
      .then(unwrap)
      .catch((err: unknown) => err)
    setBusy(false)
    if (out instanceof Error) {
      await failed(out)
      return
    }
    res.set(out as Resource)
    props.onChanged()
    toast(t('resources.edit.enabled'))
  }

  const reset = async () => {
    if (!r) return
    await applyPatch({ reset: r.overrides }, false)
  }

  const label = r ? r.name : code
  if (!r || !form) {
    return (
      <Drawer label={label} onClose={props.onClose} head={<h2>{code}</h2>}>
        {res.state === 'failed' ? (
          <LoadError error={res.error} retry={res.reload} />
        ) : (
          <div aria-busy="true" />
        )}
      </Drawer>
    )
  }

  const tabs: [Tab, ComponentChildren][] = [
    ['config', t('resources.tab.config')],
    [
      'checks',
      blocked || (!d && r.checks.blocking > 0) ? (
        <>
          {t('resources.tab.checks')} <span class="sq bad" />
        </>
      ) : (
        t('resources.tab.checks')
      ),
    ],
    ['versions', t('resources.tab.versions')],
  ]

  const canPublish = !!d && !blocked
  const foot = (
    <>
      {r.status === 'published' && (
        <button
          type="button"
          class="btn ghost danger"
          style={{ marginRight: 'auto' }}
          disabled={busy}
          onClick={() => setDialog({ kind: 'disable' })}
        >
          <Icon name="ban" />
          {t('resources.edit.disable')}
        </button>
      )}
      {r.status === 'disabled' && (
        <button
          type="button"
          class="btn ghost"
          style={{ marginRight: 'auto' }}
          disabled={busy}
          onClick={() => void enable()}
        >
          <Icon name="reset" />
          {t('resources.edit.enable')}
        </button>
      )}
      <button type="button" class="btn ghost" onClick={props.onClose}>
        {t('ui.cancel')}
      </button>
      {tab === 'config' && (
        <>
          <button
            type="button"
            class="btn"
            disabled={busy || !dirty || !form.name.trim()}
            onClick={() => void save(false)}
          >
            {t('ui.save')}
          </button>
          {d && (
            <button
              type="button"
              class="btn primary"
              disabled={busy || !canPublish || !form.name.trim()}
              title={blocked ? t('resources.edit.publish_blocked') : undefined}
              onClick={() => void save(true)}
            >
              {dirty ? t('resources.edit.save_publish') : t('resources.edit.publish')}
            </button>
          )}
        </>
      )}
    </>
  )

  return (
    <>
      <Drawer
        label={t('resources.edit.label', { kind: kindName(r.kind), name: r.name })}
        onClose={props.onClose}
        wide={r.kind === 'workflow'}
        head={
          <div class="row">
            <KindIcon kind={r.kind} />
            <div style={{ minWidth: 0 }}>
              <h2>{r.name}</h2>
              <p>
                {kindName(r.kind)} · {statusLine(r)}
              </p>
            </div>
          </div>
        }
        tabs={<Seg value={tab} onChange={setTab} items={tabs} lined label={t('resources.edit.tabs')} />}
        foot={foot}
      >
        {error !== null && <Fail error={error} />}
        {tab === 'config' && blocked && (
          <div class="callout bad">
            <Icon name="alert" />
            <span>{t('resources.edit.blocked_note')}</span>
          </div>
        )}
        {tab === 'config' && (
          <ConfigTab
            r={r}
            draft={d}
            form={form}
            setForm={setForm}
            stages={props.stages}
            products={props.products}
            busy={busy}
            onReset={() => void reset()}
            onDraft={async () => {
              await refresh()
              await draft.reload()
            }}
            onDiff={() => setTab('versions')}
          />
        )}
        {tab === 'checks' && (
          <ChecksTab r={r} draft={d} onWaive={(finding) => setDialog({ kind: 'waive', finding })} />
        )}
        {tab === 'versions' && (
          <VersionsTab r={r} draft={d} onRollback={(version) => setDialog({ kind: 'rollback', version })} />
        )}
      </Drawer>
      {dialog?.kind === 'narrow' && (
        <NarrowDialog
          impact={dialog.impact}
          scope={scopeName(dialog.patch.scope ?? '', props.products)}
          onClose={() => setDialog(null)}
          onConfirm={() => {
            const { patch: p, publish } = dialog
            setDialog(null)
            void applyPatch(p, publish)
          }}
        />
      )}
      {dialog?.kind === 'disable' && (
        <DisableDialog
          r={r}
          onClose={() => setDialog(null)}
          onDone={async (next) => {
            setDialog(null)
            res.set(next)
            props.onChanged()
            toast(t('resources.edit.disabled'))
          }}
          onStale={refresh}
        />
      )}
      {dialog?.kind === 'rollback' && (
        <RollbackDialog
          r={r}
          version={dialog.version}
          onClose={() => setDialog(null)}
          onDone={async (v) => {
            setDialog(null)
            toast(
              t('resources.versions.rolled_back', {
                version: fmtNumber(v.number),
                from: fmtNumber(dialog.version.number),
              }),
            )
            await refresh()
          }}
          onStale={refresh}
        />
      )}
      {dialog?.kind === 'waive' && (
        <WaiveDialog
          code={code}
          finding={dialog.finding}
          onClose={() => setDialog(null)}
          onDone={async () => {
            setDialog(null)
            toast(t('resources.checks.waived'))
            await refresh()
            await draft.reload()
          }}
        />
      )}
    </>
  )
}

function statusLine(r: Resource): string {
  if (r.status === 'disabled') return t('resources.edit.status_disabled')
  if (r.status === 'draft' || r.version === undefined) return t('resources.edit.status_draft')
  return r.has_draft
    ? t('resources.edit.status_pending', { version: fmtNumber(r.version) })
    : t('resources.edit.status_live', { version: fmtNumber(r.version) })
}

function overrideName(f: Schemas['OverridableField']): string {
  switch (f) {
    case 'name':
      return t('resources.edit.name')
    case 'summary':
      return t('resources.edit.summary')
    case 'stages':
      return t('resources.edit.stages')
    case 'audience':
      return t('resources.edit.audience')
    case 'scope':
      return t('resources.edit.scope')
    case 'policy':
      return t('resources.edit.policy')
    case 'when':
      return t('resources.edit.when')
    case 'try':
      return t('resources.edit.try')
    case 'maintainers':
      return t('resources.edit.maintainers')
    case 'delivery':
      return t('resources.edit.via')
  }
}

function ConfigTab(props: {
  r: Resource
  draft?: Draft
  form: Settings
  setForm: (s: Settings) => void
  stages: readonly Schemas['Stage'][]
  products: readonly Schemas['Product'][]
  busy: boolean
  onReset: () => void
  onDraft: () => Promise<void>
  onDiff: () => void
}) {
  const r = props.r
  const sep = t('ui.list_sep')
  return (
    <>
      {r.status === 'disabled' && (
        <div class="callout plain">
          <span>
            {r.disabled_reason
              ? t('resources.edit.disabled_reason', { reason: r.disabled_reason })
              : t('resources.edit.disabled_note')}
          </span>
        </div>
      )}
      {r.overrides.length > 0 && (
        <div class="callout info rs-note">
          <span class="grow">
            {t('resources.edit.overrides', { fields: r.overrides.map(overrideName).join(sep) })}
            {(r.repository_differs ?? []).length > 0 && (
              <>
                {' '}
                {t('resources.edit.differs', {
                  fields: (r.repository_differs ?? []).map(overrideName).join(sep),
                })}
              </>
            )}
          </span>
          <button type="button" class="btn sm" disabled={props.busy} onClick={props.onReset}>
            {t('resources.edit.reset')}
          </button>
        </div>
      )}
      <BasicFields value={props.form} onChange={props.setForm} code={r.code} kind={r.kind} />
      <Content r={r} draft={props.draft} onDraft={props.onDraft} onDiff={props.onDiff} />
      <DeliveryFields
        value={props.form}
        onChange={props.setForm}
        kind={r.kind}
        stages={props.stages}
        products={props.products}
      />
      <p class="hint" style={{ marginTop: '-8px' }}>
        {t('resources.edit.save_hint')}
      </p>
    </>
  )
}

/** Content says where the resource's content comes from and lets people replace it, as a draft. */
function Content(props: { r: Resource; draft?: Draft; onDraft: () => Promise<void>; onDiff: () => void }) {
  const r = props.r
  const d = props.draft
  const file = useRef<HTMLInputElement>(null)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const [text, setText] = useState<string | null>(null)
  const [syntax, setSyntax] = useState(false)
  const documentKind = !DIRECTORY_KINDS.includes(r.kind)

  // The document to edit: the draft's when there is one, else the live version's from the catalog.
  useEffect(() => {
    if (r.origin === 'repo' || !documentKind) return
    let live = true
    const load = async () => {
      if (d) return d.definition
      if (r.status === 'draft') return {}
      const res = await api.GET('/api/v1/catalog/{code}', { params: { path: { code: r.code } } })
      return res.data?.definition ?? null
    }
    load().then(
      (def) => live && setText(def ? JSON.stringify(def, null, 2) : ''),
      () => live && setText(''),
    )
    return () => {
      live = false
    }
  }, [r.code, d?.etag])

  const pending = d && (
    <div class="callout rs-note">
      <span class="grow">
        {d.updated_by
          ? t('resources.edit.draft_by', { who: d.updated_by.display_name, when: fmtRelative(d.updated_at) })
          : t('resources.edit.draft_at', { when: fmtRelative(d.updated_at) })}
      </span>
      {r.version !== undefined && (
        <button type="button" class="btn sm" onClick={props.onDiff}>
          {t('resources.edit.see_diff')}
        </button>
      )}
    </div>
  )

  if (r.origin === 'repo') {
    return (
      <>
        <div class="callout info">
          <span>
            {r.source
              ? t('resources.edit.from_repo', { source: r.source.name, path: r.source.path })
              : t('resources.edit.from_repo_gone')}
          </span>
        </div>
        {pending}
      </>
    )
  }

  const upload = async (f: File) => {
    if (tooBig(f)) {
      setError(
        new ApiError(413, {
          type: 'about:blank',
          title: '',
          status: 413,
          code: 'request.too_large',
          detail: t('resources.upload.too_big'),
        }),
      )
      return
    }
    setBusy(true)
    setError(null)
    const out = await uploadBundle(r.code, f, { draftEtag: d?.etag }).catch((err: unknown) => err)
    setBusy(false)
    if (out instanceof Error) {
      setError(out)
      return
    }
    toast(t('resources.upload.done'))
    await props.onDraft()
  }

  if (!documentKind) {
    return (
      <>
        <input
          ref={file}
          type="file"
          hidden
          accept=".zip,.tar,.tgz,.tar.gz,application/zip,application/x-tar,application/gzip"
          onChange={(e) => {
            const f = e.currentTarget.files?.[0]
            e.currentTarget.value = ''
            if (f) void upload(f)
          }}
        />
        <button type="button" class="pick" disabled={busy} onClick={() => file.current?.click()}>
          <Icon name="download" />
          <span class="grow">
            <span class="pk-t">{busy ? t('resources.upload.sending') : t('resources.upload.title')}</span>
            <span class="pk-d">{t('resources.upload.desc')}</span>
          </span>
        </button>
        <Fail error={error} />
        {pending}
      </>
    )
  }

  const saveDocument = async () => {
    const def = parseDefinition(text ?? '')
    if (def instanceof SyntaxError) {
      setSyntax(true)
      return
    }
    setBusy(true)
    setError(null)
    const out = await putDocument(
      r.code,
      {
        kind: r.kind,
        meta: {
          name: r.name,
          summary: r.summary,
          stages: r.stages,
          audience: r.audience,
          scope: r.scope,
          policy: r.policy,
          when: r.when,
          secrets: r.secrets,
        },
        definition: def,
      },
      d?.etag,
    ).catch((err: unknown) => err)
    setBusy(false)
    if (out instanceof Error) {
      setError(out)
      return
    }
    toast(t('resources.document.saved'))
    await props.onDraft()
  }

  return (
    <>
      <div>
        <label class="fl" for="e-json">
          {r.kind === 'mcp' ? t('resources.document.mcp') : t('resources.document.label')}{' '}
          <span class="faint">{t('resources.document.note')}</span>
        </label>
        <textarea
          class="field mono"
          id="e-json"
          rows={9}
          spellcheck={false}
          value={text ?? ''}
          disabled={text === null}
          aria-invalid={syntax}
          onInput={(e) => {
            setText(e.currentTarget.value)
            setSyntax(false)
          }}
        />
        <p class="hint" role={syntax ? 'alert' : undefined}>
          {syntax ? t('resources.document.syntax') : t('resources.document.hint')}
        </p>
        <div class="row" style={{ marginTop: '8px' }}>
          <button
            type="button"
            class="btn sm"
            disabled={busy || text === null}
            onClick={() => void saveDocument()}
          >
            {t('resources.document.save')}
          </button>
        </div>
      </div>
      <Fail error={error} />
      {pending}
    </>
  )
}

function ChecksTab(props: { r: Resource; draft?: Draft; onWaive: (f: Schemas['Finding']) => void }) {
  const admin = role.value === 'admin'
  const r = props.r
  const rules = useLoad(async () => unwrap(await api.GET('/api/v1/check-rules')), [])
  const live = useLoad(
    async () =>
      !props.draft && r.version !== undefined
        ? unwrap(
            await api.GET('/api/v1/resources/{code}/versions/{number}', {
              params: { path: { code: r.code, number: r.version } },
            }),
          )
        : null,
    [r.code, r.version, !!props.draft],
  )
  const waivable = new Set((rules.data?.rules ?? []).filter((x) => x.waivable).map((x) => x.id))
  const findings = props.draft ? props.draft.checks.findings : (live.data?.findings ?? [])
  const blocking = props.draft ? props.draft.checks.blocking : r.checks.blocking
  const ruleset = props.draft?.checks.ruleset ?? live.data?.ruleset ?? rules.data?.version
  const loading = !props.draft && r.version !== undefined && live.state !== 'ready'

  return (
    <div>
      <div class="sect-t">
        {props.draft ? t('resources.checks.title_draft') : t('resources.checks.title_live')}
        <span class="n">{t('resources.checks.lead')}</span>
      </div>
      <div class="card pad">
        {live.state === 'failed' ? (
          <LoadError error={live.error} />
        ) : loading ? (
          <div aria-busy="true" style={{ minHeight: '40px' }} />
        ) : !props.draft && r.version === undefined ? (
          <p class="faint t13">{t('resources.checks.nothing')}</p>
        ) : findings.length === 0 ? (
          <div class="t13">
            <span class="badge ok">
              <span class="dot" />
              {t('resources.checks.passed')}
            </span>{' '}
            <span class="dim">
              {(rules.data?.rules ?? []).map((x) => x.name).join(t('ui.list_sep'))}
              {ruleset !== undefined &&
                ` · ${t('resources.checks.ruleset', { version: fmtNumber(ruleset) })}`}
            </span>
          </div>
        ) : (
          <>
            <div class="list">
              {findings.map((f) => (
                <div key={f.id} class="li" style={{ padding: '8px 0', alignItems: 'flex-start' }}>
                  {f.waiver ? (
                    <span class="badge" title={f.waiver.reason}>
                      {t('resources.checks.waived_badge')}
                    </span>
                  ) : (
                    <span class={f.severity === 'must_fix' ? 'badge bad' : 'badge warn'}>
                      {f.severity === 'must_fix'
                        ? t('resources.checks.must_fix')
                        : t('resources.checks.advise')}
                    </span>
                  )}
                  <div class="grow t13">
                    {f.message}{' '}
                    {f.file && (
                      <span class="mono faint">
                        {f.file}
                        {f.line !== undefined && `:${f.line}`}
                      </span>
                    )}
                    {f.waiver && (
                      <div class="faint t12">
                        {t('resources.checks.waiver', {
                          who: f.waiver.by.display_name,
                          reason: f.waiver.reason,
                        })}
                      </div>
                    )}
                  </div>
                  {admin && props.draft && !f.waiver && f.severity === 'must_fix' && waivable.has(f.rule) && (
                    <button type="button" class="btn sm" onClick={() => props.onWaive(f)}>
                      {t('resources.checks.waive')}
                    </button>
                  )}
                </div>
              ))}
            </div>
            <p class="hint">
              {blocking > 0 ? t('resources.checks.blocks') : t('resources.checks.advisory_only')}
            </p>
          </>
        )}
      </div>
    </div>
  )
}

function VersionsTab(props: {
  r: Resource
  draft?: Draft
  onRollback: (v: Schemas['ResourceVersion']) => void
}) {
  const r = props.r
  const page = (cursor?: string) =>
    api
      .GET('/api/v1/resources/{code}/versions', {
        params: { path: { code: r.code }, query: cursor ? { cursor } : {} },
      })
      .then(unwrap)
  // The newest page; "加载更多" appends older ones without reloading what is shown.
  const versions = useLoad(() => page(), [r.code, r.version])
  const [older, setOlder] = useState<{ items: Schemas['ResourceVersion'][]; next?: string; busy: boolean }>({
    items: [],
    busy: false,
  })
  const [olderError, setOlderError] = useState<unknown>(null)
  useEffect(() => setOlder({ items: [], busy: false }), [r.code, r.version])
  const next = older.items.length ? older.next : versions.data?.next_cursor
  const loadOlder = async () => {
    if (!next) return
    setOlder({ ...older, busy: true })
    setOlderError(null)
    const out = await page(next).catch((err: unknown) => err)
    if (out instanceof Error) {
      setOlder({ ...older, busy: false })
      setOlderError(out)
    } else {
      const p = out as Schemas['ResourceVersionPage']
      setOlder({ items: [...older.items, ...p.items], next: p.next_cursor, busy: false })
    }
  }
  const [diff, setDiff] = useState<Schemas['DraftDiff'] | 'loading' | null>(null)
  const [diffError, setDiffError] = useState<unknown>(null)

  const showDiff = async () => {
    if (diff) {
      setDiff(null)
      return
    }
    setDiff('loading')
    setDiffError(null)
    const out = await api
      .GET('/api/v1/resources/{code}/draft/diff', { params: { path: { code: r.code } } })
      .then(unwrap)
      .catch((err: unknown) => err)
    if (out instanceof Error) {
      setDiff(null)
      setDiffError(out)
    } else setDiff(out as Schemas['DraftDiff'])
  }

  return (
    <>
      {props.draft && r.version !== undefined && (
        <div class="callout rs-note">
          <span class="grow">{t('resources.versions.pending', { version: fmtNumber(r.version) })}</span>
          <button
            type="button"
            class="btn"
            aria-expanded={!!diff && diff !== 'loading'}
            onClick={() => void showDiff()}
          >
            {t('resources.edit.see_diff')}
          </button>
        </div>
      )}
      <Fail error={diffError} />
      {diff && diff !== 'loading' && (
        <div style={{ display: 'grid', gap: '10px' }}>
          {diff.files.length === 0 && <p class="faint t13">{t('resources.versions.diff_none')}</p>}
          {diff.files.map((f) => (
            <div key={f.path}>
              <div class="row" style={{ marginBottom: '6px' }}>
                <span
                  class={
                    f.change === 'removed' ? 'badge bad' : f.change === 'added' ? 'badge ok' : 'badge warn'
                  }
                >
                  {f.change === 'added'
                    ? t('resources.versions.added')
                    : f.change === 'removed'
                      ? t('resources.versions.removed')
                      : t('resources.versions.modified')}
                </span>
                <span class="mono t12">{f.path}</span>
              </div>
              {f.unified && <pre class="code">{f.unified}</pre>}
            </div>
          ))}
        </div>
      )}
      {versions.state === 'failed' && !versions.data ? (
        <div class="card">
          <LoadError error={versions.error} retry={versions.reload} />
        </div>
      ) : (
        <div class="list card" style={{ padding: '2px 14px' }} aria-busy={versions.state === 'loading'}>
          {[...(versions.data?.items ?? []), ...older.items].map((v) => {
            const live = v.number === r.version
            return (
              <div class="li" key={v.number}>
                <span
                  class={live ? 'badge mono ok' : 'badge mono'}
                  style={{ minWidth: '34px', justifyContent: 'center' }}
                >
                  v{v.number}
                </span>
                <div class="grow">
                  <div class="name" style={{ fontWeight: 500 }}>
                    {v.note ||
                      (v.rollback_of !== undefined
                        ? t('resources.versions.rollback_of', { version: fmtNumber(v.rollback_of) })
                        : t('resources.versions.no_note'))}
                  </div>
                  <div class="desc">
                    {v.published_by.display_name} ·{' '}
                    <span title={fmtDateTime(v.published_at)}>{fmtRelative(v.published_at)}</span>
                    {v.source_commit && <span class="mono"> · {v.source_commit.slice(0, 8)}</span>}
                  </div>
                </div>
                {live && r.status !== 'disabled' ? (
                  <span class="badge ok">
                    <span class="dot" />
                    {t('resources.versions.live')}
                  </span>
                ) : (
                  !live && (
                    <button type="button" class="btn sm" onClick={() => props.onRollback(v)}>
                      {t('resources.versions.rollback')}
                    </button>
                  )
                )}
              </div>
            )
          })}
          {versions.data && versions.data.items.length === 0 && (
            <div class="faint t13" style={{ padding: '10px 0' }}>
              {t('resources.versions.none')}
            </div>
          )}
        </div>
      )}
      <Fail error={olderError} />
      {next && (
        <div class="row">
          <button type="button" class="btn sm" disabled={older.busy} onClick={() => void loadOlder()}>
            {t('ui.load.more')}
          </button>
        </div>
      )}
      <p class="hint">{t('resources.versions.hint', { code: r.code })}</p>
    </>
  )
}

function NarrowDialog(props: {
  impact: Schemas['Impact']
  scope: string
  onClose: () => void
  onConfirm: () => void
}) {
  return (
    <Modal
      title={t('resources.narrow.title')}
      onClose={props.onClose}
      foot={
        <>
          <button type="button" class="btn ghost" onClick={props.onClose}>
            {t('ui.cancel')}
          </button>
          <button type="button" class="btn primary" onClick={props.onConfirm}>
            {t('resources.narrow.confirm')}
          </button>
        </>
      }
    >
      <p class="t13">
        {t('resources.narrow.body', {
          scope: props.scope,
          people: fmtNumber(props.impact.people),
          devices: fmtNumber(props.impact.devices),
        })}
      </p>
    </Modal>
  )
}

function DisableDialog(props: {
  r: Resource
  onClose: () => void
  onDone: (r: Resource) => void
  onStale: () => Promise<void>
}) {
  const r = props.r
  const impact = useLoad(
    async () =>
      unwrap(await api.GET('/api/v1/resources/{code}/impact', { params: { path: { code: r.code } } })),
    [r.code],
  )
  const [reason, setReason] = useState('')
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const submit = async () => {
    setBusy(true)
    setError(null)
    const out = await api
      .POST('/api/v1/resources/{code}/disable', {
        params: { path: { code: r.code }, header: { 'If-Match': r.etag } },
        body: { reason: reason.trim() },
      })
      .then(unwrap)
      .catch((err: unknown) => err)
    setBusy(false)
    if (out instanceof Error) {
      setError(out)
      if (isStale(out)) await props.onStale()
      return
    }
    props.onDone(out as Resource)
  }
  const i = impact.data
  return (
    <Modal
      title={t('resources.disable.title', { name: r.name })}
      onClose={props.onClose}
      foot={
        <>
          <button type="button" class="btn ghost" onClick={props.onClose}>
            {t('ui.cancel')}
          </button>
          <button
            type="button"
            class="btn primary"
            disabled={busy || !reason.trim()}
            onClick={() => void submit()}
          >
            {t('resources.disable.submit')}
          </button>
        </>
      }
    >
      {impact.state === 'failed' ? (
        <Fail error={impact.error} />
      ) : !i ? (
        <div aria-busy="true" style={{ minHeight: '40px' }} />
      ) : (
        <div>
          <p class="t13">
            {t('resources.disable.impact', { people: fmtNumber(i.people), devices: fmtNumber(i.devices) })}
          </p>
          {i.referenced_by.length > 0 && (
            <>
              <p class="t13" style={{ marginTop: '8px' }}>
                {t('resources.disable.referenced')}
              </p>
              <ul class="t13 dim" style={{ margin: '4px 0 0', paddingLeft: '18px' }}>
                {i.referenced_by.map((x) => (
                  <li key={x.code}>
                    {x.name} <span class="mono faint">{x.code}</span> · {kindName(x.kind)}
                  </li>
                ))}
              </ul>
            </>
          )}
        </div>
      )}
      <div>
        <label class="fl" for="d-dis">
          {t('resources.disable.reason')} <span class="faint">{t('resources.disable.reason_note')}</span>
        </label>
        <textarea
          class="field"
          id="d-dis"
          rows={2}
          maxLength={500}
          value={reason}
          onInput={(e) => setReason(e.currentTarget.value)}
          placeholder={t('resources.disable.reason_ph')}
        />
        <p class="hint">{t('resources.disable.hint')}</p>
      </div>
      <Fail error={error} />
    </Modal>
  )
}

function RollbackDialog(props: {
  r: Resource
  version: Schemas['ResourceVersion']
  onClose: () => void
  onDone: (v: Schemas['ResourceVersion']) => Promise<void>
  onStale: () => Promise<void>
}) {
  const [note, setNote] = useState('')
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const v = props.version
  const submit = async () => {
    setBusy(true)
    setError(null)
    const out = await api
      .POST('/api/v1/resources/{code}/rollback', {
        params: { path: { code: props.r.code }, header: { 'If-Match': props.r.etag } },
        body: { to_version: v.number, note: note.trim() || undefined },
      })
      .then(unwrap)
      .catch((err: unknown) => err)
    setBusy(false)
    if (out instanceof Error) {
      setError(out)
      if (isStale(out)) await props.onStale()
      return
    }
    await props.onDone(out as Schemas['ResourceVersion'])
  }
  return (
    <Modal
      title={t('resources.rollback.title', { version: fmtNumber(v.number) })}
      onClose={props.onClose}
      foot={
        <>
          <button type="button" class="btn ghost" onClick={props.onClose}>
            {t('ui.cancel')}
          </button>
          <button type="button" class="btn primary" disabled={busy} onClick={() => void submit()}>
            {t('resources.rollback.submit')}
          </button>
        </>
      }
    >
      <p class="t13">
        {t('resources.rollback.body', {
          version: fmtNumber(v.number),
          next: fmtNumber((props.r.version ?? 0) + 1),
        })}
      </p>
      <div>
        <label class="fl" for="d-rb">
          {t('resources.rollback.note')} <span class="faint">{t('resources.rollback.note_hint')}</span>
        </label>
        <input
          class="field"
          id="d-rb"
          maxLength={500}
          value={note}
          onInput={(e) => setNote(e.currentTarget.value)}
          placeholder={t('resources.rollback.note_ph')}
        />
      </div>
      <Fail error={error} />
    </Modal>
  )
}

function WaiveDialog(props: {
  code: string
  finding: Schemas['Finding']
  onClose: () => void
  onDone: () => Promise<void>
}) {
  const [reason, setReason] = useState('')
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const f = props.finding
  const submit = async () => {
    setBusy(true)
    setError(null)
    const out = await api
      .POST('/api/v1/resources/{code}/waivers', {
        params: { path: { code: props.code } },
        body: { finding: f.id, reason: reason.trim() },
      })
      .then(unwrap)
      .catch((err: unknown) => err)
    setBusy(false)
    if (out instanceof Error) setError(out)
    else await props.onDone()
  }
  return (
    <Modal
      title={t('resources.waive.title')}
      onClose={props.onClose}
      foot={
        <>
          <button type="button" class="btn ghost" onClick={props.onClose}>
            {t('ui.cancel')}
          </button>
          <button
            type="button"
            class="btn primary"
            disabled={busy || !reason.trim()}
            onClick={() => void submit()}
          >
            {t('resources.waive.submit')}
          </button>
        </>
      }
    >
      <p class="t13">
        {f.message}{' '}
        {f.file && (
          <span class="mono faint">
            {f.file}
            {f.line !== undefined && `:${f.line}`}
          </span>
        )}
      </p>
      <div>
        <label class="fl" for="d-wv">
          {t('resources.waive.reason')}
        </label>
        <textarea
          class="field"
          id="d-wv"
          rows={2}
          maxLength={500}
          value={reason}
          onInput={(e) => setReason(e.currentTarget.value)}
          placeholder={t('resources.waive.reason_ph')}
        />
        <p class="hint">{t('resources.waive.hint')}</p>
      </div>
      <Fail error={error} />
    </Modal>
  )
}
