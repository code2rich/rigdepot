// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Things to copy: a command line, a sentence to send to the AI tool, a file's content. From the prototype
// (cmd, aiTip, selfTip, code-box). Copying confirms with a toast.

import type { ComponentChildren } from 'preact'
import { t } from '../i18n'
import { Icon } from './icons'
import { toast } from './toast'

/** copy puts text on the clipboard and says so. */
export function copy(text: string) {
  const done = () => toast(t('ui.copied'))
  try {
    navigator.clipboard.writeText(text).then(done, () => toast(t('ui.copy_failed')))
  } catch {
    toast(t('ui.copy_failed'))
  }
}

/** CopyButton is the small copy icon next to something copyable. */
export function CopyButton(props: { text: string; label?: string; plain?: boolean }) {
  const label = props.label ?? t('ui.copy')
  return (
    <button
      type="button"
      class={props.plain ? undefined : 'btn ghost icon'}
      onClick={() => copy(props.text)}
      aria-label={label}
      title={label}
    >
      <Icon name="copy" />
    </button>
  )
}

/** Cmd is one command line with a copy button. */
export function Cmd(props: { text: string }) {
  return (
    <div class="cmd">
      <code>{props.text}</code>
      <CopyButton text={props.text} plain />
    </div>
  )
}

/** AiTip is a sentence to send to one's AI tool ("说一句"), with a label and an optional note. */
export function AiTip(props: { say: string; note?: string; label?: string }) {
  return (
    <div class="aitip">
      <span class="lab">
        <Icon name="spark" />
        {props.label ?? t('ui.aitip.label')}
      </span>
      <span class="say">
        {t('ui.aitip.quote', { text: props.say })}
        {props.note && <span class="note">{props.note}</span>}
      </span>
      <CopyButton text={props.say} label={t('ui.aitip.copy')} />
    </div>
  )
}

/** SelfTip is the amber "要你本人" box: something only the person may do, never the AI. */
export function SelfTip(props: { children: ComponentChildren }) {
  return (
    <div class="aitip self">
      <span class="lab">
        <Icon name="user" />
        {t('ui.selftip.label')}
      </span>
      <span class="say">{props.children}</span>
    </div>
  )
}

/** CodeBox is a block of text (a file, a configuration) with a copy button in the corner. */
export function CodeBox(props: { text: string; label?: string }) {
  return (
    <div class="code-box">
      <pre class="code" tabIndex={0} aria-label={props.label}>
        {props.text}
      </pre>
      <CopyButton text={props.text} />
    </div>
  )
}

/** StatLi is a line of a status list: a label, an optional coloured square, a value. */
export function StatLi(props: {
  label: string
  tone?: 'ok' | 'warn' | 'bad' | 'off'
  children: ComponentChildren
}) {
  return (
    <div class="stat-li">
      <span>{props.label}</span>
      {props.tone && <span class={`sq ${props.tone}`} />}
      <span class={props.tone === 'off' ? 'faint' : undefined}>{props.children}</span>
    </div>
  )
}
