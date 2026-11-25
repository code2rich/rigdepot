// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Numbers and times for people, in the console's language and the browser's time zone. The API always
// carries machine formats (RFC 3339 in UTC, plain numbers); formatting happens only here. No date library.

import { locale } from './index'

export function fmtNumber(n: number, options?: Intl.NumberFormatOptions): string {
  return new Intl.NumberFormat(locale.value, options).format(n)
}

export function fmtDateTime(
  iso: string | Date,
  options: Intl.DateTimeFormatOptions = { dateStyle: 'medium', timeStyle: 'short' },
): string {
  return new Intl.DateTimeFormat(locale.value, options).format(typeof iso === 'string' ? new Date(iso) : iso)
}

const units: [Intl.RelativeTimeFormatUnit, number][] = [
  ['year', 365 * 24 * 3600],
  ['month', 30 * 24 * 3600],
  ['week', 7 * 24 * 3600],
  ['day', 24 * 3600],
  ['hour', 3600],
  ['minute', 60],
  ['second', 1],
]

/** fmtRelative says how long ago (or from now) a time is: "6 分钟前", "in 2 days". */
export function fmtRelative(iso: string | Date, now: Date = new Date()): string {
  const then = typeof iso === 'string' ? new Date(iso) : iso
  const seconds = Math.round((then.getTime() - now.getTime()) / 1000)
  const rtf = new Intl.RelativeTimeFormat(locale.value, { numeric: 'auto' })
  for (const [unit, size] of units) {
    if (Math.abs(seconds) >= size || unit === 'second') return rtf.format(Math.round(seconds / size), unit)
  }
  return rtf.format(0, 'second')
}
