// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Drawers and dialogs, as in the prototype (抽屉与对话框): a drawer slides in from the right over a scrim, a
// dialog sits in the middle. Both take the focus when they open, keep Tab inside, close on Escape or a click
// on the scrim, and give the focus back to where it was. Only the top one answers Escape when a dialog is
// open over a drawer.

import type { ComponentChildren, RefObject } from 'preact'
import { createPortal } from 'preact/compat'
import { useEffect, useRef } from 'preact/hooks'
import { t } from '../i18n'
import { Icon } from './icons'

const stack: object[] = []

const focusable =
  'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'

/** useOverlay gives an overlay the focus, traps Tab inside it and closes it on Escape. */
function useOverlay(box: RefObject<HTMLElement>, onClose: () => void) {
  const close = useRef(onClose)
  close.current = onClose
  useEffect(() => {
    const token = {}
    stack.push(token)
    const before = document.activeElement instanceof HTMLElement ? document.activeElement : null
    const el = box.current
    const first = el?.querySelector<HTMLElement>('[autofocus]') ?? el
    first?.focus()
    const onKey = (e: KeyboardEvent) => {
      if (stack.at(-1) !== token || !el) return
      if (e.key === 'Escape') {
        e.stopPropagation()
        close.current()
        return
      }
      if (e.key !== 'Tab') return
      const items = Array.from(el.querySelectorAll<HTMLElement>(focusable)).filter(
        (x) => x.offsetParent !== null,
      )
      const head = items[0]
      const tail = items.at(-1)
      if (!head || !tail) {
        e.preventDefault()
        return
      }
      if (e.shiftKey && (document.activeElement === head || document.activeElement === el)) {
        e.preventDefault()
        tail.focus()
      } else if (!e.shiftKey && document.activeElement === tail) {
        e.preventDefault()
        head.focus()
      }
    }
    document.addEventListener('keydown', onKey, true)
    return () => {
      document.removeEventListener('keydown', onKey, true)
      stack.splice(stack.indexOf(token), 1)
      if (before?.isConnected) before.focus()
    }
  }, [box])
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
 * Drawer is the panel on the right: head (title, subtitle, close), optional tabs, a scrolling body and a
 * foot with the actions.
 */
export function Drawer(props: {
  label: string
  onClose: () => void
  head: ComponentChildren
  tabs?: ComponentChildren
  foot?: ComponentChildren
  wide?: boolean
  children: ComponentChildren
}) {
  const box = useRef<HTMLDivElement>(null)
  useOverlay(box, props.onClose)
  // Rendered into <body>: the page's entrance animation transforms the page, and a transformed ancestor
  // would pin a fixed overlay to the page instead of the window.
  return createPortal(
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
        {props.tabs && <div class="d-tabs">{props.tabs}</div>}
        <div class="d-body">{props.children}</div>
        {props.foot && <div class="d-foot">{props.foot}</div>}
      </div>
    </div>,
    document.body,
  )
}

/** Modal is a dialog in the middle of the screen with a title, a body and its buttons. */
export function Modal(props: {
  title: string
  onClose: () => void
  foot: ComponentChildren
  wide?: boolean
  children: ComponentChildren
}) {
  const box = useRef<HTMLDivElement>(null)
  useOverlay(box, props.onClose)
  return createPortal(
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
        aria-label={props.title}
        tabIndex={-1}
        ref={box}
      >
        <div class="m-head">
          <h2>{props.title}</h2>
          <CloseButton onClick={props.onClose} />
        </div>
        <div class="m-body">{props.children}</div>
        <div class="m-foot">{props.foot}</div>
      </div>
    </div>,
    document.body,
  )
}
