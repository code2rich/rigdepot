// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The 提反馈 dialog, opened by the button in the corner of every page and by "不好用，说说哪里有问题" in a
// resource's details. It submits to this deployment's own feedback queue (POST /api/v1/feedback), where
// the resource's product owners or the platform administrators pick it up. The context it adds is the page
// and the browser, nothing else (docs/subsystems/feedback-friction.md). What was typed survives closing the
// dialog by accident; it is cleared once submitted.

import { signal } from '@preact/signals'
import { useEffect, useState } from 'preact/hooks'
import { useLocation } from 'preact-iso'
import { ApiError, api, unwrap, type Schemas } from '../api/client'
import { t } from '../i18n'
import { href, stripBase } from '../router/paths'
import { errorText } from '../ui/load'
import { Modal } from '../ui/overlay'

type Draft = { type: 'bug' | 'suggestion' | 'question'; resource: string; title: string; body: string }

const empty = (resource = ''): Draft => ({ type: 'bug', resource, title: '', body: '' })

const open = signal<'form' | 'done' | null>(null)
const draft = signal<Draft>(empty())
const submitted = signal<number | null>(null)

/** openFeedback opens the dialog, about a resource when its code is given. */
export function openFeedback(resource?: string) {
  if (resource !== undefined) draft.value = { ...empty(resource), type: 'bug' }
  open.value = 'form'
}

/** browserName is a short description of the browser for the context ("Chrome 140 · macOS"). */
function browserName(): string {
  const ua = navigator.userAgent
  const name =
    /Edg\/(\d+)/.exec(ua)?.[0] ??
    /Firefox\/(\d+)/.exec(ua)?.[0] ??
    /Chrome\/(\d+)/.exec(ua)?.[0] ??
    (/Version\/(\d+).*Safari/.test(ua) ? 'Safari' : 'Browser')
  const os = /Mac OS X/.test(ua) ? 'macOS' : /Windows/.test(ua) ? 'Windows' : /Linux/.test(ua) ? 'Linux' : ''
  return (os ? `${name.replace('/', ' ')} · ${os}` : name.replace('/', ' ')).slice(0, 100)
}

const TYPES: [Draft['type'], 'feedback.type.bug' | 'feedback.type.suggestion' | 'feedback.type.question'][] =
  [
    ['bug', 'feedback.type.bug'],
    ['suggestion', 'feedback.type.suggestion'],
    ['question', 'feedback.type.question'],
  ]

export function FeedbackDialog() {
  if (open.value === 'form') return <Form />
  if (open.value === 'done') return <Done />
  return null
}

function Form() {
  const { path } = useLocation()
  const [catalog, setCatalog] = useState<Schemas['CatalogItem'][]>([])
  const [sending, setSending] = useState(false)
  const [message, setMessage] = useState('')
  const d = draft.value
  const set = (patch: Partial<Draft>) => {
    draft.value = { ...draft.value, ...patch }
  }

  useEffect(() => {
    api.GET('/api/v1/catalog').then(
      (res) => setCatalog(res.data?.items ?? []),
      () => setCatalog([]),
    )
  }, [])

  const close = () => {
    open.value = null
  }

  const submit = async (e?: Event) => {
    e?.preventDefault()
    if (sending) return
    const title = d.title.trim()
    const body = d.body.trim()
    if (!title || !body) {
      setMessage(t('feedback.form.required'))
      return
    }
    setSending(true)
    setMessage('')
    const item = catalog.find((r) => r.code === d.resource)
    try {
      const created = unwrap(
        await api.POST('/api/v1/feedback', {
          body: {
            type: d.type,
            title,
            body,
            ...(item ? { resource: item.code, resource_version: item.version } : {}),
            context: { page: stripBase(path).slice(0, 200), browser: browserName() },
          },
        }),
      )
      submitted.value = created.number
      draft.value = empty()
      open.value = 'done'
    } catch (err) {
      setMessage(
        err instanceof ApiError && err.status === 429 ? t('feedback.form.too_often') : errorText(err),
      )
    } finally {
      setSending(false)
    }
  }

  const resources = d.resource && !catalog.some((r) => r.code === d.resource) ? [d.resource] : []

  return (
    <Modal
      title={t('shell.feedback')}
      onClose={close}
      foot={
        <>
          <button type="button" class="btn ghost" onClick={close}>
            {t('ui.cancel')}
          </button>
          <button
            type="submit"
            form="fb-form"
            class="btn primary"
            disabled={sending}
            aria-busy={sending ? 'true' : undefined}
          >
            {sending ? t('feedback.form.sending') : t('feedback.form.submit')}
          </button>
        </>
      }
    >
      <form id="fb-form" onSubmit={submit} style={{ display: 'contents' }} noValidate>
        {message && (
          <div class="callout bad" role="alert" style={{ display: 'block' }}>
            {message}
          </div>
        )}
        <div class="callout info" style={{ display: 'block' }}>
          {t('feedback.form.where')}
        </div>
        <div class="grid g2" style={{ gap: '10px' }}>
          <div>
            <label class="fl" for="d-fk">
              {t('feedback.form.type')}
            </label>
            <select
              class="field"
              id="d-fk"
              value={d.type}
              onChange={(e) => set({ type: e.currentTarget.value as Draft['type'] })}
            >
              {TYPES.map(([v, k]) => (
                <option key={v} value={v}>
                  {t(k)}
                </option>
              ))}
            </select>
          </div>
          <div>
            <label class="fl" for="d-fr">
              {t('feedback.form.resource')}
            </label>
            <select
              class="field"
              id="d-fr"
              value={d.resource}
              onChange={(e) => set({ resource: e.currentTarget.value })}
            >
              <option value="">{t('feedback.form.no_resource')}</option>
              {resources.map((code) => (
                <option key={code} value={code}>
                  {code}
                </option>
              ))}
              {catalog.map((r) => (
                <option key={r.code} value={r.code}>
                  {r.name}
                </option>
              ))}
            </select>
          </div>
        </div>
        <div>
          <label class="fl" for="d-fti">
            {t('feedback.form.title')} <span class="faint">{t('feedback.form.title.note')}</span>
          </label>
          <input
            class="field"
            id="d-fti"
            maxLength={80}
            value={d.title}
            onInput={(e) => set({ title: e.currentTarget.value })}
            placeholder={t('feedback.form.title.placeholder')}
          />
        </div>
        <div>
          <label class="fl" for="d-ft">
            {t('feedback.form.body')} <span class="faint">{t('feedback.form.body.note')}</span>
          </label>
          <textarea
            class="field"
            id="d-ft"
            rows={4}
            maxLength={5000}
            value={d.body}
            onInput={(e) => set({ body: e.currentTarget.value })}
            placeholder={t('feedback.form.body.placeholder')}
          />
        </div>
        <p class="hint" style={{ margin: 0 }}>
          {t('feedback.form.context')}
        </p>
      </form>
    </Modal>
  )
}

function Done() {
  const { route } = useLocation()
  const n = submitted.value
  const close = () => {
    open.value = null
  }
  return (
    <Modal
      title={t('feedback.done.title')}
      onClose={close}
      foot={
        <>
          <button type="button" class="btn ghost" onClick={close}>
            {t('ui.close')}
          </button>
          <button
            type="button"
            class="btn primary"
            onClick={() => {
              close()
              if (n !== null) route(href(`/feedback/${n}`))
            }}
          >
            {t('feedback.done.view')}
          </button>
        </>
      }
    >
      <div class="pl-code" style={{ margin: 0, justifyContent: 'space-between' }}>
        <span class="tag">{t('feedback.done.number')}</span>
        <b>{n !== null ? t('feedback.number', { number: n }) : ''}</b>
      </div>
      <p class="t13">{t('feedback.done.text')}</p>
    </Modal>
  )
}
