// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The stand-in for a page that has a place in the navigation but is not built yet. Each page task replaces
// its notBuilt(...) entry in registry.ts with the real page.

import type { ComponentType } from 'preact'
import { t } from '../i18n'
import { Empty } from '../ui/empty'
import { PageHeader } from '../ui/page-header'
import { pageCode } from './registry'

export function notBuilt(id: string): ComponentType {
  function NotBuilt() {
    const code = pageCode(id)
    if (!code) {
      return (
        <div class="center-page">
          <div class="card center-card">
            <Empty title={t('page.not_built.title')} why={t('page.not_built.why')} />
          </div>
        </div>
      )
    }
    return (
      <>
        <PageHeader page={id} title={t(code.label)} />
        <div class="card">
          <Empty title={t('page.not_built.title')} why={t('page.not_built.why')} />
        </div>
      </>
    )
  }
  NotBuilt.displayName = `NotBuilt(${id})`
  return NotBuilt
}
