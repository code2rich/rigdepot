// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The two dialogs of the home page's header: 个人信息 (the stages I follow; PATCH /api/v1/me) and
// 申请加入其他产品 (POST /api/v1/me/join-requests). Following a stage only decides what the home page
// shows first and which notifications arrive; it grants nothing.

import { useState } from 'preact/hooks'
import { api, unwrap } from '../../api/client'
import { t } from '../../i18n'
import { me } from '../../shell/session'
import { Empty } from '../../ui/empty'
import { LoadError, Loading, useLoad } from '../../ui/load'
import { failText } from '../../ui/fail'
import { Modal } from '../../ui/overlay'
import { useStages } from '../../ui/resource'
import { toast } from '../../ui/toast'

export function ProfileDialog(props: { onClose: () => void }) {
  const person = me.value
  const stages = useStages().filter((s) => !s.hidden)
  const [focus, setFocus] = useState<string[]>(person?.focus_stages ?? [])
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  if (!person) return null
  const flip = (id: string) => setFocus((f) => (f.includes(id) ? f.filter((x) => x !== id) : [...f, id]))
  const save = async () => {
    setSaving(true)
    setError('')
    try {
      me.value = unwrap(await api.PATCH('/api/v1/me', { body: { focus_stages: focus } }))
      toast(t('ui.saved'))
      props.onClose()
    } catch (err) {
      setError(failText(err))
    } finally {
      setSaving(false)
    }
  }
  return (
    <Modal
      title={t('home.profile.title')}
      onClose={props.onClose}
      foot={
        <>
          <button type="button" class="btn ghost" onClick={props.onClose}>
            {t('ui.cancel')}
          </button>
          <button type="button" class="btn primary" onClick={save} disabled={saving}>
            {t('ui.save')}
          </button>
        </>
      }
    >
      {error && (
        <div class="callout bad" role="alert">
          {error}
        </div>
      )}
      <dl class="kv lined">
        <dt>{t('home.profile.name')}</dt>
        <dd>{person.display_name}</dd>
        <dt>{t('home.profile.account')}</dt>
        <dd class="mono">{person.username}</dd>
        {person.job && (
          <>
            <dt>{t('shell.account.job')}</dt>
            <dd>{person.job}</dd>
          </>
        )}
      </dl>
      <div>
        <div class="fl">{t('home.profile.focus')}</div>
        <div class="row" role="group" aria-label={t('home.profile.focus')}>
          {stages.map((s) => {
            const on = focus.includes(s.id)
            return (
              <button
                key={s.id}
                type="button"
                class={on ? 'badge chip brand' : 'badge chip'}
                aria-pressed={on}
                onClick={() => flip(s.id)}
              >
                {s.name}
              </button>
            )
          })}
        </div>
        <p class="hint">{t('home.profile.focus.hint')}</p>
      </div>
    </Modal>
  )
}

export function JoinDialog(props: { pending: string[]; onClose: () => void; onDone: () => void }) {
  const products = useLoad(async () => unwrap(await api.GET('/api/v1/products')).items)
  const [picked, setPicked] = useState<string[]>([])
  const [sending, setSending] = useState(false)
  const [error, setError] = useState('')
  const open = (products.data ?? []).filter((p) => !p.my_role && !props.pending.includes(p.code))
  const flip = (code: string) =>
    setPicked((x) => (x.includes(code) ? x.filter((c) => c !== code) : [...x, code]))
  const submit = async () => {
    if (picked.length === 0) {
      setError(t('home.join.pick'))
      return
    }
    setSending(true)
    setError('')
    try {
      unwrap(await api.POST('/api/v1/me/join-requests', { body: { products: picked } }))
      toast(t('home.join.sent'))
      props.onDone()
      props.onClose()
    } catch (err) {
      setError(failText(err))
    } finally {
      setSending(false)
    }
  }
  return (
    <Modal
      title={t('home.join.title')}
      onClose={props.onClose}
      foot={
        <>
          <button type="button" class="btn ghost" onClick={props.onClose}>
            {t('ui.cancel')}
          </button>
          <button type="button" class="btn primary" onClick={submit} disabled={sending || open.length === 0}>
            {t('home.join.submit')}
          </button>
        </>
      }
    >
      {error && (
        <div class="callout bad" role="alert">
          {error}
        </div>
      )}
      {products.state === 'loading' && <Loading />}
      {products.state === 'failed' && <LoadError error={products.error} retry={products.reload} />}
      {products.state === 'ready' &&
        (open.length === 0 ? (
          <Empty title={t('home.join.none')} why={t('home.join.none.why')} />
        ) : (
          <div style={{ display: 'grid', gap: '6px' }}>
            {open.map((p) => {
              const on = picked.includes(p.code)
              return (
                <button
                  key={p.code}
                  type="button"
                  class={on ? 'pick on' : 'pick'}
                  aria-pressed={on}
                  onClick={() => flip(p.code)}
                >
                  <span class="grow">
                    <span class="pk-t">{p.name}</span>
                    <span class="pk-d">
                      {t('home.join.owners', {
                        line: p.line ?? '',
                        owners: p.owners.map((o) => o.display_name).join(t('ui.list_sep')),
                      })}
                    </span>
                  </span>
                </button>
              )
            })}
          </div>
        ))}
      <p class="hint" style={{ margin: 0 }}>
        {t('home.join.hint')}
      </p>
    </Modal>
  )
}
