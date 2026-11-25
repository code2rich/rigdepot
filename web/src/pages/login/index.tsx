// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The sign-in page. A placeholder until task M1-30 builds it from the prototype (V.login in app.js).

import { t } from '../../i18n'
import { Lockup } from '../../ui/brand'
import { Empty } from '../../ui/empty'

export default function Login() {
  return (
    <div class="center-page">
      <div class="card center-card">
        <div class="cc-head">
          <Lockup />
          <span class="tag">{location.host}</span>
        </div>
        <div class="cc-body">
          <h1>{t('login.title')}</h1>
          <Empty title={t('page.not_built.title')} why={t('login.not_built')} />
        </div>
      </div>
    </div>
  )
}
