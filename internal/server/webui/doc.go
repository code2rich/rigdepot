// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package webui serves the web console: the files embedded by package web
// (the built console, or the stub page when it was not built), index.html
// filled in for each request (base path, language, the nonce of the content
// security policy), cache headers, and the security headers. In development
// it proxies to the console's development server instead (RGD_WEB_DEV_PROXY).
//
// Every path that is not a file and does not look like one is a page of the
// console and gets index.html; the console's router decides what it shows.
package webui
