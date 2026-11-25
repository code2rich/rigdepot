// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

import { defineConfig } from 'vitest/config'

// The console is served by the server program under the base URL's path, which
// is only known at run time: every URL in the build is relative, and the
// server fills <base href> into index.html.
export default defineConfig({
  base: './',
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
