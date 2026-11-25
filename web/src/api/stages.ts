// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The stages of the delivery process (/api/v1/stages), with names in the console's language. Loaded once
// per language and shared; a stage the list does not have shows its id.

import { signal } from '@preact/signals'
import { useEffect } from 'preact/hooks'
import { locale } from '../i18n'
import { api, unwrap, type Schemas } from './client'

const cache = signal<{ locale: string; items: readonly Schemas['Stage'][] } | null>(null)
let loading: string | null = null

/** useStages returns the stages, empty until they have loaded (or when they cannot be). */
export function useStages(): readonly Schemas['Stage'][] {
  const lang = locale.value
  useEffect(() => {
    if (cache.value?.locale === lang || loading === lang) return
    loading = lang
    api
      .GET('/api/v1/stages')
      .then(unwrap)
      .then(
        (list) => {
          cache.value = { locale: lang, items: list.items }
          loading = null
        },
        () => {
          loading = null
        },
      )
  }, [lang])
  return cache.value?.items ?? []
}

/** stageName is a stage's display name from a list returned by useStages. */
export function stageName(stages: readonly Schemas['Stage'][], id: string): string {
  return stages.find((s) => s.id === id)?.name ?? id
}
