// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package i18n

import (
	"bytes"
	"strings"
)

// SystemLanguages returns the languages the operating system says the person
// prefers, most preferred first, as BCP 47 tags. It never fails: when nothing
// can be found the list is empty and the caller falls back to English.
//
//   - Linux and other Unix systems: the first meaningful value of LC_ALL,
//     LC_MESSAGES, LANG and LANGUAGE, in that order ("C" and "POSIX" do not
//     count).
//   - macOS: the same variables; without them, the system's preferred
//     languages (defaults read -g AppleLanguages).
//   - Windows: GetUserPreferredUILanguages.
func SystemLanguages() []string { return systemLanguages() }

// localeVariables are read in this order; the first with a meaningful value
// wins.
var localeVariables = []string{"LC_ALL", "LC_MESSAGES", "LANG", "LANGUAGE"}

// envLanguages reads the POSIX locale variables.
func envLanguages(getenv func(string) string) []string {
	for _, name := range localeVariables {
		value := getenv(name)
		var tags []string
		// LANGUAGE is a colon-separated list of preferences; the others hold
		// one locale.
		for _, part := range strings.Split(value, ":") {
			if tag := posixToTag(part); tag != "" {
				tags = append(tags, tag)
			}
		}
		if len(tags) > 0 {
			return tags
		}
	}
	return nil
}

// posixToTag turns a POSIX locale such as zh_CN.UTF-8 or de_DE@euro into a
// BCP 47 tag (zh-CN, de-DE). The C and POSIX locales say nothing about
// language and give "".
func posixToTag(locale string) string {
	locale = strings.TrimSpace(locale)
	if i := strings.IndexAny(locale, ".@"); i >= 0 {
		locale = locale[:i]
	}
	if locale == "" || locale == "C" || locale == "POSIX" {
		return ""
	}
	return strings.ReplaceAll(locale, "_", "-")
}

// parseAppleLanguages reads the output of defaults read -g AppleLanguages, a
// property list array such as:
//
//	(
//	    "zh-Hans-CN",
//	    en
//	)
func parseAppleLanguages(out []byte) []string {
	out = bytes.TrimSpace(out)
	out = bytes.TrimPrefix(out, []byte("("))
	out = bytes.TrimSuffix(out, []byte(")"))
	var tags []string
	for _, item := range strings.Split(string(out), ",") {
		item = strings.Trim(strings.TrimSpace(item), `"`)
		if item != "" {
			tags = append(tags, item)
		}
	}
	return tags
}

// darwinLanguages is the macOS lookup with its two sources passed in, so that
// it can be tested on every platform.
func darwinLanguages(getenv func(string) string, appleLanguages func() ([]byte, error)) []string {
	if tags := envLanguages(getenv); len(tags) > 0 {
		return tags
	}
	out, err := appleLanguages()
	if err != nil {
		return nil
	}
	return parseAppleLanguages(out)
}
