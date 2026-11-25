// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Names and badges for what a managed resource carries: policy, origin, scope, status and check counts.

import type { Schemas } from '../../api/client'
import { t } from '../../i18n'
import { fmtNumber } from '../../i18n/format'

type Resource = Schemas['ResourceSummary']

export function originName(o: Schemas['Origin']): string {
  switch (o) {
    case 'repo':
      return t('resources.origin.repo')
    case 'upload':
      return t('resources.origin.upload')
    case 'web':
      return t('resources.origin.web')
  }
}

export function ChecksBadge({ checks }: { checks: Schemas['CheckCounts'] }) {
  if (checks.blocking > 0) {
    return <span class="badge bad">{t('resources.checks.blocking', { count: checks.blocking })}</span>
  }
  if (checks.advisory > 0) {
    return <span class="badge warn">{t('resources.checks.advisory', { count: checks.advisory })}</span>
  }
  return <span class="badge ok">{t('resources.checks.ok')}</span>
}

export function StatusBadges({ r }: { r: Resource }) {
  if (r.status === 'disabled') {
    return (
      <span class="badge">
        <span class="dot" />
        {t('resources.status.disabled')}
      </span>
    )
  }
  if (r.status === 'draft' || r.version === undefined) {
    return <span class="badge">{t('resources.status.draft')}</span>
  }
  if (r.has_draft) {
    return (
      <>
        <span class="badge ok">{t('resources.status.version', { version: fmtNumber(r.version) })}</span>
        <span class="badge warn">{t('resources.status.pending')}</span>
      </>
    )
  }
  return <span class="badge ok">{t('resources.status.published', { version: fmtNumber(r.version) })}</span>
}

/** The status filter of the list, as in the prototype; each maps to a test on the resource. */
export const STATUS_FILTERS = ['all', 'draft', 'pending', 'published', 'blocked', 'disabled'] as const
export type StatusFilter = (typeof STATUS_FILTERS)[number]

export function statusFilterName(f: StatusFilter): string {
  switch (f) {
    case 'all':
      return t('resources.filter.all_status')
    case 'draft':
      return t('resources.filter.draft')
    case 'pending':
      return t('resources.filter.pending')
    case 'published':
      return t('resources.filter.published')
    case 'blocked':
      return t('resources.filter.blocked')
    case 'disabled':
      return t('resources.filter.disabled')
  }
}

export function matchesStatus(r: Resource, f: StatusFilter): boolean {
  switch (f) {
    case 'all':
      return true
    case 'draft':
      return r.status === 'draft'
    case 'pending':
      return r.status === 'published' && r.has_draft
    case 'published':
      return r.status === 'published'
    case 'blocked':
      return r.checks.blocking > 0
    case 'disabled':
      return r.status === 'disabled'
  }
}

/** The built-in kinds a resource can be created as in this milestone, in the prototype's order. */
export const WEB_KINDS: readonly string[] = ['skill', 'mcp', 'workflow', 'agent', 'knowledge']

/** DIRECTORY_KINDS are uploaded as an archive; the other built-in kinds are one document filled in on the web. */
export const DIRECTORY_KINDS: readonly string[] = ['skill', 'agent', 'workflow']

/** kindDescription is the one line of the kind picker: what the kind is for and how it comes in. */
export function kindDescription(kind: string): [string, string] {
  switch (kind) {
    case 'skill':
      return [t('resources.kind_desc.skill'), t('resources.kind_how.skill')]
    case 'mcp':
      return [t('resources.kind_desc.mcp'), t('resources.kind_how.mcp')]
    case 'workflow':
      return [t('resources.kind_desc.workflow'), t('resources.kind_how.workflow')]
    case 'agent':
      return [t('resources.kind_desc.agent'), t('resources.kind_how.agent')]
    case 'knowledge':
      return [t('resources.kind_desc.knowledge'), t('resources.kind_how.knowledge')]
    default:
      return ['', '']
  }
}
