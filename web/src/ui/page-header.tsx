// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

import type { ComponentChildren } from 'preact'
import { t } from '../i18n'
import { pageCode } from '../pages/registry'

/**
 * PageHeader is the top of every page in the shell: the page code from the prototype (A-01 …) with its
 * group and name, the title, a lead sentence and the page's actions.
 */
export function PageHeader(props: {
  page: string
  title: string
  lead?: string
  actions?: ComponentChildren
}) {
  const code = pageCode(props.page)
  return (
    <div class="ph">
      <div>
        {code && (
          <div class="eyebrow">
            <b>
              {code.letter}
              <i>-</i>
              {code.number}
            </b>
            <span class="tag">
              {t(code.group)} / {t(code.label)}
            </span>
          </div>
        )}
        <h1>{props.title}</h1>
        {props.lead && <p class="lead">{props.lead}</p>}
      </div>
      {props.actions && <div class="acts">{props.actions}</div>}
    </div>
  )
}
