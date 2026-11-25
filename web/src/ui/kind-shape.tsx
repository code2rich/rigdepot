// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The shape of each resource kind, as in the product site's legend: Skill a pointed hexagon, MCP a flat
// hexagon, workflow a bar, Agent a triangle, knowledge base an ellipse; anything else a square slot. Kinds
// are told apart by shape and name, never by colour alone.

import type { JSX } from 'preact'

const shapes: Record<string, JSX.Element> = {
  skill: <path d="M8 1.5 14 5v6l-6 3.5L2 11V5z" />,
  mcp: <path d="M4.5 2h7L15 8l-3.5 6h-7L1 8z" />,
  workflow: <rect x="1" y="5.5" width="14" height="5" />,
  agent: <path d="M8 1.5 15 14H1z" />,
  knowledge: <ellipse cx="8" cy="8" rx="7" ry="4.2" />,
}

export function KindShape({ kind }: { kind: string }) {
  return (
    <span class="kshape" data-k={kind}>
      <svg viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">
        {shapes[kind] ?? <rect x="2.5" y="2.5" width="11" height="11" />}
      </svg>
    </span>
  )
}
