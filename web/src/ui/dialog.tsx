// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// A dialog as in the prototype (dialog() in app.js): a title with a close button, the body, and the
// buttons at the foot. Focus moves into it when it opens and stays inside while it is open; Esc or a click
// outside closes it, and focus goes back to where it was.

import type { ComponentChildren } from 'preact'
import { createPortal } from 'preact/compat'
import { useEffect, useRef } from 'preact/hooks'
import { t } from '../i18n'
import { Icon } from './icons'

const focusable =
  'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'

export function Dialog(props: {
  title: string
  onClose: () => void
  children: ComponentChildren
  foot: ComponentChildren
  wide?: boolean
}) {
  const box = useRef<HTMLDivElement>(null)
  const close = useRef(props.onClose)
  close.current = props.onClose

  useEffect(() => {
    const before = document.activeElement instanceof HTMLElement ? document.activeElement : null
    const el = box.current
    const first = el?.querySelector<HTMLElement>(`.m-body :is(${focusable})`) ?? el
    first?.focus()
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        close.current()
        return
      }
      if (e.key !== 'Tab' || !el) return
      const items = [...el.querySelectorAll<HTMLElement>(focusable)]
      const head = items[0]
      const tail = items[items.length - 1]
      if (!head || !tail) return
      if (e.shiftKey && document.activeElement === head) {
        e.preventDefault()
        tail.focus()
      } else if (!e.shiftKey && document.activeElement === tail) {
        e.preventDefault()
        head.focus()
      }
    }
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('keydown', onKey)
      before?.focus()
    }
  }, [])

  // Rendered at the end of <body>, above the shell and its feedback button whatever their stacking.
  return createPortal(
    <div
      class="modal-wrap enter"
      onClick={(e) => {
        if (e.target === e.currentTarget) props.onClose()
      }}
    >
      <div
        ref={box}
        class={props.wide ? 'modal wide' : 'modal'}
        role="dialog"
        aria-modal="true"
        aria-labelledby="m-title"
        tabIndex={-1}
      >
        <div class="m-head">
          <h2 id="m-title">{props.title}</h2>
          <button
            type="button"
            class="btn ghost icon"
            onClick={props.onClose}
            aria-label={t('ui.dialog.close')}
            title={t('ui.dialog.close')}
          >
            <Icon name="x" />
          </button>
        </div>
        <div class="m-body">{props.children}</div>
        <div class="m-foot">{props.foot}</div>
      </div>
    </div>,
    document.body,
  )
}
