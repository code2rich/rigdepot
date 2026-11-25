// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The card in the middle of the page that the pages outside the shell use (apply, setup, invitation,
// device confirmation), as in the prototype: the logo on the left of the head, a tag on the right, the
// body, and the buttons at the foot.

import type { ComponentChildren } from 'preact'
import { Lockup } from './brand'

export function CenterCard(props: {
  tag?: ComponentChildren
  /** Beside the logo, such as "· Setup". */
  after?: ComponentChildren
  /** An extra class on the card: its width. */
  class?: string
  children: ComponentChildren
  foot?: ComponentChildren
}) {
  return (
    <div class="center-page">
      <main class={props.class ? `card center-card ${props.class}` : 'card center-card'}>
        <div class="cc-head">
          <span class="row" style={{ flexWrap: 'nowrap' }}>
            <Lockup />
            {props.after}
          </span>
          {props.tag && <span class="tag">{props.tag}</span>}
        </div>
        {props.children}
        {props.foot && <div class="cc-foot">{props.foot}</div>}
      </main>
    </div>
  )
}
