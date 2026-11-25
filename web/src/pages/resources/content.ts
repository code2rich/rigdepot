// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Sending a resource's content: an archive of its directory (skill, agent, workflow) or its one document
// (MCP server, knowledge pointer). Both become the resource's draft; publishing is a separate step.

import { ApiError, api, unwrap, type Schemas } from '../../api/client'
import { meta } from '../../shell/session'

/** tooBig says whether a file is over the server's limit for an upload. */
export function tooBig(file: File): boolean {
  const limit = meta.value?.limits.max_bundle_bytes
  return limit !== undefined && file.size > limit
}

/** uploadBundle sends an archive as the resource's new draft (or creates the resource with kind). */
export async function uploadBundle(
  code: string,
  file: File,
  opts: { draftEtag?: string; kind?: string },
): Promise<Schemas['Draft']> {
  return unwrap(
    await api.PUT('/api/v1/resources/{code}/draft/bundle', {
      params: {
        path: { code },
        query: opts.kind ? { kind: opts.kind } : {},
        header: opts.draftEtag ? { 'If-Match': opts.draftEtag } : {},
      },
      // The body is the file's bytes as they are.
      body: file as unknown as string,
      bodySerializer: (b: unknown) => b as BodyInit,
      headers: { 'Content-Type': 'application/octet-stream' },
    }),
  )
}

/** putDocument saves a resource's one document as its draft (or creates the resource). */
export async function putDocument(
  code: string,
  body: Schemas['PutDraftRequest'],
  draftEtag?: string,
): Promise<Schemas['Draft']> {
  return unwrap(
    await api.PUT('/api/v1/resources/{code}/draft', {
      params: { path: { code }, header: draftEtag ? { 'If-Match': draftEtag } : {} },
      body,
    }),
  )
}

/** parseDefinition reads the JSON people typed; a syntax error is reported, never sent. */
export function parseDefinition(text: string): Record<string, unknown> | SyntaxError {
  try {
    const v: unknown = JSON.parse(text)
    if (typeof v !== 'object' || v === null || Array.isArray(v)) return new SyntaxError('not an object')
    return v as Record<string, unknown>
  } catch (err) {
    return err instanceof SyntaxError ? err : new SyntaxError(String(err))
  }
}

/** A starting point for a new document, shaped like the contract's examples. */
export function templateFor(kind: string, code: string): Record<string, unknown> {
  const name = code || 'my-mcp'
  if (kind === 'mcp') {
    return {
      transport: 'http',
      url: `https://${name}.example.com/mcp`,
      headers: { Authorization: 'Bearer ${secret:MY_TOKEN}' },
      delivery: 'native',
    }
  }
  if (kind === 'knowledge') return { repo: 'group/project', dir: 'docs/rules' }
  return {}
}

/** isConflict tells a 412 (someone changed it meanwhile) from other failures. */
export function isStale(err: unknown): boolean {
  return err instanceof ApiError && err.status === 412
}
