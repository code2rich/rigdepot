// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package check

import (
	"bytes"
	"errors"

	"go.yaml.in/yaml/v4"
)

// frontMatterDescription returns the description in the YAML front matter of a SKILL.md: the
// block between a first line "---" and the next line "---". It fails when there is no front
// matter or it is not a YAML mapping.
func frontMatterDescription(data []byte) (string, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	lines := bytes.SplitAfter(data, []byte("\n"))
	if len(lines) == 0 || string(bytes.TrimRight(lines[0], "\r\n")) != "---" {
		return "", errors.New("SKILL.md has no front matter")
	}
	var block []byte
	closed := false
	for _, l := range lines[1:] {
		if string(bytes.TrimRight(l, "\r\n")) == "---" {
			closed = true
			break
		}
		block = append(block, l...)
	}
	if !closed {
		return "", errors.New("the front matter of SKILL.md does not end")
	}
	var fm struct {
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal(block, &fm); err != nil {
		return "", err
	}
	return fm.Description, nil
}
