// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The fields of a resource people set on the platform (prototype editDrawer: 基本信息 and 下发): name and
// summary, who it is for, which stages, the install policy, when it applies and, for an MCP server, how it
// is delivered. The same form serves a new resource and an existing one.

import type { Schemas } from '../../api/client'
import { t } from '../../i18n'
import { Chip, Seg } from '../../ui/controls'
import { policyName } from '../../ui/resource'

export interface Settings {
  name: string
  summary: string
  audience: Schemas['Audience']
  stages: string[]
  scope: string
  policy: Schemas['Policy']
  when: string
  delivery?: Schemas['Delivery']
}

export function settingsOf(r: Schemas['ManagedResource']): Settings {
  return {
    name: r.name,
    summary: r.summary ?? '',
    audience: r.audience,
    stages: [...r.stages],
    scope: r.scope,
    policy: r.policy,
    when: r.when ?? '',
    delivery: r.delivery,
  }
}

/** changes lists the fields that differ, as an UpdateResourceRequest. */
export function changes(before: Settings, now: Settings): Schemas['UpdateResourceRequest'] {
  const out: Schemas['UpdateResourceRequest'] = {}
  if (now.name.trim() !== before.name) out.name = now.name.trim()
  if (now.summary.trim() !== before.summary) out.summary = now.summary.trim()
  if (now.audience !== before.audience) out.audience = now.audience
  if (now.stages.join() !== before.stages.join()) out.stages = now.stages
  if (now.scope !== before.scope) out.scope = now.scope
  if (now.policy !== before.policy) out.policy = now.policy
  if (now.when.trim() !== before.when) out.when = now.when.trim()
  if (now.delivery && now.delivery !== before.delivery) out.delivery = now.delivery
  return out
}

export function BasicFields(props: {
  value: Settings
  onChange: (next: Settings) => void
  code: string
  onCode?: (code: string) => void
  kind: string
}) {
  const v = props.value
  const codeOk = /^[a-z0-9]+(-[a-z0-9]+)*$/.test(props.code) && props.code.length >= 2
  return (
    <>
      <div class="sect-t" style={{ margin: 0 }}>
        {t('resources.edit.basic')}
      </div>
      <div>
        <label class="fl" for="e-n">
          {t('resources.edit.name')}
        </label>
        <input
          class="field"
          id="e-n"
          maxLength={64}
          value={v.name}
          onInput={(e) => props.onChange({ ...v, name: e.currentTarget.value })}
        />
      </div>
      <div>
        <label class="fl" for="e-c">
          {t('resources.edit.code')}
        </label>
        <input
          class="field mono"
          id="e-c"
          maxLength={48}
          value={props.code}
          readOnly={!props.onCode}
          aria-invalid={props.onCode && props.code !== '' && !codeOk}
          placeholder={t('resources.edit.code_ph')}
          onInput={(e) => props.onCode?.(e.currentTarget.value.trim().toLowerCase())}
        />
        <p class="hint">
          {props.onCode && props.code !== '' && !codeOk
            ? t('resources.edit.code_bad')
            : props.kind === 'workflow'
              ? t('resources.edit.code_hint_workflow', { code: props.code || t('resources.edit.code_word') })
              : t('resources.edit.code_hint')}
        </p>
      </div>
      <div>
        <label class="fl" for="e-d">
          {t('resources.edit.summary')}
        </label>
        <textarea
          class="field"
          id="e-d"
          rows={2}
          maxLength={500}
          value={v.summary}
          onInput={(e) => props.onChange({ ...v, summary: e.currentTarget.value })}
        />
      </div>
    </>
  )
}

export function DeliveryFields(props: {
  value: Settings
  onChange: (next: Settings) => void
  kind: string
  stages: readonly Schemas['Stage'][]
  products: readonly Schemas['Product'][]
}) {
  const v = props.value
  const set = (patch: Partial<Settings>) => props.onChange({ ...v, ...patch })
  const visibleStages = props.stages.filter((s) => !s.hidden || v.stages.includes(s.id))
  return (
    <>
      <div class="sect-t" style={{ margin: '8px 0 0' }}>
        {t('resources.edit.delivery')}
      </div>
      <div>
        <div class="fl">{t('resources.edit.audience')}</div>
        <Seg
          value={v.audience}
          onChange={(audience) => set({ audience })}
          label={t('resources.edit.audience')}
          items={[
            ['rd', t('resources.audience.rd')],
            ['mgmt', t('resources.audience.mgmt')],
          ]}
        />
      </div>
      <div>
        <div class="fl">{t('resources.edit.stages')}</div>
        <div class="row" role="group" aria-label={t('resources.edit.stages')}>
          {visibleStages.map((s) => (
            <Chip
              key={s.id}
              on={v.stages.includes(s.id)}
              onClick={() =>
                set({
                  stages: v.stages.includes(s.id)
                    ? v.stages.filter((x) => x !== s.id)
                    : props.stages.filter((x) => x.id === s.id || v.stages.includes(x.id)).map((x) => x.id),
                })
              }
            >
              {s.name}
            </Chip>
          ))}
        </div>
        <p class="hint">{t('resources.edit.stages_hint')}</p>
      </div>
      <div>
        <div class="fl">{t('resources.edit.scope')}</div>
        <div class="row" role="group" aria-label={t('resources.edit.scope')}>
          <Chip on={v.scope === 'org'} onClick={() => set({ scope: 'org' })}>
            {t('resources.scope.org')}
          </Chip>
          {props.products.map((p) => (
            <Chip key={p.code} on={v.scope === p.code} onClick={() => set({ scope: p.code })}>
              {p.name}
            </Chip>
          ))}
          {v.scope !== 'org' && !props.products.some((p) => p.code === v.scope) && (
            <Chip on onClick={() => undefined}>
              {v.scope === 'me' ? t('resources.scope.me') : v.scope}
            </Chip>
          )}
        </div>
      </div>
      <div>
        <label class="fl" for="e-pol">
          {t('resources.edit.policy')}
        </label>
        <select
          class="field"
          id="e-pol"
          style={{ width: 'auto' }}
          value={v.policy}
          onChange={(e) => set({ policy: e.currentTarget.value as Schemas['Policy'] })}
        >
          {(['required', 'recommended', 'optional'] as const).map((p) => (
            <option key={p} value={p}>
              {policyName(p)}
            </option>
          ))}
        </select>
        <p class="hint">{t('resources.edit.policy_hint')}</p>
      </div>
      {props.kind === 'mcp' && (
        <div>
          <div class="fl">{t('resources.edit.via')}</div>
          <Seg
            value={v.delivery ?? 'native'}
            onChange={(delivery) => set({ delivery })}
            label={t('resources.edit.via')}
            items={[
              ['native', t('resources.via.native')],
              ['on_demand', t('resources.via.on_demand')],
            ]}
          />
          <p class="hint">{t('resources.edit.via_hint')}</p>
        </div>
      )}
      <div>
        <label class="fl" for="e-when">
          {t('resources.edit.when')} <span class="faint">{t('resources.edit.when_note')}</span>
        </label>
        <input
          class="field"
          id="e-when"
          maxLength={200}
          value={v.when}
          placeholder={t('resources.edit.when_ph')}
          onInput={(e) => set({ when: e.currentTarget.value })}
        />
      </div>
    </>
  )
}
