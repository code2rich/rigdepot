// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// A-01 工作台. A placeholder until task M1-33 builds the page from the prototype (V.home in app.js).

import { t } from '../../i18n'
import { Empty } from '../../ui/empty'
import { PageHeader } from '../../ui/page-header'

export default function Home() {
  return (
    <>
      <PageHeader page="home" title={t('nav.home')} lead={t('home.lead')} />
      <div class="card">
        <Empty title={t('page.not_built.title')} why={t('page.not_built.why')} />
      </div>
    </>
  )
}
