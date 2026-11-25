// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package dbx

import (
	"strconv"
	"strings"
)

// Rebind rewrites the ? placeholders of a query for the dialect: PostgreSQL
// numbers them ($1, $2, …), SQLite takes them as they are. A ? inside a
// string literal, a quoted identifier, a comment or a dollar-quoted string is
// left alone.
func Rebind(d Dialect, query string) string {
	if d != Postgres || !strings.Contains(query, "?") {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 8)
	n := 0
	for i := 0; i < len(query); {
		c := query[i]
		switch {
		case c == '\'' || c == '"':
			end := skipQuoted(query, i, c)
			b.WriteString(query[i:end])
			i = end
		case c == '-' && strings.HasPrefix(query[i:], "--"):
			end := strings.IndexByte(query[i:], '\n')
			if end < 0 {
				end = len(query) - i
			}
			b.WriteString(query[i : i+end])
			i += end
		case c == '/' && strings.HasPrefix(query[i:], "/*"):
			end := strings.Index(query[i+2:], "*/")
			if end < 0 {
				end = len(query)
			} else {
				end = i + 2 + end + 2
			}
			b.WriteString(query[i:end])
			i = end
		case c == '$':
			if end, ok := skipDollarQuoted(query, i); ok {
				b.WriteString(query[i:end])
				i = end
				break
			}
			b.WriteByte(c)
			i++
		case c == '?':
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// skipQuoted returns the index just past the literal that starts at i with
// quote q. A doubled quote inside is an escaped quote.
func skipQuoted(s string, i int, q byte) int {
	for j := i + 1; j < len(s); j++ {
		if s[j] == q {
			if j+1 < len(s) && s[j+1] == q {
				j++
				continue
			}
			return j + 1
		}
	}
	return len(s)
}

// skipDollarQuoted recognizes a PostgreSQL dollar-quoted string ($$…$$ or
// $tag$…$tag$) starting at i and returns the index just past it.
func skipDollarQuoted(s string, i int) (int, bool) {
	j := i + 1
	for j < len(s) && (s[j] == '_' || s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z' || j > i+1 && s[j] >= '0' && s[j] <= '9') {
		j++
	}
	if j >= len(s) || s[j] != '$' {
		return 0, false // $1 and the like, not a quote
	}
	tag := s[i : j+1]
	end := strings.Index(s[j+1:], tag)
	if end < 0 {
		return len(s), true
	}
	return j + 1 + end + len(tag), true
}
