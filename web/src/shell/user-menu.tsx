// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

import { api } from '../api/client'
import { t } from '../i18n'
import { href } from '../router/paths'
import { Icon } from '../ui/icons'
import { me } from './session'

/** UserMenu is the account popover above the account button: who is signed in, their products, sign out. */
export function UserMenu(props: { onClose: () => void }) {
  const person = me.value
  if (!person) return null
  const signOut = async () => {
    await api.POST('/api/v1/auth/logout')
    me.value = null
    location.assign(href('/login'))
  }
  return (
    <>
      <div class="pop-back" onClick={props.onClose} />
      <div class="pop" role="dialog" aria-label={t('shell.account.menu')}>
        <div class="row" style={{ flexWrap: 'nowrap', gap: '12px' }}>
          <span class="avatar lg">{initial(person.display_name)}</span>
          <div>
            <div style={{ fontWeight: 600 }}>{person.display_name}</div>
            <div class="mono faint">{person.username}</div>
          </div>
        </div>
        <dl class="kv lined" style={{ gridTemplateColumns: '64px minmax(0,1fr)', fontSize: '12.5px' }}>
          {person.job && (
            <>
              <dt>{t('shell.account.job')}</dt>
              <dd>{person.job}</dd>
            </>
          )}
          <dt>{t('shell.account.platform_role')}</dt>
          <dd>
            {person.platform_admin ? (
              <span class="badge brand">{t('shell.role.admin')}</span>
            ) : (
              t('shell.account.regular')
            )}
          </dd>
          {person.memberships.length > 0 && (
            <>
              <dt>{t('shell.account.products')}</dt>
              <dd>
                {person.memberships.map((m) => (
                  <div
                    key={m.product.code}
                    class="row"
                    style={{ justifyContent: 'space-between', flexWrap: 'nowrap', padding: '1px 0' }}
                  >
                    <span>{m.product.name}</span>
                    <span class={m.role === 'owner' ? 'badge brand' : 'badge'}>
                      {m.role === 'owner' ? t('shell.membership.owner') : t('shell.membership.member')}
                    </span>
                  </div>
                ))}
              </dd>
            </>
          )}
        </dl>
        <div class="row" style={{ borderTop: '1px solid var(--line)', paddingTop: '10px', gap: '2px' }}>
          <a
            class="btn ghost icon"
            href={href('/keys')}
            onClick={props.onClose}
            aria-label={t('nav.keys')}
            title={t('nav.keys')}
          >
            <Icon name="key" />
          </a>
          <span class="grow" />
          <button type="button" class="btn ghost sm" onClick={signOut}>
            <Icon name="logout" />
            {t('shell.account.sign_out')}
          </button>
        </div>
      </div>
    </>
  )
}

/** initial is the first character of a name, for the avatar square. */
export function initial(name: string): string {
  return Array.from(name.trim())[0]?.toUpperCase() ?? '?'
}
