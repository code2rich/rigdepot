// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The console's browser tests (docs/architecture/11-testing.md#web-控制台), in Chromium and WebKit. Run
// them with `go run ./tools/dev e2e-web`, which prepares what each run needs and sets the variables below:
//
//   E2E_MODE=mock   the pages against the development stand-in (VITE_API_MOCK=1), started here on
//                   E2E_PORT: every page × three roles × light and dark × 390 and 1440 px × Chinese,
//                   English and the pseudo-language, plus keyboard and accessibility checks.
//   E2E_MODE=real   the main paths against a real server that tools/dev started at E2E_BASE_URL with an
//                   empty database: the first-run wizard, sign-in, users, products, uploading and
//                   publishing a resource, finding it in the catalog.
//   E2E_OUT         where screenshots, the report and the traces of failures go.

import { defineConfig, devices } from '@playwright/test'

const mode = process.env.E2E_MODE === 'real' ? 'real' : 'mock'
const port = process.env.E2E_PORT ?? '5174'
const baseURL = mode === 'real' ? process.env.E2E_BASE_URL : `http://127.0.0.1:${port}`
const out = process.env.E2E_OUT ?? 'e2e/.out'

const browsers = [
  { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
  // No traces in WebKit: recording one adds a style sheet to the page, which WebKit holds to the server's
  // content security policy and refuses, logging an error that the tests would blame on the page.
  { name: 'webkit', use: { ...devices['Desktop Safari'], trace: 'off' as const } },
]

export default defineConfig({
  testDir: `e2e/${mode}`,
  outputDir: `${out}/${mode}/results`,
  fullyParallel: mode === 'mock',
  forbidOnly: true,
  retries: 0,
  // The real run shares one server and one database: its steps build on each other.
  workers: mode === 'real' ? 1 : undefined,
  timeout: mode === 'real' ? 60_000 : 120_000,
  expect: { timeout: 10_000 },
  reporter: [
    ['list'],
    ['html', { outputFolder: `${out}/${mode}/report`, open: 'never' }],
    ['json', { outputFile: `${out}/${mode}/results.json` }],
  ],
  use: {
    baseURL,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    locale: 'en-US',
    timezoneId: 'UTC',
  },
  projects:
    mode === 'real'
      ? [
          // The wizard runs once, in the first browser; each browser then works with names of its own.
          { name: 'setup', testMatch: /setup\.spec\.ts/, use: browsers[0]?.use },
          ...browsers.map((b) => ({ ...b, testIgnore: /setup\.spec\.ts/, dependencies: ['setup'] })),
        ]
      : browsers,
  webServer:
    mode === 'mock'
      ? {
          command: `pnpm exec vite --port ${port} --strictPort`,
          url: `http://127.0.0.1:${port}/`,
          env: { VITE_API_MOCK: '1' },
          reuseExistingServer: false,
          timeout: 60_000,
        }
      : undefined,
})
