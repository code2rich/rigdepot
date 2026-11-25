// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// A message with markup inside it: a command in monospace, a word in bold. The message keeps its
// placeholder ("run {{cmd}} on the server") so translators can move it; the page passes the element.

import type { ComponentChildren } from 'preact'
import { Fragment } from 'preact'
import { t, type MessageKey } from '../i18n'

const open = '\u0001'
const close = '\u0002'

/** rich renders a message whose placeholders named in parts are elements; other params are text. */
export function rich(
  key: MessageKey,
  parts: Readonly<Record<string, ComponentChildren>>,
  params: Readonly<Record<string, string | number>> = {},
): ComponentChildren {
  const marks = Object.fromEntries(Object.keys(parts).map((name) => [name, open + name + close]))
  const text = t(key, { ...params, ...marks })
  const pieces = text.split(new RegExp(`${open}([A-Za-z0-9_]+)${close}`))
  return pieces.map((piece, i) => <Fragment key={i}>{i % 2 === 1 ? parts[piece] : piece}</Fragment>)
}
