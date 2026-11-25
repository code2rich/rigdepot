// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package catalog

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"
)

// maxDiffCells bounds the work of one diff: lines of the changed middle of the old file times
// those of the new one. Beyond it the change is reported without a unified diff.
const maxDiffCells = 1 << 20

// diffContext is the number of unchanged lines shown around a change.
const diffContext = 3

// unifiedDiff returns the hunks of a unified diff from a to b, without file headers, or "" when
// either is not text or the files are too different to compare cheaply.
func unifiedDiff(a, b []byte) string {
	if !isText(a) || !isText(b) {
		return ""
	}
	x, y := splitLines(a), splitLines(b)
	// Common prefix and suffix need no table.
	pre := 0
	for pre < len(x) && pre < len(y) && x[pre] == y[pre] {
		pre++
	}
	suf := 0
	for suf < len(x)-pre && suf < len(y)-pre && x[len(x)-1-suf] == y[len(y)-1-suf] {
		suf++
	}
	mx, my := x[pre:len(x)-suf], y[pre:len(y)-suf]
	if (len(mx)+1)*(len(my)+1) > maxDiffCells {
		return ""
	}
	type op struct {
		kind byte // ' ', '-', '+'
		line string
	}
	var ops []op
	for _, l := range x[:pre] {
		ops = append(ops, op{' ', l})
	}
	// Longest common subsequence of the middles.
	n, m := len(mx), len(my)
	lcs := make([]int32, (n+1)*(m+1))
	at := func(i, j int) *int32 { return &lcs[i*(m+1)+j] }
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if mx[i] == my[j] {
				*at(i, j) = *at(i+1, j+1) + 1
			} else {
				*at(i, j) = max(*at(i+1, j), *at(i, j+1))
			}
		}
	}
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && mx[i] == my[j]:
			ops = append(ops, op{' ', mx[i]})
			i++
			j++
		case i < n && (j == m || *at(i+1, j) >= *at(i, j+1)):
			// Removals before additions, as diff(1) writes them.
			ops = append(ops, op{'-', mx[i]})
			i++
		default:
			ops = append(ops, op{'+', my[j]})
			j++
		}
	}
	for _, l := range x[len(x)-suf:] {
		ops = append(ops, op{' ', l})
	}

	// Group the changes into hunks with context around them.
	var out strings.Builder
	for k := 0; k < len(ops); {
		if ops[k].kind == ' ' {
			k++
			continue
		}
		start := max(0, k-diffContext)
		end := k
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].kind == ' ' {
				run++
			}
			if run == len(ops) || run-end > 2*diffContext {
				end = min(len(ops), end+diffContext)
				break
			}
			end = run
		}
		// Line numbers of the hunk in both files.
		oldStart, newStart := 1, 1
		for _, o := range ops[:start] {
			if o.kind != '+' {
				oldStart++
			}
			if o.kind != '-' {
				newStart++
			}
		}
		oldLen, newLen := 0, 0
		var body strings.Builder
		for _, o := range ops[start:end] {
			if o.kind != '+' {
				oldLen++
			}
			if o.kind != '-' {
				newLen++
			}
			body.WriteByte(o.kind)
			body.WriteString(strings.TrimSuffix(o.line, "\n"))
			body.WriteByte('\n')
			if !strings.HasSuffix(o.line, "\n") {
				body.WriteString("\\ No newline at end of file\n")
			}
		}
		if oldLen == 0 {
			oldStart--
		}
		if newLen == 0 {
			newStart--
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n%s", oldStart, oldLen, newStart, newLen, body.String())
		k = end
	}
	return out.String()
}

func isText(b []byte) bool { return utf8.Valid(b) && bytes.IndexByte(b, 0) < 0 }

// splitLines splits after each newline; the last line may lack one.
func splitLines(b []byte) []string {
	var out []string
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			out = append(out, string(b))
			break
		}
		out = append(out, string(b[:i+1]))
		b = b[i+1:]
	}
	return out
}
