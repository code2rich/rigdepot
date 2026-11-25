// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The application: load what the console needs to start (/api/v1/meta, the language, /api/v1/me), then
// route. Public pages (sign-in, setup …) render on their own; every other page renders inside the shell
// and needs someone signed in. Someone not signed in goes to the sign-in page, which brings them back.

import { useEffect, useState } from 'preact/hooks'
import { ErrorBoundary, LocationProvider, Route, Router, useLocation } from 'preact-iso'
import { ApiError, api, onSignedOut, unwrap } from './api/client'
import { BRAND } from './brand.gen'
import { chooseLocale, setLocale, t } from './i18n'
import { NAV, PUBLIC, findPublic } from './pages/registry'
import { NotFound } from './pages/not-found'
import { href, stripBase } from './router/paths'
import { Shell } from './shell/shell'
import { me, meta } from './shell/session'
import { Empty } from './ui/empty'

type Boot = { state: 'loading' } | { state: 'ready' } | { state: 'failed'; error: unknown }

async function boot(): Promise<void> {
  await setLocale(chooseLocale())
  meta.value = unwrap(await api.GET('/api/v1/meta'))
  const res = await api.GET('/api/v1/me')
  if (res.response.status === 401) {
    me.value = null
  } else {
    me.value = unwrap(res)
    await setLocale(chooseLocale(me.value.locale))
  }
}

/** signInPath is the sign-in page that comes back to the given console path afterwards. */
export function signInPath(next: string): string {
  return next === '/' ? href('/login') : `${href('/login')}?next=${encodeURIComponent(next)}`
}

function Routes() {
  const { path, route } = useLocation()
  const here = stripBase(path)
  const publicPage = findPublic(here)
  const isPublic = publicPage !== undefined

  useEffect(() => {
    onSignedOut(() => {
      me.value = null
    })
  }, [])

  // Pages in the shell need someone signed in; the first-run wizard comes before anything else.
  const target =
    meta.value && !meta.value.setup_complete && here !== '/setup'
      ? href('/setup')
      : !isPublic && !me.value
        ? signInPath(here)
        : undefined
  useEffect(() => {
    if (target) route(target, true)
  }, [target, route])
  if (target) return null

  if (publicPage) {
    document.title = `${t(publicPage.label)} · ${BRAND.name}`
    return (
      <Router>
        {PUBLIC.map((p) => (
          <Route key={p.id} path={href(p.path)} component={p.component} />
        ))}
      </Router>
    )
  }
  return (
    <Shell>
      <Router>
        {[
          // A page also owns the paths under its own (/manage/resources/<code>): one route, so that moving
          // between them keeps the page mounted. The page reads the rest from useRoute().params.sub.
          ...NAV.flatMap((g) => g.pages).map((p) => (
            <Route
              key={p.id}
              path={p.path === '/' ? href(p.path) : `${href(p.path)}/:sub*`}
              component={p.component}
            />
          )),
          <Route key="404" default component={NotFound} />,
        ]}
      </Router>
    </Shell>
  )
}

function Failed(props: { error: unknown; retry: () => void }) {
  const offline = props.error instanceof ApiError && props.error.status === 0
  return (
    <div class="center-page">
      <div class="card center-card">
        <Empty
          title={t('boot.failed.title')}
          why={offline ? t('boot.failed.offline') : t('boot.failed.server')}
        >
          <button type="button" class="btn" onClick={props.retry}>
            {t('boot.failed.retry')}
          </button>
        </Empty>
      </div>
    </div>
  )
}

export function App() {
  const [state, setState] = useState<Boot>({ state: 'loading' })
  const start = () => {
    setState({ state: 'loading' })
    boot().then(
      () => setState({ state: 'ready' }),
      (error: unknown) => setState({ state: 'failed', error }),
    )
  }
  useEffect(start, [])

  if (state.state === 'loading') return null
  if (state.state === 'failed') return <Failed error={state.error} retry={start} />
  return (
    <LocationProvider>
      <ErrorBoundary>
        <Routes />
      </ErrorBoundary>
    </LocationProvider>
  )
}
