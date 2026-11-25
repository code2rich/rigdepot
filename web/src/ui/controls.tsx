// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Small controls from the prototype: the segmented choice (segBtn), the square switch and the toggle chip
// (chip). The sentence for the AI tool is AiTip in say.tsx.

import type { ComponentChildren } from 'preact'

/** Seg is a choice of one among a few, as buttons; `lined` is the tab style. */
export function Seg<V extends string>(props: {
  value: V
  items: readonly (readonly [V, ComponentChildren])[]
  onChange: (v: V) => void
  lined?: boolean
  label?: string
}) {
  return (
    <div class={props.lined ? 'seg lined' : 'seg'} role="group" aria-label={props.label}>
      {props.items.map(([v, label]) => (
        <button
          key={v}
          type="button"
          class={props.value === v ? 'on' : undefined}
          aria-pressed={props.value === v}
          onClick={() => props.onChange(v)}
        >
          {label}
        </button>
      ))}
    </div>
  )
}

/** Switch is an on/off switch. It says what it switches in `label`. */
export function Switch(props: {
  on: boolean
  label: string
  onChange: (on: boolean) => void
  disabled?: boolean
}) {
  return (
    <button
      type="button"
      class={props.on ? 'switch on' : 'switch'}
      role="switch"
      aria-checked={props.on}
      aria-label={props.label}
      disabled={props.disabled}
      onClick={() => props.onChange(!props.on)}
    />
  )
}

/** Chip is a toggle among several that can be on together (stages), or one of a few (scope). */
export function Chip(props: { on: boolean; onClick: () => void; children: ComponentChildren }) {
  return (
    <button
      type="button"
      class={props.on ? 'badge chip brand' : 'badge chip'}
      aria-pressed={props.on}
      onClick={props.onClick}
    >
      {props.children}
    </button>
  )
}
