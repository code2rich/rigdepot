// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package sandbox

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

// LineOp says whether a line of a diff is kept, removed or added.
type LineOp byte

const (
	Same LineOp = ' '
	Del  LineOp = '-'
	Add  LineOp = '+'
)

// Line is one line of a line diff. Text is the line without its line ending.
type Line struct {
	Op   LineOp
	Text string
}

// maxCells bounds the work of the longest-common-subsequence step: the product of the line
// counts of the part that differs. Beyond it the differing part is shown as removed and added as
// a whole, which is still correct, only less precise.
const maxCells = 4 << 20

// LineDiff compares two texts line by line and returns every line of both, marked as kept,
// removed or added, in order. ok is false when either is not text (not UTF-8, or containing a
// NUL byte); a caller then reports the file as changed without showing lines.
//
// Line endings are part of the comparison, so a line that only changed from LF to CRLF shows as
// changed, but not part of Text.
func LineDiff(before, after []byte) (lines []Line, ok bool) {
	if !isText(before) || !isText(after) {
		return nil, false
	}
	a, b := splitLines(before), splitLines(after)

	// Common prefix and suffix: a tool configuration usually changes in one place.
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	for _, l := range a[:pre] {
		lines = append(lines, Line{Same, trimEOL(l)})
	}
	lines = append(lines, middle(a[pre:len(a)-suf], b[pre:len(b)-suf])...)
	for _, l := range a[len(a)-suf:] {
		lines = append(lines, Line{Same, trimEOL(l)})
	}
	return lines, true
}

// middle diffs the part between the common prefix and suffix with a longest common subsequence.
func middle(a, b []string) []Line {
	var out []Line
	if len(a)*len(b) > maxCells {
		for _, l := range a {
			out = append(out, Line{Del, trimEOL(l)})
		}
		for _, l := range b {
			out = append(out, Line{Add, trimEOL(l)})
		}
		return out
	}
	// lcs[i][j] is the length of the longest common subsequence of a[i:] and b[j:].
	w := len(b) + 1
	lcs := make([]int32, (len(a)+1)*w)
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i*w+j] = lcs[(i+1)*w+j+1] + 1
			} else {
				lcs[i*w+j] = max(lcs[(i+1)*w+j], lcs[i*w+j+1])
			}
		}
	}
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			out = append(out, Line{Same, trimEOL(a[i])})
			i, j = i+1, j+1
		case lcs[(i+1)*w+j] >= lcs[i*w+j+1]:
			out = append(out, Line{Del, trimEOL(a[i])})
			i++
		default:
			out = append(out, Line{Add, trimEOL(b[j])})
			j++
		}
	}
	for ; i < len(a); i++ {
		out = append(out, Line{Del, trimEOL(a[i])})
	}
	for ; j < len(b); j++ {
		out = append(out, Line{Add, trimEOL(b[j])})
	}
	return out
}

func isText(data []byte) bool {
	return utf8.Valid(data) && bytes.IndexByte(data, 0) < 0
}

// splitLines splits after every "\n", keeping it, so that a missing final newline is a
// difference.
func splitLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	lines := strings.SplitAfter(string(data), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func trimEOL(l string) string {
	return strings.TrimSuffix(strings.TrimSuffix(l, "\n"), "\r")
}
