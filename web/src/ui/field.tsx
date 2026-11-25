// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// A labelled form field as in the prototype (fld() in app.js): the label, an optional note beside it, the
// input, a hint under it and the field's own problem. Hint and problem are tied to the input, so a screen
// reader reads them with it.

import type { ComponentChildren, InputHTMLAttributes, Ref } from 'preact'
import './field.css'

export interface FieldProps extends Omit<InputHTMLAttributes<HTMLInputElement>, 'label' | 'class' | 'ref'> {
  id: string
  label: string
  note?: string
  hint?: ComponentChildren
  error?: string | null
  mono?: boolean
  inputRef?: Ref<HTMLInputElement>
}

/** describedBy is the aria-describedby of a field with an id, given whether it has a hint and a problem. */
export function describedBy(id: string, hint: unknown, error: unknown): string | undefined {
  return [error ? `${id}-err` : '', hint ? `${id}-hint` : ''].filter(Boolean).join(' ') || undefined
}

/** FieldShell is the label, hint and problem around any control (a textarea, a select …) with that id. */
export function FieldShell(props: {
  id: string
  label: string
  note?: string
  hint?: ComponentChildren
  error?: string | null
  children: ComponentChildren
}) {
  return (
    <div>
      <label class="fl" for={props.id}>
        {props.label}
        {props.note && <span class="faint"> {props.note}</span>}
      </label>
      {props.children}
      {props.error && (
        <p class="hint err" id={`${props.id}-err`}>
          {props.error}
        </p>
      )}
      {props.hint && (
        <p class="hint" id={`${props.id}-hint`}>
          {props.hint}
        </p>
      )}
    </div>
  )
}

/** Field is a labelled single-line input. */
export function Field(props: FieldProps) {
  const { id, label, note, hint, error, mono, inputRef, ...input } = props
  return (
    <FieldShell id={id} label={label} note={note} hint={hint} error={error}>
      <input
        {...input}
        id={id}
        ref={inputRef}
        class={mono ? 'field mono' : 'field'}
        aria-invalid={error ? true : undefined}
        aria-describedby={describedBy(id, hint, error)}
      />
    </FieldShell>
  )
}
