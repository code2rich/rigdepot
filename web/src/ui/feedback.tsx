// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// How feedback is labelled wherever it appears: its number (FB-41), status badge and type.

import type { Schemas } from '../api/client'
import { t, type MessageKey } from '../i18n'

const STATUS: Record<Schemas['FeedbackStatus'], [MessageKey, string]> = {
  new: ['feedback.status.new', 'info'],
  accepted: ['feedback.status.accepted', 'warn'],
  in_progress: ['feedback.status.in_progress', 'warn'],
  resolved: ['feedback.status.resolved', 'ok'],
  wont_fix: ['feedback.status.wont_fix', ''],
}

export const STATUSES = Object.keys(STATUS) as Schemas['FeedbackStatus'][]

export function statusName(s: Schemas['FeedbackStatus']): string {
  return t(STATUS[s][0])
}

export function StatusBadge(props: { status: Schemas['FeedbackStatus'] }) {
  return (
    <span class={`badge ${STATUS[props.status][1]}`.trim()}>
      <span class="dot" />
      {statusName(props.status)}
    </span>
  )
}

const TYPE: Record<Schemas['FeedbackType'], MessageKey> = {
  bug: 'feedback.type.bug',
  suggestion: 'feedback.type.suggestion',
  question: 'feedback.type.question',
  friction: 'feedback.type.friction',
}

export function typeName(type: Schemas['FeedbackType']): string {
  return t(TYPE[type])
}

const ORIGIN: Record<Schemas['FeedbackOrigin'], MessageKey> = {
  web: 'feedback.origin.web',
  ai: 'feedback.origin.ai',
  cli: 'feedback.origin.cli',
  system: 'feedback.origin.system',
}

export function originName(origin: Schemas['FeedbackOrigin']): string {
  return t(ORIGIN[origin])
}

/** fbNumber is how feedback is referred to: FB-41. */
export function fbNumber(n: number): string {
  return t('feedback.number', { number: n })
}
