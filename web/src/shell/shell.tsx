// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The shell around every signed-in page, as in the prototype: the sidebar with the logo, the navigation
// for the person's role, the help links and the account; the top bar on narrow screens; the feedback
// button. Markup and class names follow the prototype's render() in app.js.

import { Fragment, type ComponentChildren } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { useLocation } from 'preact-iso'
import { BRAND } from '../brand.gen'
import { t } from '../i18n'
import { findPage, groupLetter, pageCode, visibleGroups } from '../pages/registry'
import { href, stripBase } from '../router/paths'
import { Logo, Lockup } from '../ui/brand'
import { Icon } from '../ui/icons'
import { Toast } from '../ui/toast'
import { loadMissingSecrets, missingSecrets } from './counts'
import { FeedbackDialog, openFeedback } from './feedback-dialog'
import { DOCS_URL } from './links'
import { me, meta, role } from './session'
import { ThemeButton } from './theme-button'
import { initial, UserMenu } from './user-menu'

export function Shell(props: { children: ComponentChildren }) {
  const { path } = useLocation()
  const here = stripBase(path)
  const page = findPage(here)
  const code = page && pageCode(page.id)
  // A sub-path of the same page (a resource's details at /catalog/<code>) keeps the page where it is.
  const pageKey = page?.id ?? here
  const [navOpen, setNavOpen] = useState(false)
  const [menuOpen, setMenuOpen] = useState(false)
  const main = useRef<HTMLElement>(null)
  const wrap = useRef<HTMLDivElement>(null)

  // A new page starts at the top, with the sidebar and the account menu closed.
  useEffect(() => {
    setNavOpen(false)
    setMenuOpen(false)
    if (main.current) main.current.scrollTop = 0
    if (innerWidth <= 760) scrollTo(0, 0)
    document.title = page ? `${t(page.label)} · ${BRAND.name}` : BRAND.name
  }, [pageKey, page])

  useEffect(loadMissingSecrets, [])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      setNavOpen(false)
      setMenuOpen(false)
    }
    addEventListener('keydown', onKey)
    return () => removeEventListener('keydown', onKey)
  }, [])

  const person = me.value
  const groups = visibleGroups(role.value, meta.value?.modules ?? [])
  const skip = () => {
    wrap.current?.focus()
    wrap.current?.scrollIntoView()
  }

  return (
    <>
      <button type="button" class="skip" onClick={skip}>
        {t('shell.skip')}
      </button>
      <div class="shell">
        <header class="mtop">
          <button
            type="button"
            class="btn ghost icon"
            onClick={() => setNavOpen(true)}
            aria-label={t('shell.nav.open')}
            aria-expanded={navOpen}
          >
            <Icon name="menu" />
          </button>
          <Logo />
          <span class="pg">
            {code && <span class="tag">{`${code.letter}-${code.number}`}</span>}
            {page && t(page.label)}
          </span>
          <ThemeButton />
        </header>
        {navOpen && <div class="side-scrim" onClick={() => setNavOpen(false)} />}
        <aside class={navOpen ? 'side open' : 'side'} aria-label={t('shell.side.label')}>
          <div class="side-brand">
            <Lockup />
            <span class="tag">{location.host}</span>
          </div>
          <nav class="nav" aria-label={t('shell.nav.label')}>
            {groups.map((g) => (
              <Fragment key={g.label}>
                <div class="nav-group">
                  <b>{groupLetter(g)}</b>
                  <span class="tag">{t(g.label)}</span>
                </div>
                {g.pages.map((p) => {
                  const on = p.id === page?.id
                  return (
                    <a
                      key={p.id}
                      href={href(p.path)}
                      class={on ? 'on' : undefined}
                      aria-current={on ? 'page' : undefined}
                    >
                      <Icon name={p.icon} />
                      {t(p.label)}
                      {p.id === 'keys' && missingSecrets.value > 0 && (
                        <span class="cnt" title={t('shell.count.secrets', { count: missingSecrets.value })}>
                          {missingSecrets.value}
                        </span>
                      )}
                    </a>
                  )
                })}
              </Fragment>
            ))}
          </nav>
          <div class="side-help">
            <a href={DOCS_URL} target="_blank" rel="noopener noreferrer">
              <Icon name="book" />
              {t('shell.help.docs')}
              <Icon name="out" class="ext" />
              <span class="sr">{t('shell.help.new_window')}</span>
            </a>
          </div>
          {person && (
            <div class="side-foot">
              {menuOpen && <UserMenu onClose={() => setMenuOpen(false)} />}
              <button
                type="button"
                class="who"
                onClick={() => setMenuOpen(!menuOpen)}
                aria-expanded={menuOpen}
                aria-haspopup="dialog"
              >
                <span class="avatar">{initial(person.display_name)}</span>
                <div class="txt">
                  <div class="t13" style={{ fontWeight: 600 }}>
                    {person.display_name}
                  </div>
                  <div class="faint t11">
                    {person.job
                      ? t('shell.account.line', { role: roleName(role.value), job: person.job })
                      : roleName(role.value)}
                  </div>
                </div>
              </button>
              <ThemeButton />
            </div>
          )}
        </aside>
        <main class="main" ref={main}>
          <div key={pageKey} class="wrap pg-in" tabIndex={-1} ref={wrap}>
            {props.children}
          </div>
        </main>
      </div>
      <button
        type="button"
        class="fab"
        onClick={() => openFeedback()}
        aria-label={t('shell.feedback')}
        title={t('shell.feedback')}
      >
        <Icon name="chat" />
        <span class="fab-t">{t('shell.feedback')}</span>
      </button>
      <FeedbackDialog />
      <Toast />
    </>
  )
}

function roleName(r: string): string {
  switch (r) {
    case 'admin':
      return t('shell.role.admin')
    case 'owner':
      return t('shell.role.owner')
    default:
      return t('shell.role.member')
  }
}
