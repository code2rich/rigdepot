// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Reading what the server says a sign-in method needs (config_schema, a JSON Schema) into form fields, and
// naming a resource repository after its address.

export interface ConfigField {
  name: string
  required: boolean
  /** The value is an address (format: uri). */
  uri: boolean
}

/** configFields lists the string settings of a sign-in method's config_schema, in the schema's order. */
export function configFields(schema: Readonly<Record<string, unknown>>): ConfigField[] {
  const props = schema.properties
  if (typeof props !== 'object' || props === null) return []
  const required = Array.isArray(schema.required) ? schema.required.filter((x) => typeof x === 'string') : []
  return Object.entries(props as Record<string, unknown>)
    .filter(([, p]) => typeof p === 'object' && p !== null && (p as { type?: unknown }).type === 'string')
    .map(([name, p]) => ({
      name,
      required: required.includes(name),
      uri: (p as { format?: unknown }).format === 'uri',
    }))
}

/** repoName is a repository's name from its address: the last path segment without ".git". */
export function repoName(url: string): string {
  const last = url.trim().replace(/\/+$/, '').split(/[/:]/).pop() ?? ''
  const name = last.replace(/\.git$/, '')
  return (name || 'resources').slice(0, 64)
}
