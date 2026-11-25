// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Small controls from the prototype: the segmented choice (segBtn), the square switch, the toggle chip and the
// AI tip ("让 AI 来做") with its copy button.

import type { ComponentChildren } from 'preact'
import { t } from '../i18n'
import { Icon } from './icons'
import { toast } from './toast'

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

/** copyText puts text on the clipboard and says so. */
export async function copyText(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    toast(t('ui.copied'))
  } catch {
    toast(t('ui.copy_failed'))
  }
}

/** AiTip is the line that says what to ask an AI tool to do instead of clicking through the page. */
export function AiTip(props: { say: string; note?: string }) {
  return (
    <div class="aitip">
      <span class="lab">
        <Icon name="spark" />
        {t('ui.aitip.label')}
      </span>
      <span class="say">
        {t('ui.aitip.quote', { say: props.say })}
        {props.note && <span class="note">{props.note}</span>}
      </span>
      <button
        type="button"
        class="btn ghost icon"
        onClick={() => void copyText(props.say)}
        aria-label={t('ui.aitip.copy')}
        title={t('ui.aitip.copy')}
      >
        <Icon name="copy" />
      </button>
    </div>
  )
}
