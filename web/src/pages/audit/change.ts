// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The "改了什么" column of the audit log. An entry's change is a small summary object chosen by whoever wrote
// the entry (never content, never secrets): field names with a value, or with { from, to }. It is shown as
// it is, one field after another; a secret that was changed reads "replaced".

/** changeText summarises a change: "version: 8 → 9 · files_changed: 1"; nothing is "—". */
export function changeText(change: Readonly<Record<string, unknown>>, replaced: string): string {
  const show = (v: unknown): string => {
    if (v === 'replaced') return replaced
    if (v === null || v === undefined || v === '') return '—'
    if (Array.isArray(v)) return v.length ? v.map(show).join(', ') : '—'
    if (typeof v === 'object') return JSON.stringify(v)
    return String(v)
  }
  const parts = Object.entries(change).map(([k, v]) =>
    isFromTo(v) ? `${k}: ${show(v.from)} → ${show(v.to)}` : `${k}: ${show(v)}`,
  )
  return parts.length ? parts.join(' · ') : '—'
}

function isFromTo(v: unknown): v is { from: unknown; to: unknown } {
  return (
    typeof v === 'object' && v !== null && !Array.isArray(v) && Object.keys(v).sort().join() === 'from,to'
  )
}
