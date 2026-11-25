// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The five steps of the first-run wizard. Each is a <form id="wz-form"> the wizard's Next button submits:
// it saves what was filled in and calls done(), or shows why it cannot. Secrets (the setup token, client
// secrets, the repository token, passwords) stay in their password fields, are read when sent and cleared
// afterwards; they never go into component state.

import { useEffect, useRef, useState } from 'preact/hooks'
import { api, unwrap, type Schemas } from '../../api/client'
import { BRAND } from '../../brand.gen'
import { chooseLocale, setLocale, t, type MessageKey } from '../../i18n'
import { href } from '../../router/paths'
import { me } from '../../shell/session'
import { Empty } from '../../ui/empty'
import { Fail, isCode } from '../../ui/fail'
import { Field, FieldShell } from '../../ui/field'
import { Icon } from '../../ui/icons'
import { rich } from '../../ui/rich'
import { MIN_PASSWORD, USERNAME } from '../invite/rules'
import { configFields, repoName, type ConfigField } from './config'

export interface StepProps {
  status: Schemas['SetupStatus']
  setBusy: (busy: boolean) => void
  /** done moves on to the next step (after the last one: into the console). */
  done: () => void
  /** created is called when the first administrator exists and is signed in. */
  created: (status: Schemas['SetupStatus']) => void
}

/** run wraps what a step's Next does: busy while it runs, the failure kept for the step to show. */
function useRun(props: StepProps): [unknown, (work: () => Promise<void>) => Promise<void>] {
  const [error, setError] = useState<unknown>(null)
  const run = async (work: () => Promise<void>) => {
    props.setBusy(true)
    setError(null)
    try {
      await work()
    } catch (err) {
      setError(err)
    } finally {
      props.setBusy(false)
    }
  }
  return [error, run]
}

function clear(...fields: (HTMLInputElement | null)[]) {
  for (const f of fields) if (f) f.value = ''
}

// ------------------------------------------------------------------ 1 the first administrator

export function AdminStep(props: StepProps) {
  const person = me.value
  const [username, setUsername] = useState('admin')
  const [errors, setErrors] = useState<{ token?: string; username?: string; password?: string }>({})
  const [error, run] = useRun(props)
  const token = useRef<HTMLInputElement>(null)
  const password = useRef<HTMLInputElement>(null)

  if (props.status.admin_created) {
    if (person?.platform_admin) {
      return (
        <form
          id="wz-form"
          onSubmit={(e) => {
            e.preventDefault()
            props.done()
          }}
        >
          <div class="callout ok" role="status">
            <Icon name="check" />
            <span class="grow">
              {t('setup.admin.done', { name: person.display_name, username: person.username })}
            </span>
          </div>
        </form>
      )
    }
    return (
      <form
        id="wz-form"
        onSubmit={(e) => {
          e.preventDefault()
          location.assign(`${href('/login')}?next=${encodeURIComponent('/setup')}`)
        }}
      >
        <div class="callout info" role="status">
          <span class="grow">{t('setup.admin.exists')}</span>
        </div>
      </form>
    )
  }

  const submit = (e: Event) => {
    e.preventDefault()
    const tok = token.current?.value.trim() ?? ''
    const secret = password.current?.value ?? ''
    const next: typeof errors = {}
    if (!tok) next.token = t('setup.admin.error.token_empty')
    if (!USERNAME.test(username)) next.username = t('setup.admin.error.username')
    if (secret.length < MIN_PASSWORD) next.password = t('setup.admin.error.password', { min: MIN_PASSWORD })
    setErrors(next)
    if (Object.keys(next).length) return
    void run(async () => {
      try {
        const admin = unwrap(
          await api.POST('/api/v1/setup/admin', {
            params: { header: { 'X-Setup-Token': tok } },
            body: { username, password: secret },
          }),
        )
        clear(token.current)
        me.value = admin
        await setLocale(chooseLocale(admin.locale))
        props.created({ ...props.status, admin_created: true })
      } catch (err) {
        if (isCode(err, 'setup.token_invalid')) setErrors({ token: t('setup.admin.error.token') })
        else if (isCode(err, 'setup.admin_exists')) props.created({ ...props.status, admin_created: true })
        else throw err
      } finally {
        clear(password.current)
      }
    })
  }

  return (
    <form id="wz-form" class="wz-form" onSubmit={submit} noValidate>
      <p class="dim t13">{t('setup.admin.lead')}</p>
      <Field
        id="w-init"
        label={t('setup.admin.token')}
        mono
        autocomplete="off"
        spellcheck={false}
        placeholder={t('setup.admin.token.placeholder')}
        inputRef={token}
        error={errors.token}
        hint={rich('setup.admin.token.hint', { cmd: <span class="mono">docker compose logs</span> })}
      />
      <Field
        id="w-u"
        label={t('setup.admin.username')}
        autocomplete="username"
        autocapitalize="off"
        spellcheck={false}
        maxLength={32}
        value={username}
        error={errors.username}
        onInput={(e) => setUsername(e.currentTarget.value.trim().toLowerCase())}
      />
      <Field
        id="w-p"
        label={t('setup.admin.password')}
        type="password"
        autocomplete="new-password"
        placeholder={t('setup.admin.password.placeholder', { min: MIN_PASSWORD })}
        inputRef={password}
        error={errors.password}
        hint={rich('setup.admin.password.hint', {
          cmd: <span class="mono">{`${BRAND.server} reset-admin`}</span>,
        })}
      />
      {error !== null && <Fail error={error} />}
    </form>
  )
}

// ------------------------------------------------------------------ 2 sign-in methods

const KIND_TEXT: Readonly<Record<string, readonly [MessageKey, MessageKey, MessageKey]>> = {
  oidc: ['setup.methods.oidc', 'setup.methods.oidc.desc', 'setup.methods.oidc.name'],
  gitlab: ['setup.methods.gitlab', 'setup.methods.gitlab.desc', 'setup.methods.gitlab.name'],
}

const FIELD_LABEL: Readonly<Record<string, MessageKey>> = {
  issuer: 'setup.methods.field.issuer',
  url: 'setup.methods.field.url',
  client_id: 'setup.methods.field.client_id',
}

const FIELD_EXAMPLE: Readonly<Record<string, string>> = {
  issuer: 'https://sso.example.com/realms/dev',
  url: 'https://gitlab.example.com',
}

type Methods = {
  kinds: Schemas['IdentityProviderKind'][]
  providers: Schemas['IdentityProvider'][]
}

export function MethodsStep(props: StepProps) {
  const [data, setData] = useState<Methods | null>(null)
  const [loadError, setLoadError] = useState<unknown>(null)
  const [on, setOn] = useState<Record<string, boolean>>({})
  const [config, setConfig] = useState<Record<string, Record<string, string>>>({})
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [none, setNone] = useState(false)
  const [error, run] = useRun(props)
  const secrets = useRef<Record<string, HTMLInputElement | null>>({})

  const load = () => {
    setLoadError(null)
    Promise.all([
      api.GET('/api/v1/identity-providers/kinds').then(unwrap),
      api.GET('/api/v1/identity-providers').then(unwrap),
    ]).then(([kinds, providers]) => {
      setData({ kinds: kinds.items, providers: providers.items })
      const start: Record<string, boolean> = { local: true }
      for (const p of providers.items) start[p.kind] = (start[p.kind] && p.kind !== 'local') || p.enabled
      setOn(start)
    }, setLoadError)
  }
  useEffect(load, [])

  if (loadError !== null || !data) {
    return (
      <form
        id="wz-form"
        class="wz-form"
        onSubmit={(e) => {
          e.preventDefault()
          props.done()
        }}
      >
        <p class="dim t13">{t('setup.methods.lead')}</p>
        {loadError !== null ? (
          <Fail error={loadError} retry={load}>
            <span class="faint t12">{t('setup.methods.later')}</span>
          </Fail>
        ) : (
          <p class="faint t13" aria-busy="true">
            {t('setup.loading')}
          </p>
        )}
      </form>
    )
  }

  const existing = (kind: string) => data.providers.filter((p) => p.kind === kind)
  const choices = [
    ...data.kinds.map((k) => ({ kind: k.kind, name: k.name, schema: k.config_schema })),
    { kind: 'local', name: '', schema: {} },
  ]
  const label = (kind: string, name: string) => {
    const known = KIND_TEXT[kind]
    if (known) return t(known[0])
    return kind === 'local' ? t('setup.methods.local') : name
  }
  const desc = (kind: string) => {
    const known = KIND_TEXT[kind]
    if (known) return t(known[1])
    return kind === 'local' ? t('setup.methods.local.desc') : ''
  }
  const fieldsOf = (schema: Record<string, unknown>): ConfigField[] => configFields(schema)

  const submit = (e: Event) => {
    e.preventDefault()
    const chosen = choices.filter((c) => on[c.kind])
    setNone(chosen.length === 0)
    if (chosen.length === 0) return
    const fresh = chosen.filter((c) => c.kind !== 'local' && existing(c.kind).length === 0)
    const next: Record<string, string> = {}
    for (const c of fresh) {
      for (const f of fieldsOf(c.schema)) {
        const v = (config[c.kind]?.[f.name] ?? '').trim()
        if (f.required && !v) next[`${c.kind}.${f.name}`] = t('setup.methods.error.required')
        else if (v && f.uri && !/^https?:\/\/\S+$/.test(v))
          next[`${c.kind}.${f.name}`] = t('setup.methods.error.uri')
      }
    }
    setErrors(next)
    if (Object.keys(next).length) return
    void run(async () => {
      try {
        // Add the new methods first, so turning one off never leaves nobody able to sign in.
        for (const c of fresh) {
          const known = KIND_TEXT[c.kind]
          const values = Object.fromEntries(
            Object.entries(config[c.kind] ?? {})
              .map(([k, v]) => [k, v.trim()])
              .filter(([, v]) => v),
          )
          const secret = secrets.current[c.kind]?.value ?? ''
          unwrap(
            await api.POST('/api/v1/identity-providers', {
              body: {
                kind: c.kind,
                name: known ? t(known[2]) : c.name,
                enabled: true,
                config: values,
                client_secret: secret || undefined,
              },
            }),
          )
        }
        for (const p of data.providers) {
          const want = !!on[p.kind]
          if (p.enabled === want) continue
          unwrap(
            await api.PATCH('/api/v1/identity-providers/{provider_id}', {
              params: { path: { provider_id: p.id }, header: { 'If-Match': p.etag } },
              body: { enabled: want },
            }),
          )
        }
        props.done()
      } finally {
        for (const el of Object.values(secrets.current)) clear(el)
      }
    })
  }

  return (
    <form id="wz-form" class="wz-form" onSubmit={submit} noValidate>
      <p class="dim t13">{t('setup.methods.lead')}</p>
      <div class="wz-picks" role="group" aria-label={t('setup.step.methods')}>
        {choices.map((c) => {
          const picked = !!on[c.kind]
          const have = existing(c.kind)
          return (
            <button
              type="button"
              key={c.kind}
              class={picked ? 'pick on' : 'pick'}
              aria-pressed={picked}
              onClick={() => setOn({ ...on, [c.kind]: !picked })}
            >
              <span class="grow">
                <span class="pk-t">{label(c.kind, c.name)}</span>
                <span class="pk-d">
                  {c.kind !== 'local' && have.length > 0
                    ? t('setup.methods.configured', { names: have.map((p) => p.name).join(', ') })
                    : desc(c.kind)}
                </span>
              </span>
              {picked && <Icon name="check" />}
            </button>
          )
        })}
      </div>
      {choices
        .filter((c) => c.kind !== 'local' && on[c.kind] && existing(c.kind).length === 0)
        .map((c) => (
          <fieldset class="wz-set" key={c.kind}>
            <legend class="sect-t">{label(c.kind, c.name)}</legend>
            {fieldsOf(c.schema).map((f) => {
              const fieldLabel = FIELD_LABEL[f.name]
              return (
                <Field
                  key={f.name}
                  id={`w-${c.kind}-${f.name}`}
                  label={fieldLabel ? t(fieldLabel) : f.name}
                  mono={f.uri}
                  autocomplete="off"
                  spellcheck={false}
                  placeholder={FIELD_EXAMPLE[f.name]}
                  value={config[c.kind]?.[f.name] ?? ''}
                  error={errors[`${c.kind}.${f.name}`]}
                  onInput={(e) =>
                    setConfig({ ...config, [c.kind]: { ...config[c.kind], [f.name]: e.currentTarget.value } })
                  }
                />
              )
            })}
            <Field
              id={`w-${c.kind}-secret`}
              label={t('setup.methods.secret')}
              type="password"
              autocomplete="off"
              placeholder={t('setup.methods.secret.placeholder')}
              inputRef={(el: HTMLInputElement | null) => {
                secrets.current[c.kind] = el
              }}
              hint={t('setup.methods.secret.hint')}
            />
          </fieldset>
        ))}
      {none && (
        <div class="callout bad" role="alert">
          {t('setup.methods.error.none')}
        </div>
      )}
      {error !== null && (
        <Fail error={error} known={{ 'identity.last_method': t('setup.methods.error.last') }} />
      )}
    </form>
  )
}

// ------------------------------------------------------------------ 3 the resource repository

export function RepoStep(props: StepProps) {
  const [sources, setSources] = useState<Schemas['Source'][] | null>(null)
  const [loadError, setLoadError] = useState<unknown>(null)
  const [url, setUrl] = useState('')
  const [branch, setBranch] = useState('main')
  const [errors, setErrors] = useState<{ url?: string; branch?: string }>({})
  const [error, run] = useRun(props)
  const credential = useRef<HTMLInputElement>(null)

  const load = () => {
    setLoadError(null)
    api
      .GET('/api/v1/sources')
      .then(unwrap)
      .then((list) => setSources(list.items), setLoadError)
  }
  useEffect(load, [])

  const submit = (e: Event) => {
    e.preventDefault()
    const address = url.trim()
    if (!address) {
      props.done()
      return
    }
    const next: typeof errors = {}
    if (!/^(https:\/\/|ssh:\/\/|[\w.-]+@[\w.-]+:)\S+$/.test(address)) next.url = t('setup.repo.error.url')
    if (!branch.trim()) next.branch = t('setup.repo.error.branch')
    setErrors(next)
    if (Object.keys(next).length) return
    void run(async () => {
      try {
        unwrap(
          await api.POST('/api/v1/sources', {
            body: {
              name: repoName(address),
              url: address,
              branch: branch.trim(),
              credential: credential.current?.value || undefined,
            },
          }),
        )
        props.done()
      } finally {
        clear(credential.current)
      }
    })
  }

  return (
    <form id="wz-form" class="wz-form" onSubmit={submit} noValidate>
      <p class="dim t13">{t('setup.repo.lead')}</p>
      {loadError !== null && <Fail error={loadError} retry={load} />}
      {sources && sources.length > 0 && (
        <div>
          <div class="sect-t">{t('setup.repo.existing')}</div>
          <div class="list card wz-list">
            {sources.map((s) => (
              <div class="li" key={s.id}>
                <div class="grow">
                  <div class="name">{s.name}</div>
                  <div class="desc mono">
                    {s.url} · {s.branch}
                  </div>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}
      <Field
        id="w-repo"
        label={sources && sources.length > 0 ? t('setup.repo.url.another') : t('setup.repo.url')}
        mono
        autocomplete="off"
        spellcheck={false}
        placeholder="https://gitlab.example.com/platform/ai-resources.git"
        value={url}
        error={errors.url}
        onInput={(e) => setUrl(e.currentTarget.value)}
      />
      <div class="grid g2" style={{ gap: '10px' }}>
        <Field
          id="w-br"
          label={t('setup.repo.branch')}
          mono
          autocomplete="off"
          spellcheck={false}
          value={branch}
          error={errors.branch}
          onInput={(e) => setBranch(e.currentTarget.value)}
        />
        <Field
          id="w-tk"
          label={t('setup.repo.token')}
          type="password"
          autocomplete="off"
          placeholder={t('setup.repo.token.placeholder')}
          inputRef={credential}
        />
      </div>
      <p class="hint">{t('setup.repo.hint')}</p>
      {error !== null && <Fail error={error} />}
    </form>
  )
}

// ------------------------------------------------------------------ 4 data and privacy

export function ModulesStep(props: StepProps) {
  const [modules, setModules] = useState<Schemas['Module'][] | null>(null)
  const [loadError, setLoadError] = useState<unknown>(null)
  const [on, setOn] = useState<Record<string, boolean>>({})
  const [error, run] = useRun(props)

  const load = () => {
    setLoadError(null)
    api
      .GET('/api/v1/modules')
      .then(unwrap)
      .then((list) => {
        setModules(list.items)
        setOn(Object.fromEntries(list.items.map((m) => [m.id, m.enabled])))
      }, setLoadError)
  }
  useEffect(load, [])

  const submit = (e: Event) => {
    e.preventDefault()
    if (!modules) {
      props.done()
      return
    }
    void run(async () => {
      for (const m of modules) {
        const want = !!on[m.id]
        if (m.enabled === want || m.locked) continue
        unwrap(
          await api.PATCH('/api/v1/modules/{module_id}', {
            params: { path: { module_id: m.id } },
            body: { enabled: want },
          }),
        )
      }
      props.done()
    })
  }

  return (
    <form id="wz-form" class="wz-form" onSubmit={submit} noValidate>
      <p class="dim t13">{t('setup.privacy.lead')}</p>
      {loadError !== null && <Fail error={loadError} retry={load} />}
      {modules &&
        (modules.length === 0 ? (
          <div class="card">
            <Empty title={t('setup.privacy.empty.title')} why={t('setup.privacy.empty.why')} />
          </div>
        ) : (
          <div class="list card wz-list">
            {modules.map((m) => {
              const picked = !!on[m.id]
              return (
                <div class="li wz-mod" key={m.id}>
                  <div class="grow">
                    <div class="name" id={`w-mod-${m.id}`}>
                      {m.name}
                    </div>
                    <div class="desc">{m.locked ? t('setup.privacy.locked') : m.description}</div>
                    {picked && !m.enabled && m.notice && <p class="hint wz-notice">{m.notice}</p>}
                  </div>
                  <button
                    type="button"
                    class={picked ? 'switch on' : 'switch'}
                    role="switch"
                    aria-checked={picked}
                    aria-labelledby={`w-mod-${m.id}`}
                    disabled={m.locked}
                    onClick={() => setOn({ ...on, [m.id]: !picked })}
                  />
                </div>
              )
            })}
          </div>
        ))}
      <p class="hint">{t('setup.privacy.hint')}</p>
      {error !== null && <Fail error={error} known={{ 'module.locked': t('setup.privacy.locked') }} />}
    </form>
  )
}

// ------------------------------------------------------------------ 5 the first product

const PRODUCT_CODE = /^[a-z0-9]+(-[a-z0-9]+)*$/

export function ProductStep(props: StepProps) {
  const [products, setProducts] = useState<Schemas['Product'][] | null>(null)
  const [users, setUsers] = useState<Schemas['User'][]>([])
  const [loadError, setLoadError] = useState<unknown>(null)
  const [name, setName] = useState('')
  const [code, setCode] = useState('')
  const [owner, setOwner] = useState('')
  const [errors, setErrors] = useState<{ name?: string; code?: string }>({})
  const [error, run] = useRun(props)

  const load = () => {
    setLoadError(null)
    Promise.all([api.GET('/api/v1/products').then(unwrap), api.GET('/api/v1/users').then(unwrap)]).then(
      ([p, u]) => {
        setProducts(p.items)
        setUsers(u.items.filter((x) => x.status === 'active'))
      },
      setLoadError,
    )
  }
  useEffect(load, [])

  const submit = (e: Event) => {
    e.preventDefault()
    const n = name.trim()
    const c = code.trim()
    const next: typeof errors = {}
    if (n || c) {
      if (!n) next.name = t('setup.product.error.name')
      if (!PRODUCT_CODE.test(c) || c.length < 2) next.code = t('setup.product.error.code')
    }
    setErrors(next)
    if (Object.keys(next).length) return
    void run(async () => {
      if (n && c) {
        try {
          unwrap(
            await api.POST('/api/v1/products', {
              body: { code: c, name: n, owners: owner ? [owner] : [] },
            }),
          )
        } catch (err) {
          if (isCode(err, 'product.code_reserved'))
            return setErrors({ code: t('setup.product.error.reserved') })
          if (isCode(err, 'product.code_taken')) return setErrors({ code: t('setup.product.error.taken') })
          throw err
        }
      }
      unwrap(await api.POST('/api/v1/setup/complete'))
      props.done()
    })
  }

  return (
    <form id="wz-form" class="wz-form" onSubmit={submit} noValidate>
      <p class="dim t13">{t('setup.product.lead')}</p>
      {loadError !== null && <Fail error={loadError} retry={load} />}
      {products && products.length > 0 && (
        <div>
          <div class="sect-t">{t('setup.product.existing')}</div>
          <div class="row" style={{ gap: '4px' }}>
            {products.map((p) => (
              <span class="badge" key={p.code}>
                {p.name} <span class="mono faint">{p.code}</span>
              </span>
            ))}
          </div>
        </div>
      )}
      <div class="grid g2" style={{ gap: '10px' }}>
        <Field
          id="w-pn"
          label={t('setup.product.name')}
          maxLength={64}
          placeholder={t('setup.product.name.placeholder')}
          value={name}
          error={errors.name}
          onInput={(e) => setName(e.currentTarget.value)}
        />
        <Field
          id="w-pc"
          label={t('setup.product.code')}
          mono
          autocomplete="off"
          autocapitalize="off"
          spellcheck={false}
          maxLength={32}
          placeholder={t('setup.product.code.placeholder')}
          hint={t('setup.product.code.hint')}
          value={code}
          error={errors.code}
          onInput={(e) => setCode(e.currentTarget.value.trim().toLowerCase())}
        />
      </div>
      <FieldShell id="w-po" label={t('setup.product.owner')} hint={t('setup.product.owner.hint')}>
        <select
          class="field"
          id="w-po"
          aria-describedby="w-po-hint"
          value={owner}
          onChange={(e) => setOwner(e.currentTarget.value)}
        >
          <option value="">{t('setup.product.owner.none')}</option>
          {users.map((u) => (
            <option key={u.id} value={u.id}>
              {u.display_name} ({u.username})
            </option>
          ))}
        </select>
      </FieldShell>
      <p class="hint">{t('setup.product.skip')}</p>
      {error !== null && <Fail error={error} />}
    </form>
  )
}
