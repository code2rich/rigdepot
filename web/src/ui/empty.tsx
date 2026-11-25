// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

import type { ComponentChildren } from 'preact'

/**
 * Empty is the empty state: an empty slot waiting for its first item. It always says why the place is empty
 * and what to do next; there is no default "no data". Every list passes its own.
 */
export function Empty(props: { title: string; why: string; children?: ComponentChildren }) {
  return (
    <div class="empty">
      <div class="em-art" aria-hidden="true">
        <svg viewBox="0 0 92 64">
          <rect class="slot" x="1" y="17" width="20" height="20" />
          <rect class="slot" x="25" y="17" width="20" height="20" />
          <rect class="slot dash" x="49" y="17" width="20" height="20" />
          <rect class="slot" x="1" y="41" width="20" height="20" />
          <rect class="slot" x="25" y="41" width="20" height="20" />
          <rect class="slot" x="49" y="41" width="20" height="20" />
          <rect class="lit" x="53" y="21" width="12" height="12" />
          <path class="slot" d="M73 61h18M73 37h10" />
        </svg>
      </div>
      <div class="em-t">{props.title}</div>
      <div>{props.why}</div>
      {props.children && <div class="row">{props.children}</div>}
    </div>
  )
}
