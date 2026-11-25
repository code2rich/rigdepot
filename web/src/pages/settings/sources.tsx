// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The resource repositories (/api/v1/sources), in priority order: when two have a resource with the same
// code, the earlier one wins. Each shows how its last pull went; "立即拉取" queues a pull and the card
// follows it until it finishes. The read-only token is write-only: the page only knows whether one is set.

import { useEffect, useRef, useState } from 'preact/hooks'
import { api, unwrap, type Schemas } from '../../api/client'
import { useLoad } from '../../api/use-load'
import { t } from '../../i18n'
import { fmtDateTime, fmtNumber, fmtRelative } from '../../i18n/format'
import { Icon } from '../../ui/icons'
import { Modal } from '../../ui/overlay'
import { ErrorNote, LoadFailed } from '../../ui/problem'
import { toast } from '../../ui/toast'
import './settings.css'

type Source = Schemas['Source']

export function SourcesCard() {
  const [sources, reload, setSources] = useLoad(
    async () => unwrap(await api.GET('/api/v1/sources')).items,
    [],
  )
  const [editing, setEditing] = useState<Source | 'new' | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const list = sources.data ?? []

  // While a pull is running, look again every few seconds.
  const running = list.some((s) => s.last_pull?.result === 'running')
  const tries = useRef(0)
  useEffect(() => {
    if (!running) {
      tries.current = 0
      return
    }
    if (tries.current++ > 30) return
    const timer = setTimeout(() => void reload(), 2000)
    return () => clearTimeout(timer)
  }, [running, sources.data])

  const pull = async (s: Source) => {
    setBusy(true)
    setError(null)
    const out = await api
      .POST('/api/v1/sources/{source_id}/pull', { params: { path: { source_id: s.id } } })
      .then(unwrap)
      .catch((err: unknown) => err)
    setBusy(false)
    if (out instanceof Error) {
      setError(out)
      return
    }
    setSources(list.map((x) => (x.id === s.id ? (out as Source) : x)))
    toast(t('settings.sources.pulling', { name: s.name }))
  }

  const move = async (i: number, by: number) => {
    const ids = list.map((s) => s.id)
    const [id] = ids.splice(i, 1)
    if (!id) return
    ids.splice(i + by, 0, id)
    setBusy(true)
    setError(null)
    const out = await api
      .PUT('/api/v1/sources/order', { body: { ids } })
      .then(unwrap)
      .catch((err: unknown) => err)
    setBusy(false)
    if (out instanceof Error) setError(out)
    else setSources((out as Schemas['SourceList']).items)
  }

  return (
    <section class="card pad">
      <h3>
        <span>
          {t('settings.sources.title')} {sources.data && <span class="tag">{fmtNumber(list.length)}</span>}
        </span>
      </h3>
      {sources.state === 'failed' && !sources.data ? (
        <LoadFailed error={sources.error} retry={reload} />
      ) : !sources.data ? (
        <div aria-busy="true" style={{ minHeight: '80px' }} />
      ) : list.length === 0 ? (
        <div class="callout plain">
          <span>{t('settings.sources.none')}</span>
        </div>
      ) : (
        <div class="list">
          {list.map((s, i) => (
            <div key={s.id} class="li st-src" style={{ alignItems: 'flex-start' }}>
              <span class={`sq ${pullClass(s)}`} style={{ marginTop: '6px' }} />
              <div class="grow">
                <div class="name">{s.name}</div>
                <div class="desc mono" style={{ whiteSpace: 'normal', overflowWrap: 'anywhere' }}>
                  {displayURL(s.url)} · {s.branch}
                  {s.dir && ` · ${s.dir}`}
                </div>
                <div class="desc" style={{ whiteSpace: 'normal' }}>
                  {pullText(s)}
                </div>
              </div>
              <div class="row st-acts" style={{ gap: '2px' }}>
                <PullBadge s={s} />
                {list.length > 1 && (
                  <>
                    <button
                      type="button"
                      class="btn ghost icon sm"
                      disabled={busy || i === 0}
                      onClick={() => void move(i, -1)}
                      aria-label={t('settings.sources.up', { name: s.name })}
                      title={t('settings.sources.up', { name: s.name })}
                    >
                      <span class="st-up">
                        <Icon name="arrow" />
                      </span>
                    </button>
                    <button
                      type="button"
                      class="btn ghost icon sm"
                      disabled={busy || i === list.length - 1}
                      onClick={() => void move(i, 1)}
                      aria-label={t('settings.sources.down', { name: s.name })}
                      title={t('settings.sources.down', { name: s.name })}
                    >
                      <span class="st-down">
                        <Icon name="arrow" />
                      </span>
                    </button>
                  </>
                )}
                <button
                  type="button"
                  class="btn ghost icon sm"
                  disabled={busy || s.last_pull?.result === 'running'}
                  onClick={() => void pull(s)}
                  aria-label={t('settings.sources.pull', { name: s.name })}
                  title={t('settings.sources.pull', { name: s.name })}
                >
                  <Icon name="reset" />
                </button>
                <button
                  type="button"
                  class="btn ghost icon sm"
                  onClick={() => setEditing(s)}
                  aria-label={t('settings.sources.edit', { name: s.name })}
                  title={t('settings.sources.edit', { name: s.name })}
                >
                  <Icon name="edit" />
                </button>
              </div>
            </div>
          ))}
        </div>
      )}
      <ErrorNote error={error} />
      {sources.data && (
        <button type="button" class="btn" style={{ marginTop: '10px' }} onClick={() => setEditing('new')}>
          <Icon name="plus" />
          {t('settings.sources.add')}
        </button>
      )}
      <p class="hint">{t('settings.sources.hint')}</p>
      {editing && (
        <SourceDialog
          source={editing === 'new' ? undefined : editing}
          onClose={() => setEditing(null)}
          onDone={(text) => {
            setEditing(null)
            void reload()
            toast(text)
          }}
        />
      )}
    </section>
  )
}

/** displayURL leaves out the scheme and .git, as the prototype shows a repository. */
function displayURL(url: string): string {
  return url.replace(/^https:\/\//, '').replace(/\.git$/, '')
}

function pullClass(s: Source): string {
  switch (s.last_pull?.result) {
    case 'ok':
      return 'ok'
    case 'failed':
      return 'bad'
    case 'running':
      return 'warn'
    default:
      return 'off'
  }
}

function PullBadge({ s }: { s: Source }) {
  switch (s.last_pull?.result) {
    case 'ok':
      return <span class="badge ok">{t('settings.sources.ok')}</span>
    case 'failed':
      return <span class="badge bad">{t('settings.sources.failed')}</span>
    case 'running':
      return (
        <span class="badge warn">
          <span class="dot pulse" />
          {t('settings.sources.running')}
        </span>
      )
    default:
      return <span class="badge">{t('settings.sources.never')}</span>
  }
}

function pullText(s: Source): string {
  const p = s.last_pull
  if (!p) return t('settings.sources.never_desc')
  const when = fmtRelative(p.at)
  if (p.result === 'running') return t('settings.sources.running_desc', { when })
  if (p.result === 'failed') {
    const why = p.error ?? ''
    return p.last_success_at
      ? t('settings.sources.failed_desc', { when, why, last: fmtRelative(p.last_success_at) })
      : t('settings.sources.failed_never', { when, why })
  }
  return t('settings.sources.ok_desc', {
    when,
    count: s.resource_count,
    resources: fmtNumber(s.resource_count),
    imported: fmtNumber(p.imported ?? 0),
  })
}

function SourceDialog(props: { source?: Source; onClose: () => void; onDone: (toast: string) => void }) {
  const s = props.source
  const [name, setName] = useState(s?.name ?? '')
  const [url, setURL] = useState(s?.url ?? '')
  const [branch, setBranch] = useState(s?.branch ?? 'main')
  const [dir, setDir] = useState(s?.dir ?? '')
  const [credential, setCredential] = useState('')
  const [clear, setClear] = useState(false)
  const [removing, setRemoving] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const urlOk = /^(https:\/\/|ssh:\/\/|[\w.-]+@[\w.-]+:)/.test(url.trim())
  const ok = name.trim() && urlOk && branch.trim()

  const submit = async (e: Event) => {
    e.preventDefault()
    if (!ok) return
    setBusy(true)
    setError(null)
    const fields = { name: name.trim(), url: url.trim(), branch: branch.trim(), dir: dir.trim() }
    const out = s
      ? await api
          .PATCH('/api/v1/sources/{source_id}', {
            params: { path: { source_id: s.id }, header: { 'If-Match': s.etag } },
            body: {
              ...fields,
              ...(credential ? { credential } : {}),
              ...(clear ? { clear_credential: true } : {}),
            },
          })
          .then(unwrap)
          .catch((err: unknown) => err)
      : await api
          .POST('/api/v1/sources', { body: { ...fields, ...(credential ? { credential } : {}) } })
          .then(unwrap)
          .catch((err: unknown) => err)
    setBusy(false)
    setCredential('')
    if (out instanceof Error) {
      setError(out)
      return
    }
    props.onDone(s ? t('settings.sources.saved') : t('settings.sources.added', { name: fields.name }))
  }

  const remove = async () => {
    if (!s) return
    setBusy(true)
    setError(null)
    const out = await api
      .DELETE('/api/v1/sources/{source_id}', { params: { path: { source_id: s.id } } })
      .then(unwrap)
      .catch((err: unknown) => err)
    setBusy(false)
    if (out instanceof Error) {
      setError(out)
      return
    }
    props.onDone(t('settings.sources.removed', { name: s.name }))
  }

  return (
    <Modal
      wide
      title={s ? t('settings.sources.edit_title', { name: s.name }) : t('settings.sources.add_title')}
      onClose={props.onClose}
      foot={
        removing ? (
          <>
            <span class="t13 grow">{t('settings.sources.remove_ask')}</span>
            <button type="button" class="btn ghost" onClick={() => setRemoving(false)}>
              {t('ui.cancel')}
            </button>
            <button type="button" class="btn primary" disabled={busy} onClick={() => void remove()}>
              {t('settings.sources.remove')}
            </button>
          </>
        ) : (
          <>
            {s && (
              <button
                type="button"
                class="btn ghost danger"
                style={{ marginRight: 'auto' }}
                onClick={() => setRemoving(true)}
              >
                <Icon name="trash" />
                {t('settings.sources.remove')}
              </button>
            )}
            <button type="button" class="btn ghost" onClick={props.onClose}>
              {t('ui.cancel')}
            </button>
            <button type="submit" form="d-src" class="btn primary" disabled={busy || !ok}>
              {s ? t('ui.save') : t('settings.sources.add_submit')}
            </button>
          </>
        )
      }
    >
      <form id="d-src" onSubmit={submit} style={{ display: 'contents' }}>
        <div>
          <label class="fl" for="d-sn">
            {t('settings.sources.name')}
          </label>
          <input
            class="field"
            id="d-sn"
            autofocus
            maxLength={64}
            value={name}
            onInput={(e) => setName(e.currentTarget.value)}
            placeholder="harness-resources"
          />
        </div>
        <div>
          <label class="fl" for="d-su">
            {t('settings.sources.url')}
          </label>
          <input
            class="field mono"
            id="d-su"
            maxLength={1024}
            value={url}
            aria-invalid={url !== '' && !urlOk}
            onInput={(e) => setURL(e.currentTarget.value)}
            placeholder="https://gitlab.example.com/platform/harness-resources.git"
          />
          <p class="hint">
            {url !== '' && !urlOk ? t('settings.sources.url_bad') : t('settings.sources.url_hint')}
          </p>
        </div>
        <div class="grid g2" style={{ gap: '10px' }}>
          <div>
            <label class="fl" for="d-sb">
              {t('settings.sources.branch')}
            </label>
            <input
              class="field mono"
              id="d-sb"
              maxLength={255}
              value={branch}
              onInput={(e) => setBranch(e.currentTarget.value)}
            />
          </div>
          <div>
            <label class="fl" for="d-sd">
              {t('settings.sources.dir')} <span class="faint">{t('settings.sources.dir_note')}</span>
            </label>
            <input
              class="field mono"
              id="d-sd"
              maxLength={255}
              value={dir}
              onInput={(e) => setDir(e.currentTarget.value)}
            />
          </div>
        </div>
        <div>
          <label class="fl" for="d-sk">
            {t('settings.sources.token')}
          </label>
          <input
            class="field mono"
            id="d-sk"
            type="password"
            autocomplete="off"
            maxLength={8192}
            value={credential}
            disabled={clear}
            onInput={(e) => setCredential(e.currentTarget.value)}
            placeholder={s?.has_credential ? t('settings.sources.token_set') : t('settings.sources.token_ph')}
          />
          <p class="hint">{t('settings.sources.token_hint')}</p>
          {s?.has_credential && (
            <label class="check" style={{ marginTop: '6px' }}>
              <input type="checkbox" checked={clear} onChange={(e) => setClear(e.currentTarget.checked)} />
              {t('settings.sources.token_clear')}
            </label>
          )}
        </div>
        {s?.last_pull?.error && (
          <div class="callout bad">
            <span>
              {t('settings.sources.last_error', {
                when: fmtDateTime(s.last_pull.at),
                why: s.last_pull.error,
              })}
            </span>
          </div>
        )}
        <ErrorNote error={error} />
      </form>
    </Modal>
  )
}
