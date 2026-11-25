// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Finding people to add to a product or make its owners: a search over /api/v1/users (platform
// administrators and product owners may search) with the matches as toggles.

import { useEffect, useState } from 'preact/hooks'
import { api, unwrap, type Schemas } from '../../api/client'
import { t } from '../../i18n'
import { initial } from '../../shell/user-menu'
import { Icon } from '../../ui/icons'
import { errorText } from '../../ui/problem'

export function PeoplePicker(props: {
  id: string
  label: string
  picked: readonly Schemas['UserRef'][]
  onChange: (next: Schemas['UserRef'][]) => void
  /** People who cannot be picked, with the reason shown next to them. */
  taken?: ReadonlyMap<string, string>
}) {
  const [q, setQ] = useState('')
  const [found, setFound] = useState<Schemas['User'][] | null>(null)
  const [error, setError] = useState<unknown>(null)

  useEffect(() => {
    const query = q.trim()
    if (!query) {
      setFound(null)
      setError(null)
      return
    }
    let live = true
    const timer = setTimeout(() => {
      api
        .GET('/api/v1/users', { params: { query: { q: query } } })
        .then(unwrap)
        .then(
          (list) => {
            if (!live) return
            setFound(list.items.filter((u) => u.status !== 'disabled'))
            setError(null)
          },
          (err: unknown) => live && setError(err),
        )
    }, 200)
    return () => {
      live = false
      clearTimeout(timer)
    }
  }, [q])

  const isPicked = (u: Schemas['UserRef']) => props.picked.some((p) => p.id === u.id)
  const toggle = (u: Schemas['UserRef']) =>
    props.onChange(
      isPicked(u)
        ? props.picked.filter((p) => p.id !== u.id)
        : [...props.picked, { id: u.id, username: u.username, display_name: u.display_name }],
    )

  return (
    <div>
      <label class="fl" for={props.id}>
        {props.label}
      </label>
      <div class="search" style={{ width: '100%' }}>
        <Icon name="search" />
        <input
          class="field"
          id={props.id}
          value={q}
          onInput={(e) => setQ(e.currentTarget.value)}
          placeholder={t('products.people.placeholder')}
          autocomplete="off"
        />
      </div>
      {props.picked.length > 0 && (
        <div class="row" style={{ marginTop: '8px', gap: '4px' }}>
          {props.picked.map((u) => (
            <button
              key={u.id}
              type="button"
              class="badge chip brand"
              aria-pressed="true"
              onClick={() => toggle(u)}
              aria-label={t('products.people.unpick', { name: u.display_name })}
            >
              {u.display_name}
              <Icon name="x" />
            </button>
          ))}
        </div>
      )}
      {error !== null && (
        <p class="hint" role="alert">
          {errorText(error)}
        </p>
      )}
      {found && found.length === 0 && <p class="hint">{t('products.people.none')}</p>}
      {found && found.length > 0 && (
        <div style={{ display: 'grid', gap: '4px', marginTop: '8px', maxHeight: '220px', overflowY: 'auto' }}>
          {found.map((u) => {
            const taken = props.taken?.get(u.id)
            const on = isPicked(u)
            return (
              <button
                key={u.id}
                type="button"
                class={on ? 'pick on' : 'pick'}
                aria-pressed={on}
                disabled={taken !== undefined}
                onClick={() => toggle(u)}
                style={{ padding: '7px 12px', opacity: taken ? 0.6 : undefined }}
              >
                <span class="avatar">{initial(u.display_name)}</span>
                <span class="grow">
                  <span class="pk-t">
                    {u.display_name} <span class="mono faint">{u.username}</span>
                  </span>
                  <span class="pk-d">{taken ?? (u.job || u.email)}</span>
                </span>
                {on && <Icon name="check" />}
              </button>
            )
          })}
        </div>
      )}
    </div>
  )
}
