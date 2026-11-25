// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The console's entry point.

import { render } from 'preact'
import './styles/index.css'
import { App } from './app'
import { setTransport } from './api/client'

// The development stand-in for the server (src/api/mock.ts). The condition is decided at build time, so a
// real build contains neither the mock nor this branch.
if (import.meta.env.VITE_API_MOCK === '1') {
  const { mockServer } = await import('./api/mock')
  setTransport(mockServer())
}

const root = document.getElementById('app')
if (root) render(<App />, root)
