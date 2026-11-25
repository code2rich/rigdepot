// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// How a resource is shown wherever it appears: its kind (icon block, shape and name), its policy, its scope,
// its stages. From the prototype (kindIcon, ktag, policyBadge, pName, stageBadges). Kinds are an extension
// point: an unknown kind gets a plain box and its id as the name.

import { signal } from '@preact/signals'
import { useEffect } from 'preact/hooks'
import { api, unwrap, type Schemas } from '../api/client'
import { BRAND } from '../brand.gen'
import { t, type MessageKey } from '../i18n'
import { me } from '../shell/session'
import { Icon, type IconName } from './icons'
import { KindShape } from './kind-shape'

const KIND_ICON: Record<string, IconName> = {
  skill: 'skill',
  mcp: 'mcp',
  workflow: 'flow',
  agent: 'agent',
  knowledge: 'book',
  database: 'db',
  server: 'server',
  env: 'globe',
  extension: 'ext',
}

const KIND_NAME: Record<string, MessageKey> = {
  skill: 'res.kind.skill',
  mcp: 'res.kind.mcp',
  workflow: 'res.kind.workflow',
  agent: 'res.kind.agent',
  knowledge: 'res.kind.knowledge',
  database: 'res.kind.database',
  server: 'res.kind.server',
  env: 'res.kind.env',
  extension: 'res.kind.extension',
}

/** The built-in kinds in the order the prototype lists them. */
export const KINDS = Object.keys(KIND_NAME)

export function kindName(kind: string): string {
  const key = KIND_NAME[kind]
  return key ? t(key) : kind
}

/** KindIcon is the square icon block in the kind's colour. */
export function KindIcon(props: { kind: string }) {
  const known = props.kind in KIND_ICON
  return (
    <span class="kind" data-k={known ? props.kind : undefined} title={kindName(props.kind)}>
      <Icon name={KIND_ICON[props.kind] ?? 'box'} />
    </span>
  )
}

/** KindTag is the kind's shape and name in small capitals, optionally with the version. */
export function KindTag(props: { kind: string; version?: number }) {
  return (
    <span class="ktag" data-k={props.kind}>
      <KindShape kind={props.kind} />
      {kindName(props.kind)}
      {props.version !== undefined && (
        <span class="v">{t('res.version_dot', { version: props.version })}</span>
      )}
    </span>
  )
}

const POLICY: Record<Schemas['Policy'], [MessageKey, MessageKey, string]> = {
  required: ['res.policy.required', 'res.policy.required.what', 'brand'],
  recommended: ['res.policy.recommended', 'res.policy.recommended.what', ''],
  optional: ['res.policy.optional', 'res.policy.optional.what', ''],
}

export function policyName(policy: Schemas['Policy']): string {
  return t(POLICY[policy][0])
}

/** policyWhat says what the policy means for the person ("默认安装，个人可以停用"). */
export function policyWhat(policy: Schemas['Policy']): string {
  return t(POLICY[policy][1])
}

export function PolicyBadge(props: { policy: Schemas['Policy'] }) {
  return <span class={`badge ${POLICY[props.policy][2]}`.trim()}>{policyName(props.policy)}</span>
}

/** scopeName is who a resource is for: the whole organisation, only its author, or a product by name. */
export function scopeName(scope: string): string {
  if (scope === 'org') return t('res.scope.org')
  if (scope === 'me') return t('res.scope.me')
  return me.value?.memberships.find((m) => m.product.code === scope)?.product.name ?? scope
}

/** slashName is how a workflow is called in an AI tool: /rgd-<code>. */
export function slashName(code: string): string {
  return '/' + BRAND.prefix + code
}

/** codeLabel is what is shown next to a resource's name: its slash command for workflows, else its code. */
export function codeLabel(r: { kind: string; code: string }): string {
  return r.kind === 'workflow' ? slashName(r.code) : r.code
}

// The stages of the delivery process, loaded once (they change only when an administrator edits them).
const stageList = signal<Schemas['Stage'][] | null>(null)
let stagesLoading: Promise<void> | undefined

function loadStages() {
  stagesLoading ??= api
    .GET('/api/v1/stages')
    .then(unwrap)
    .then(
      (res) => {
        stageList.value = res.items
      },
      () => {
        stagesLoading = undefined
      },
    )
}

/** useStages returns the stages (an empty list until they are loaded). */
export function useStages(): Schemas['Stage'][] {
  useEffect(loadStages, [])
  return stageList.value ?? []
}

/** stageName is a stage's translated name; its id until the stages are loaded. */
export function stageName(id: string): string {
  return stageList.value?.find((s) => s.id === id)?.name ?? id
}

export function StageBadges(props: { stages: readonly string[] }) {
  if (props.stages.length === 0) return <span class="faint">—</span>
  return (
    <>
      {props.stages.map((s) => (
        <span key={s} class="badge">
          {stageName(s)}
        </span>
      ))}
    </>
  )
}

const TOOL_NAMES: Record<string, string> = {
  'claude-code': 'Claude Code',
  codex: 'Codex',
  opencode: 'OpenCode',
  gemini: 'Gemini CLI',
}

/** toolName is an AI tool's own name for an adapter id (not translated); the id when it is not known here. */
export function toolName(id: string): string {
  return TOOL_NAMES[id] ?? id
}
