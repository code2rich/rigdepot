// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// What to tell people when a call fails. A 403 says that the page needs permissions they do not have (hiding
// a button is not a permission: the server decides), a failed connection says to check the network, anything
// else shows the server's own text with the request id that finds it in the log.

import { ApiError } from '../api/client'
import { t } from '../i18n'
import { Empty } from './empty'

/** errorText is one sentence about a failed call, for next to the thing that failed. */
export function errorText(err: unknown): string {
  if (!(err instanceof ApiError)) return t('ui.error.unexpected')
  if (err.status === 0) return t('ui.error.network')
  if (err.status === 403) return t('ui.error.denied')
  if (err.status === 412) return t('ui.error.stale')
  const detail = err.problem?.detail
  const id = err.problem?.request_id
  if (detail && id) return t('ui.error.server_with_id', { detail, id })
  if (detail) return detail
  return id ? t('ui.error.server_id', { id }) : t('ui.error.server')
}

/** LoadFailed takes the place of a page or a card whose data could not be loaded. */
export function LoadFailed(props: { error: unknown; retry?: () => void }) {
  const denied = props.error instanceof ApiError && props.error.status === 403
  return (
    <Empty title={denied ? t('ui.load.denied') : t('ui.load.failed')} why={errorText(props.error)}>
      {props.retry && !denied && (
        <button type="button" class="btn" onClick={props.retry}>
          {t('ui.load.retry')}
        </button>
      )}
    </Empty>
  )
}

/** ErrorNote is a failure inside a form or a dialog. */
export function ErrorNote(props: { error: unknown }) {
  if (props.error === undefined || props.error === null) return null
  return (
    <div class="callout bad" role="alert">
      <span>{errorText(props.error)}</span>
    </div>
  )
}
