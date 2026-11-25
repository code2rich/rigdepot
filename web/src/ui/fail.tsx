// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// What a failed call tells people. Pages branch on the problem's code for the failures they expect (a wrong
// password, a code that is taken) and leave the rest here: no connection, not allowed (403 is the page's to
// explain, hiding a button is not a permission), too many attempts, and anything else with the server's own
// text, which is already in the request's language.

import type { ComponentChildren } from 'preact'
import { ApiError } from '../api/client'
import { t } from '../i18n'

/** Texts a page has for the codes it expects, by problem code ("auth.invalid_credentials"). */
export type Known = Readonly<Record<string, string>>

/** failText is the sentence for a failed call: the page's own text for its code, else a general one. */
export function failText(err: unknown, known: Known = {}): string {
  if (!(err instanceof ApiError)) return t('ui.fail.unknown')
  const own = known[err.code]
  if (own) return own
  if (err.status === 0) return t('ui.fail.network')
  if (err.status === 403) return t('ui.fail.forbidden')
  if (err.status === 429) return t('ui.fail.rate_limited')
  // Someone changed it meanwhile: the page reloads it and says so.
  if (err.status === 412) return t('ui.fail.stale')
  if (err.problem?.detail) return err.problem.detail
  return t('ui.fail.server', { status: err.status })
}

/** isCode tells whether err is the API's problem with this code. */
export function isCode(err: unknown, code: string): boolean {
  return err instanceof ApiError && err.code === code
}

/** Fail is a failure in place: a red callout read out when it appears, with an optional way to retry; nothing when error is null. */
export function Fail(props: {
  error: unknown
  known?: Known
  retry?: () => void
  children?: ComponentChildren
}) {
  // Nothing failed (yet): nothing to say. Lets a form keep its error in state and always render <Fail>.
  if (props.error === null || props.error === undefined) return null
  return (
    <div class="callout bad" role="alert">
      <span class="grow">{failText(props.error, props.known)}</span>
      {props.children}
      {props.retry && (
        <button type="button" class="btn sm" onClick={props.retry}>
          {t('ui.fail.retry')}
        </button>
      )}
    </div>
  )
}
