// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The one way the console talks to the server: openapi-fetch over the types generated from
// api/openapi.yaml (schema.gen.ts, go run ./tools/dev gen). A change to the contract that a page does not
// follow fails the type check.
//
//   const { data, error } = await api.GET('/api/v1/me')
//   const me = unwrap(await api.GET('/api/v1/me'))   // throws ApiError instead
//
// Every request carries the console's language (Accept-Language), so the server's error texts match the
// page. A 401 on any request means the session is gone: the handler set with onSignedOut runs (the app
// sends the person to the sign-in page). Errors are normalised to ApiError; a 403 is the page's to show
// (hiding a button is not a permission, the server decides).

import createClient, { type Middleware } from 'openapi-fetch'
import { locale } from '../i18n'
import { basePath } from '../router/paths'
import type { components, paths } from './schema.gen'

export type Schemas = components['schemas']
export type Problem = Schemas['Problem']

/** ApiError is a failed call: the HTTP status (0 when the server could not be reached) and the problem document. */
export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly problem: Problem | undefined

  constructor(status: number, problem?: Problem, cause?: unknown) {
    super(problem?.detail ?? problem?.title ?? (status === 0 ? 'network error' : `HTTP ${status}`), { cause })
    this.name = 'ApiError'
    this.status = status
    this.problem = problem
    this.code = problem?.code ?? (status === 0 ? 'network' : `http.${status}`)
  }
}

type Transport = (request: Request) => Promise<Response>

let transport: Transport = (request) => globalThis.fetch(request)

/** setTransport replaces how requests are sent. Only tests and the development mock (mock.ts) use it. */
export function setTransport(next: Transport) {
  transport = next
}

let signedOut: () => void = () => {}

/** onSignedOut sets what happens when the server says the session is gone (401). */
export function onSignedOut(handler: () => void) {
  signedOut = handler
}

const language: Middleware = {
  onRequest({ request }) {
    request.headers.set('Accept-Language', locale.value)
    return request
  },
  onResponse({ response }) {
    if (response.status === 401) signedOut()
    return response
  },
}

export const api = createClient<paths>({
  baseUrl: basePath(),
  fetch: async (request) => {
    try {
      return await transport(request)
    } catch (err) {
      throw new ApiError(0, undefined, err)
    }
  },
})
api.use(language)

/** isProblem tells a problem document (RFC 9457 with this API's code) from any other error body. */
export function isProblem(body: unknown): body is Problem {
  return typeof body === 'object' && body !== null && 'code' in body && 'status' in body
}

/**
 * unwrap returns the data of a call or throws its error as an ApiError. Use it where a failure is handled
 * in one place (the page's error state); look at { data, error } where each failure needs its own text.
 */
export function unwrap<T>(result: { data?: T; error?: unknown; response: Response }): T {
  if (result.error !== undefined || !result.response.ok) {
    throw new ApiError(result.response.status, isProblem(result.error) ? result.error : undefined)
  }
  return result.data as T
}
