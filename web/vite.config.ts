// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

import { defineConfig, type Plugin } from 'vitest/config'

// With the development stand-in for the server (VITE_API_MOCK=1, src/api/mock.ts) no server is in front to
// fill in index.html's placeholders, so the development server does it: the console at the root, English as
// the default language, no nonce. Through the server (RGD_WEB_DEV_PROXY) the placeholders stay for it.
const mockPage: Plugin = {
  name: 'mock-page',
  apply: (_, env) => env.command === 'serve' && process.env.VITE_API_MOCK === '1',
  transformIndexHtml: (html) =>
    html
      .replaceAll('__BASE_HREF__', '/')
      .replaceAll('__LANG__', 'en')
      .replaceAll('__DEFAULT_LOCALE__', 'en')
      .replaceAll('__CSP_NONCE__', ''),
}

// The console is served by the server program under the base URL's path, which
// is only known at run time: every URL in the build is relative, and the
// server fills <base href> into index.html.
export default defineConfig({
  base: './',
  plugins: [mockPage],
  // The message catalogues live outside web/, in the repository's locales/.
  server: {
    port: 5173,
    strictPort: true,
    host: '127.0.0.1',
    fs: { allow: ['.', '../locales'] },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    // No inline script: the content security policy allows scripts from the
    // server and the one boot script carrying the per-request nonce.
    modulePreload: { polyfill: false },
    assetsInlineLimit: 0,
    sourcemap: false,
    target: 'es2022',
  },
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts', 'src/**/*.test.tsx'],
  },
})
