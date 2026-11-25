// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package codex

import (
	"bytes"
	"errors"
	"fmt"
	"slices"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// checkConfig looks at the content of config.toml before servers are planned for it. It returns
// an error when the file does not parse, and a problem when it parses but an entry cannot be
// added as a table of its own ("[mcp_servers.<name>]" appended at the end) without rewriting
// what the person wrote: mcp_servers written as an inline table (mcp_servers = { … }), as an
// array of tables, or as something that is not a table at all. Neither message quotes the file.
//
// Codex itself accepts a byte order mark and CRLF line endings (checked on codex-cli 0.172.0),
// and so does this.
func checkConfig(data []byte) (problem string, err error) {
	body := bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	doc := map[string]any{}
	if err := toml.Unmarshal(body, &doc); err != nil {
		var de *toml.DecodeError
		if errors.As(err, &de) {
			row, col := de.Position()
			return "", fmt.Errorf("not valid TOML at line %d, column %d", row, col)
		}
		return "", errors.New("not valid TOML")
	}

	// Find how mcp_servers is written. The decoded document cannot tell an inline table from
	// one written with headers; the parser's expressions can.
	p := unstable.Parser{}
	p.Reset(body)
	var header []string
	inArray := false
	for p.NextExpression() {
		n := p.Expression()
		switch n.Kind {
		case unstable.Table, unstable.ArrayTable:
			header, inArray = keyOf(n), n.Kind == unstable.ArrayTable
			if inArray && len(header) > 0 && header[0] == serversKey {
				return fmt.Sprintf("%s is written as an array of tables ([[%s]]), which Codex does not read and which cannot take an entry of ours", serversKey, serversKey), nil
			}
		case unstable.KeyValue:
			if inArray {
				continue
			}
			key := slices.Concat(header, keyOf(n))
			if !slices.Equal(key, []string{serversKey}) {
				continue
			}
			if n.Value().Kind == unstable.InlineTable {
				return fmt.Sprintf("%s is written as an inline table (%s = { … }); TOML does not allow adding a [%s.<name>] table to it, and it is not rewritten", serversKey, serversKey, serversKey), nil
			}
			return fmt.Sprintf("%s is not a table", serversKey), nil
		}
	}
	if err := p.Error(); err != nil {
		return "", errors.New("not valid TOML")
	}
	if v, ok := doc[serversKey]; ok {
		if _, isTable := v.(map[string]any); !isTable {
			return fmt.Sprintf("%s is not a table", serversKey), nil
		}
	}
	return "", nil
}

// keyOf returns the parts of a table header's or a key-value's key.
func keyOf(n *unstable.Node) []string {
	var parts []string
	it := n.Key()
	for it.Next() {
		parts = append(parts, string(it.Node().Data))
	}
	return parts
}
