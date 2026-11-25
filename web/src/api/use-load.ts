// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Loading data for a page or a part of it: the three states every list shows (loading, failed, loaded) and a
// way to load again after a change. A newer load wins over an older one still on its way.

import { useCallback, useEffect, useRef, useState } from 'preact/hooks'

export type Load<T> =
  { state: 'loading'; data?: T } | { state: 'failed'; error: unknown; data?: T } | { state: 'ready'; data: T }

/** useLoad runs fetch now and whenever deps change; reload runs it again and keeps showing the old data meanwhile. */
export function useLoad<T>(
  fetch: () => Promise<T>,
  deps: readonly unknown[],
): [Load<T>, () => Promise<void>, (next: T) => void] {
  const [load, setLoad] = useState<Load<T>>({ state: 'loading' })
  const seq = useRef(0)
  const fn = useRef(fetch)
  fn.current = fetch
  const run = useCallback(async () => {
    const mine = ++seq.current
    setLoad((prev) => ({ state: 'loading', data: prev.data }))
    try {
      const data = await fn.current()
      if (mine === seq.current) setLoad({ state: 'ready', data })
    } catch (error) {
      if (mine === seq.current) setLoad((prev) => ({ state: 'failed', error, data: prev.data }))
    }
  }, [])
  // The caller lists what the fetch depends on.
  useEffect(() => void run(), deps)
  const set = useCallback((data: T) => {
    seq.current++
    setLoad({ state: 'ready', data })
  }, [])
  return [load, run, set]
}
