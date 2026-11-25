// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Loading data for a page and saying clearly when it failed. Every page shows three states besides the
// data: loading, failed (with why and a retry), and empty (the page's own Empty). A 403 is explained, not
// hidden: the server decides who may see what.

import { useCallback, useEffect, useRef, useState } from 'preact/hooks'
import { ApiError } from '../api/client'
import { t } from '../i18n'
import { Empty } from './empty'

export type Loaded<T> =
  | { state: 'loading'; data?: undefined; error?: undefined }
  | { state: 'ready'; data: T; error?: undefined }
  /** A failed reload keeps the data it had, so a page can say what failed next to what it still shows. */
  | { state: 'failed'; data?: T; error: unknown }

/** What useLoad returns: the state, reload (resolves when it is done) and set (after a change came back). */
export type Load<T> = Loaded<T> & { reload: () => Promise<void>; set: (data: T) => void }

/**
 * useLoad runs load when the component mounts and whenever deps change, and returns its state, a reload
 * function and a setter for data a change returned. A reload keeps showing the previous data until the new
 * data is there; a newer load wins over an older one still on its way.
 */
export function useLoad<T>(load: () => Promise<T>, deps: readonly unknown[] = []): Load<T> {
  const [state, setState] = useState<Loaded<T>>({ state: 'loading' })
  const run = useRef(0)
  const fn = useRef(load)
  fn.current = load
  const reload = useCallback(() => {
    const n = ++run.current
    return fn.current().then(
      (data) => {
        if (n === run.current) setState({ state: 'ready', data })
      },
      (error: unknown) => {
        if (n === run.current) setState((prev) => ({ state: 'failed', error, data: prev.data }))
      },
    )
  }, deps)
  const set = useCallback((data: T) => {
    run.current++
    setState({ state: 'ready', data })
  }, [])
  useEffect(() => {
    setState({ state: 'loading' })
    void reload()
  }, [reload])
  return { ...state, reload, set }
}

/** status is the HTTP status of a failed call: 0 when the server could not be reached, -1 for other errors. */
export function status(error: unknown): number {
  return error instanceof ApiError ? error.status : -1
}

/** LoadError is the failed state of a page or a panel: why it failed and a retry button. */
export function LoadError(props: { error: unknown; retry?: () => void }) {
  const s = status(props.error)
  const title =
    s === 403 ? t('ui.load.denied.title') : s === 0 ? t('ui.load.offline.title') : t('ui.load.failed.title')
  const why =
    s === 403
      ? t('ui.load.denied.why')
      : s === 0
        ? t('ui.load.offline.why')
        : props.error instanceof ApiError && props.error.problem?.detail
          ? props.error.problem.detail
          : t('ui.load.failed.why')
  return (
    <Empty title={title} why={why}>
      {s !== 403 && props.retry && (
        <button type="button" class="btn" onClick={props.retry}>
          {t('ui.retry')}
        </button>
      )}
    </Empty>
  )
}

/** Loading is the placeholder while a panel's data is on its way. */
export function Loading() {
  return (
    <div class="loading" role="status">
      <span class="dot pulse" />
      {t('ui.loading')}
    </div>
  )
}
