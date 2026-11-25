// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

import { describe, expect, it } from 'vitest'
import { configFields, repoName } from './config'

describe('configFields', () => {
  it('reads the string settings with whether they are needed', () => {
    const schema = {
      type: 'object',
      required: ['issuer', 'client_id'],
      properties: {
        issuer: { type: 'string', format: 'uri' },
        client_id: { type: 'string' },
        scopes: { type: 'array' },
        audience: { type: 'string' },
      },
    }
    expect(configFields(schema)).toEqual([
      { name: 'issuer', required: true, uri: true },
      { name: 'client_id', required: true, uri: false },
      { name: 'audience', required: false, uri: false },
    ])
  })
  it('copes with a schema without properties', () => {
    expect(configFields({})).toEqual([])
  })
})

describe('repoName', () => {
  it('takes the last segment of the address', () => {
    expect(repoName('https://gitlab.example.com/platform/ai-resources.git')).toBe('ai-resources')
    expect(repoName('git@gitlab.example.com:platform/tools.git')).toBe('tools')
    expect(repoName('ssh://git@example.com/x/y/')).toBe('y')
    expect(repoName('')).toBe('resources')
  })
})
