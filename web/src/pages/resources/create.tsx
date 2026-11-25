// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Creating a resource on the web (prototype kindPicker and the new-resource drawer). Skills, workflows and
// agents belong in a resource repository; uploading their directory as an archive is the fallback. MCP
// servers and knowledge pointers are one document, filled in here. Either way the result is a draft: the
// editing drawer opens on it, to check and publish.

import { useRef, useState } from 'preact/hooks'
import { ApiError, type Schemas } from '../../api/client'
import { t } from '../../i18n'
import { Icon } from '../../ui/icons'
import { KINDS, KindIcon, kindName } from '../../ui/kind'
import { Drawer } from '../../ui/overlay'
import { ErrorNote } from '../../ui/problem'
import { toast } from '../../ui/toast'
import { parseDefinition, putDocument, templateFor, tooBig, uploadBundle } from './content'
import { BasicFields, DeliveryFields, type Settings } from './fields'
import { DIRECTORY_KINDS, kindDescription } from './labels'

export function Create(props: {
  products: readonly Schemas['Product'][]
  stages: readonly Schemas['Stage'][]
  onClose: () => void
  onCreated: (code: string) => void
}) {
  const [kind, setKind] = useState<string | null>(null)
  if (!kind) return <KindPicker onPick={setKind} onClose={props.onClose} />
  return DIRECTORY_KINDS.includes(kind) ? (
    <NewUpload kind={kind} onBack={() => setKind(null)} onClose={props.onClose} onCreated={props.onCreated} />
  ) : (
    <NewDocument
      kind={kind}
      products={props.products}
      stages={props.stages}
      onBack={() => setKind(null)}
      onClose={props.onClose}
      onCreated={props.onCreated}
    />
  )
}

function KindPicker(props: { onPick: (kind: string) => void; onClose: () => void }) {
  return (
    <Drawer
      label={t('resources.new.title')}
      onClose={props.onClose}
      head={
        <div>
          <h2>{t('resources.new.title')}</h2>
          <p>{t('resources.new.lead')}</p>
        </div>
      }
    >
      <div class="callout info">
        <span>{t('resources.new.repo_first')}</span>
      </div>
      <div style={{ display: 'grid', gap: '6px' }}>
        {KINDS.map((k) => {
          const [what, how] = kindDescription(k)
          return (
            <button key={k} type="button" class="pick" onClick={() => props.onPick(k)}>
              <KindIcon kind={k} />
              <span class="grow">
                <span class="pk-t">
                  {kindName(k)}{' '}
                  <span class="faint t12" style={{ fontWeight: 400 }}>
                    · {what}
                  </span>
                </span>
                <span class="pk-d">{how}</span>
              </span>
              <Icon name="chev" />
            </button>
          )
        })}
      </div>
    </Drawer>
  )
}

function Head(props: { kind: string; onBack: () => void }) {
  return (
    <div class="row">
      <KindIcon kind={props.kind} />
      <div style={{ minWidth: 0 }}>
        <h2>{t('resources.new.kind_title', { kind: kindName(props.kind) })}</h2>
        <p>
          {kindDescription(props.kind)[0]} ·{' '}
          <button type="button" class="link" onClick={props.onBack}>
            {t('resources.new.other_kind')}
          </button>
        </p>
      </div>
    </div>
  )
}

const codeOk = (code: string) =>
  /^[a-z0-9]+(-[a-z0-9]+)*$/.test(code) && code.length >= 2 && code.length <= 48

function NewUpload(props: {
  kind: string
  onBack: () => void
  onClose: () => void
  onCreated: (code: string) => void
}) {
  const [code, setCode] = useState('')
  const [file, setFile] = useState<File | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const input = useRef<HTMLInputElement>(null)
  const submit = async () => {
    if (!file) return
    if (tooBig(file)) {
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
    const out = await uploadBundle(code, file, { kind: props.kind }).catch((err: unknown) => err)
    setBusy(false)
    if (out instanceof Error) {
      setError(out)
      return
    }
    toast(t('resources.new.created'))
    props.onCreated(code)
  }
  return (
    <Drawer
      label={t('resources.new.kind_title', { kind: kindName(props.kind) })}
      onClose={props.onClose}
      head={<Head kind={props.kind} onBack={props.onBack} />}
      foot={
        <>
          <button type="button" class="btn ghost" onClick={props.onClose}>
            {t('ui.cancel')}
          </button>
          <button
            type="button"
            class="btn primary"
            disabled={busy || !file || !codeOk(code)}
            onClick={() => void submit()}
          >
            {busy ? t('resources.upload.sending') : t('resources.new.upload_submit')}
          </button>
        </>
      }
    >
      <div class="callout info">
        <span>{t('resources.new.repo_first')}</span>
      </div>
      <div>
        <label class="fl" for="n-c">
          {t('resources.edit.code')}
        </label>
        <input
          class="field mono"
          id="n-c"
          autofocus
          maxLength={48}
          value={code}
          aria-invalid={code !== '' && !codeOk(code)}
          placeholder={t('resources.edit.code_ph')}
          onInput={(e) => setCode(e.currentTarget.value.trim().toLowerCase())}
        />
        <p class="hint">
          {code !== '' && !codeOk(code) ? t('resources.edit.code_bad') : t('resources.new.code_hint')}
        </p>
      </div>
      <div>
        <div class="fl">{t('resources.new.archive')}</div>
        <input
          ref={input}
          type="file"
          hidden
          accept=".zip,.tar,.tgz,.tar.gz,application/zip,application/x-tar,application/gzip"
          onChange={(e) => setFile(e.currentTarget.files?.[0] ?? null)}
        />
        <button type="button" class="pick" onClick={() => input.current?.click()}>
          <Icon name="folder" />
          <span class="grow">
            <span class="pk-t">{file ? file.name : t('resources.new.choose')}</span>
            <span class="pk-d">{t('resources.upload.desc')}</span>
          </span>
        </button>
      </div>
      <p class="hint" style={{ marginTop: '-8px' }}>
        {t('resources.new.upload_hint')}
      </p>
      <ErrorNote error={error} />
    </Drawer>
  )
}

function NewDocument(props: {
  kind: string
  products: readonly Schemas['Product'][]
  stages: readonly Schemas['Stage'][]
  onBack: () => void
  onClose: () => void
  onCreated: (code: string) => void
}) {
  const [code, setCode] = useState('')
  const [form, setForm] = useState<Settings>({
    name: '',
    summary: '',
    audience: 'rd',
    stages: [],
    scope: 'org',
    policy: 'recommended',
    when: '',
    delivery: props.kind === 'mcp' ? 'native' : undefined,
  })
  const [text, setText] = useState(() => JSON.stringify(templateFor(props.kind, ''), null, 2))
  const [touched, setTouched] = useState(false)
  const [syntax, setSyntax] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)

  const onCode = (next: string) => {
    setCode(next)
    // Until people edit the document, its address follows the code.
    if (!touched) setText(JSON.stringify(templateFor(props.kind, next), null, 2))
  }

  const submit = async () => {
    const def = parseDefinition(text)
    if (def instanceof SyntaxError) {
      setSyntax(true)
      return
    }
    if (props.kind === 'mcp') def.delivery = form.delivery
    setBusy(true)
    setError(null)
    const out = await putDocument(code, {
      kind: props.kind,
      meta: {
        name: form.name.trim(),
        summary: form.summary.trim() || undefined,
        stages: form.stages,
        audience: form.audience,
        scope: form.scope,
        policy: form.policy,
        when: form.when.trim() || undefined,
      },
      definition: def,
    }).catch((err: unknown) => err)
    setBusy(false)
    if (out instanceof Error) {
      setError(out)
      return
    }
    toast(t('resources.new.created'))
    props.onCreated(code)
  }

  return (
    <Drawer
      label={t('resources.new.kind_title', { kind: kindName(props.kind) })}
      onClose={props.onClose}
      head={<Head kind={props.kind} onBack={props.onBack} />}
      foot={
        <>
          <button type="button" class="btn ghost" onClick={props.onClose}>
            {t('ui.cancel')}
          </button>
          <button
            type="button"
            class="btn primary"
            disabled={busy || !codeOk(code) || !form.name.trim()}
            onClick={() => void submit()}
          >
            {t('resources.new.document_submit')}
          </button>
        </>
      }
    >
      <BasicFields value={form} onChange={setForm} code={code} onCode={onCode} kind={props.kind} />
      <div>
        <label class="fl" for="n-json">
          {props.kind === 'mcp' ? t('resources.document.mcp') : t('resources.document.label')}{' '}
          <span class="faint">{t('resources.document.note')}</span>
        </label>
        <textarea
          class="field mono"
          id="n-json"
          rows={9}
          spellcheck={false}
          value={text}
          aria-invalid={syntax}
          onInput={(e) => {
            setText(e.currentTarget.value)
            setTouched(true)
            setSyntax(false)
          }}
        />
        <p class="hint" role={syntax ? 'alert' : undefined}>
          {syntax ? t('resources.document.syntax') : t('resources.document.hint')}
        </p>
      </div>
      <DeliveryFields
        value={form}
        onChange={setForm}
        kind={props.kind}
        stages={props.stages}
        products={props.products}
      />
      <ErrorNote error={error} />
    </Drawer>
  )
}
