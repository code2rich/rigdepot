// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// D-04 平台设置 (prototype V.settings in app.js). What the setup wizard asked can be changed here: the resource
// repositories, the optional modules, outgoing mail and the organisation's name. Every change is audited.
// Platform administrators only; the server refuses everyone else.
//
// The prototype's 数据与隐私 card becomes the list of optional modules (usage insights is one); its AI 评审
// and 环境资源 cards belong to later milestones. Sign-in methods live on the users page (D-01).

import { useState } from 'preact/hooks'
import { ApiError, api, unwrap, type Schemas } from '../../api/client'
import { useLoad } from '../../ui/load'
import { t } from '../../i18n'
import { meta } from '../../shell/session'
import { Switch } from '../../ui/controls'
import { Modal } from '../../ui/overlay'
import { PageHeader } from '../../ui/page-header'
import { Fail } from '../../ui/fail'
import { LoadError } from '../../ui/load'
import { toast } from '../../ui/toast'
import { MailCard, OrganizationCard } from './mail'
import { SourcesCard } from './sources'

export default function Settings() {
  const settings = useLoad(async () => {
    const res = await api.GET('/api/v1/settings')
    return unwrap(res)
  }, [])

  const denied =
    settings.state === 'failed' && settings.error instanceof ApiError && settings.error.status === 403
  return (
    <>
      <PageHeader page="settings" title={t('nav.settings')} lead={t('settings.lead')} />
      {denied ? (
        <div class="card">
          <LoadError error={settings.state === 'failed' ? settings.error : null} />
        </div>
      ) : (
        <div class="grid g2" style={{ alignItems: 'start' }}>
          <SourcesCard />
          <ModulesCard />
          <MailCard settings={settings} reload={settings.reload} onSaved={settings.set} />
          <OrganizationCard settings={settings} reload={settings.reload} onSaved={settings.set} />
        </div>
      )}
    </>
  )
}

/** The optional modules compiled into this server, each with its switch. */
function ModulesCard() {
  const modules = useLoad(async () => unwrap(await api.GET('/api/v1/modules')).items, [])
  const [asking, setAsking] = useState<Schemas['Module'] | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const list = modules.data ?? []

  const set = async (m: Schemas['Module'], enabled: boolean) => {
    setAsking(null)
    setBusy(m.id)
    setError(null)
    const out = await api
      .PATCH('/api/v1/modules/{module_id}', { params: { path: { module_id: m.id } }, body: { enabled } })
      .then(unwrap)
      .catch((err: unknown) => err)
    setBusy(null)
    if (out instanceof Error) {
      setError(out)
      return
    }
    const next = list.map((x) => (x.id === m.id ? (out as Schemas['Module']) : x))
    modules.set(next)
    // The navigation follows: a module's pages show only while it is on.
    if (meta.value) meta.value = { ...meta.value, modules: next.filter((x) => x.enabled).map((x) => x.id) }
    toast(enabled ? t('settings.modules.on', { name: m.name }) : t('settings.modules.off', { name: m.name }))
  }

  return (
    <section class="card pad">
      <h3>{t('settings.modules.title')}</h3>
      {modules.state === 'failed' && !modules.data ? (
        <LoadError error={modules.error} retry={modules.reload} />
      ) : !modules.data ? (
        <div aria-busy="true" style={{ minHeight: '80px' }} />
      ) : list.length === 0 ? (
        <p class="faint t13">{t('settings.modules.none')}</p>
      ) : (
        <div class="list">
          {list.map((m) => (
            <div key={m.id} class="li">
              <div class="grow">
                <div class="name">{m.name}</div>
                <div class="desc" style={{ whiteSpace: 'normal' }}>
                  {m.description}
                  {m.locked && <> {t('settings.modules.locked')}</>}
                </div>
              </div>
              <Switch
                on={m.enabled}
                label={m.name}
                disabled={m.locked || busy === m.id}
                onChange={(on) => (on && m.notice ? setAsking(m) : void set(m, on))}
              />
            </div>
          ))}
        </div>
      )}
      <Fail error={error} />
      <p class="hint">{t('settings.modules.hint')}</p>
      {asking && (
        <Modal
          title={t('settings.modules.ask', { name: asking.name })}
          onClose={() => setAsking(null)}
          foot={
            <>
              <button type="button" class="btn ghost" onClick={() => setAsking(null)}>
                {t('ui.cancel')}
              </button>
              <button type="button" class="btn primary" onClick={() => void set(asking, true)}>
                {t('settings.modules.turn_on')}
              </button>
            </>
          }
        >
          <p class="t13">{asking.notice}</p>
          <p class="hint">{t('settings.modules.ask_hint')}</p>
        </Modal>
      )}
    </section>
  )
}
