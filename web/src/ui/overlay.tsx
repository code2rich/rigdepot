// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Drawers and dialogs, as in the prototype (抽屉与对话框): a scrim, the box, Esc and a click outside close
// it. Keyboard behaviour is built in so pages do not each redo it: when one opens, focus moves to its first
// field (or the box itself, so a screen reader reads the title); Tab stays inside; when it closes, focus
// returns to whatever opened it.

import type { ComponentChildren, RefObject } from 'preact'
import { useEffect, useRef } from 'preact/hooks'
import { t } from '../i18n'
import { Icon } from './icons'

const focusable =
  'a[href],button:not([disabled]),input:not([type=hidden]),select,textarea,[tabindex]:not([tabindex="-1"])'

/** useOverlay gives an open drawer or dialog its keyboard behaviour. */
function useOverlay(box: RefObject<HTMLDivElement>, onClose: () => void, firstField: string) {
  const close = useRef(onClose)
  close.current = onClose
  useEffect(() => {
    const opener = document.activeElement as HTMLElement | null
    const el = box.current
    if (el) {
      const field = el.querySelector<HTMLElement>(firstField)
      ;(field ?? el).focus({ preventScroll: true })
    }
    const onKey = (e: KeyboardEvent) => {
      if (!el) return
      // Only the topmost overlay answers.
      const all = document.querySelectorAll('.drawer, .modal')
      if (all[all.length - 1] !== el) return
      if (e.key === 'Escape') {
        e.preventDefault()
        e.stopPropagation()
        close.current()
      } else if (e.key === 'Tab') {
        const items = Array.from(el.querySelectorAll<HTMLElement>(focusable)).filter(
          (n) => n.offsetParent !== null,
        )
        const first = items[0]
        const last = items[items.length - 1]
        if (!first || !last) return
        if (e.shiftKey && (document.activeElement === first || document.activeElement === el)) {
          e.preventDefault()
          last.focus()
        } else if (!e.shiftKey && document.activeElement === last) {
          e.preventDefault()
          first.focus()
        }
      }
    }
    document.addEventListener('keydown', onKey, true)
    return () => {
      document.removeEventListener('keydown', onKey, true)
      if (opener && opener.isConnected) opener.focus({ preventScroll: true })
    }
  }, [box, firstField])
}

/** CloseButton is the × in the corner of a drawer or dialog. */
export function CloseButton(props: { onClick: () => void }) {
  return (
    <button
      type="button"
      class="btn ghost icon"
      onClick={props.onClick}
      aria-label={t('ui.close')}
      title={t('ui.close')}
    >
      <Icon name="x" />
    </button>
  )
}

/**
 * Drawer slides in from the right. `head` is the content of the header next to the close button; the body
 * scrolls; `foot` (optional) is the row of buttons at the bottom.
 */
export function Drawer(props: {
  label: string
  onClose: () => void
  head: ComponentChildren
  children: ComponentChildren
  foot?: ComponentChildren
  wide?: boolean
}) {
  const box = useRef<HTMLDivElement>(null)
  useOverlay(box, props.onClose, '.d-body input:not([type=checkbox]), .d-body textarea')
  return (
    <div
      class="scrim enter"
      onClick={(e) => {
        if (e.target === e.currentTarget) props.onClose()
      }}
    >
      <div
        class={props.wide ? 'drawer wide' : 'drawer'}
        role="dialog"
        aria-modal="true"
        aria-label={props.label}
        tabIndex={-1}
        ref={box}
      >
        <div class="d-head">
          {props.head}
          <CloseButton onClick={props.onClose} />
        </div>
        <div class="d-body">{props.children}</div>
        {props.foot && <div class="d-foot">{props.foot}</div>}
      </div>
    </div>
  )
}

/** Modal is a dialog in the middle of the screen: title, body, and a row of buttons. */
export function Modal(props: {
  title: ComponentChildren
  onClose: () => void
  children: ComponentChildren
  foot?: ComponentChildren
  wide?: boolean
  titleClass?: string
}) {
  const box = useRef<HTMLDivElement>(null)
  useOverlay(box, props.onClose, '.m-body input:not([type=checkbox]):not([tabindex="-1"]), .m-body textarea')
  return (
    <div
      class="modal-wrap enter"
      onClick={(e) => {
        if (e.target === e.currentTarget) props.onClose()
      }}
    >
      <div
        class={props.wide ? 'modal wide' : 'modal'}
        role="dialog"
        aria-modal="true"
        aria-labelledby="m-title"
        tabIndex={-1}
        ref={box}
      >
        <div class="m-head">
          <h2 id="m-title" class={props.titleClass}>
            {props.title}
          </h2>
          <CloseButton onClick={props.onClose} />
        </div>
        <div class="m-body">{props.children}</div>
        {props.foot && <div class="m-foot">{props.foot}</div>}
      </div>
    </div>
  )
}
