// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package check

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"

	extcheck "github.com/code2rich/rigdepot/ext/check"
)

// FindingIDs returns the identifier of each finding of a result, in order. res is the resource
// that was checked; for one the reader refused, it has no files.
//
// An identifier is what a waiver (an administrator marking a finding as a false positive) refers
// to, so it stays the same as long as the finding is about the same content and changes as soon
// as that content changes: it is "f-<rule>-" and the start of a SHA-256 over the rule, the
// message key, the file and
//
//   - for a finding on a line, the text of that line (not its number, so moving it does not
//     matter; the line ending does not count, so a checkout with CRLF gives the same identifier);
//   - for a finding about a whole file, the content of the file;
//   - otherwise, the line number and the parameters.
//
// Two findings that would get the same identifier in one result (the same problem on two equal
// lines) get "-2", "-3" … appended in order.
func FindingIDs(res Resource, found []extcheck.Finding) []string {
	out := make([]string, len(found))
	seen := map[string]int{}
	for i, f := range found {
		h := sha256.New()
		for _, part := range []string{f.Rule, f.Message, f.File} {
			h.Write([]byte(part))
			h.Write([]byte{0})
		}
		data, ok := res.Files[f.File]
		switch {
		case ok && f.Line > 0:
			h.Write([]byte("line\x00"))
			h.Write(lineAt(data, f.Line))
		case ok:
			h.Write([]byte("file\x00"))
			h.Write(data)
		default:
			params, _ := json.Marshal(f.Params) // map keys are sorted
			h.Write([]byte("at\x00" + strconv.Itoa(f.Line) + "\x00"))
			h.Write(params)
		}
		id := "f-" + f.Rule + "-" + hex.EncodeToString(h.Sum(nil))[:12]
		seen[id]++
		if n := seen[id]; n > 1 {
			id += "-" + strconv.Itoa(n)
		}
		out[i] = id
	}
	return out
}

// lineAt returns line n of data, counted from 1, without its line ending.
func lineAt(data []byte, n int) []byte {
	for i := 1; i < n; i++ {
		j := bytes.IndexByte(data, '\n')
		if j < 0 {
			return nil
		}
		data = data[j+1:]
	}
	if j := bytes.IndexByte(data, '\n'); j >= 0 {
		data = data[:j]
	}
	return bytes.TrimSuffix(data, []byte("\r"))
}
