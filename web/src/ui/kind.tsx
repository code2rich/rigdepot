// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Resource kinds as people see them: the coloured icon square (app.js kindIcon) and the kind's name. Kinds are
// an extension point; one the console does not know shows its id in a plain square.

import { t } from '../i18n'
import { Icon, type IconName } from './icons'

/** The built-in kinds, in the prototype's order. */
export const KINDS = ['skill', 'mcp', 'workflow', 'agent', 'knowledge'] as const

const icons: Record<string, IconName> = {
  skill: 'skill',
  mcp: 'mcp',
  workflow: 'flow',
  agent: 'agent',
  knowledge: 'book',
}

/** kindName is a kind's display name. */
export function kindName(kind: string): string {
  switch (kind) {
    case 'skill':
      return t('ui.kind.skill')
    case 'mcp':
      return t('ui.kind.mcp')
    case 'workflow':
      return t('ui.kind.workflow')
    case 'agent':
      return t('ui.kind.agent')
    case 'knowledge':
      return t('ui.kind.knowledge')
    default:
      return kind
  }
}

export function KindIcon({ kind }: { kind: string }) {
  return (
    <span class="kind" data-k={kind in icons ? kind : undefined} title={kindName(kind)}>
      <Icon name={icons[kind] ?? 'box'} />
    </span>
  )
}
