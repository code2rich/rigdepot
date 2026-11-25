// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The console can live under a sub-path of the server (RGD_BASE_URL with a path, behind a reverse proxy).
// The server writes that path into <base href>. Links and route patterns are built with href(), which adds
// it: href('/catalog') is "/catalog" at the root and "/hub/catalog" under /hub.

/** basePath is the path the console is served under, without the trailing slash: "" or "/hub". */
export function basePath(): string {
  if (typeof document === 'undefined') return ''
  return new URL(document.baseURI).pathname.replace(/\/+$/, '')
}

/** href returns a link to a console path such as "/catalog" or "/". */
export function href(path: string): string {
  return basePath() + (path.startsWith('/') ? path : '/' + path)
}

/** stripBase turns a location path back into a console path: "/hub/catalog" -> "/catalog". */
export function stripBase(pathname: string): string {
  const base = basePath()
  const rest = base && pathname.startsWith(base) ? pathname.slice(base.length) : pathname
  return rest.replace(/\/+$/, '') || '/'
}
