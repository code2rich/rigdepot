// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package injection

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/code2rich/rigdepot/builtin/checks/internal/textlines"
	"github.com/code2rich/rigdepot/ext/check"
)

const id = "injection"

type rule struct{}

// New returns the rule.
func New() check.Rule { return rule{} }

func (rule) ID() string               { return id }
func (rule) Severity() check.Severity { return check.MustFix }

// Applies to every kind: an AI reads all of them.
func (rule) Applies(string) bool { return true }

var ignoreInstructions = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(ignore|disregard|forget|override)\s+(all\s+|any\s+)?(of\s+)?(the\s+|your\s+|my\s+)?(previous|prior|above|earlier|preceding|system|original)\s+(instructions?|prompts?|rules|messages|directions|guidelines)`),
	regexp.MustCompile(`(忽略|无视|忘记|忘掉|不要理会|不用理会|抛开)掉?(你)?(之前|以上|前面|上面|上述|先前|此前|系统|原来|原有|所有)的?(所有|全部|一切)?的?(指令|指示|规则|要求|提示词|提示|约束|设定)`),
}

const credential = `(credentials?|passwords?|tokens?|secrets?|api[ _-]?keys?|private[ _-]?keys?|ssh[ _-]?keys?|cookies?|\.env\b|~/\.ssh|~/\.aws|id_rsa|\.netrc)`

var exfiltrate = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(send|upload|post|forward|exfiltrate|transmit|email|leak|paste)\b[^.\n]{0,60}` + credential + `[^.\n]{0,40}(\b(to|into)\b|https?://)`),
	regexp.MustCompile(`(?i)(发送|上传|外发|发到|传到|提交到|转发|贴到|发给)[^。\n]{0,30}(凭证|凭据|密码|口令|令牌|密钥|私钥|token|cookie|\.ssh|\.env|id_rsa|credentials)`),
	regexp.MustCompile(`(?i)(把|将)[^。\n]{0,20}(凭证|凭据|密码|口令|令牌|密钥|私钥|token|cookie|\.ssh|\.env|id_rsa)[^。\n]{0,20}(发送|上传|外发|发到|传到|提交到|转发|贴到|发给)`),
	regexp.MustCompile(`(?i)(凭证|凭据|密码|口令|令牌|密钥|私钥|token|cookie|\.ssh|\.env|id_rsa|credentials)[^。\n]{0,20}(上传|外发|发到|传到|提交到|转发|贴到|发给)`),
	regexp.MustCompile(`\bcat\s+[^|\n]*(~|\$HOME)/\.(ssh|aws|netrc|config/gcloud)[^|\n]*\|\s*(curl|wget|nc|ncat)\b`),
	regexp.MustCompile(`\bcurl\b[^\n]*(-d|--data(-binary|-raw)?|-F|--form|-T|--upload-file)\s*["']?[a-z]*=?@?["']?(~|\$HOME)/\.(ssh|aws|netrc|env)`),
}

// negations, written just before a match, turn a request into a prohibition.
var negations = []string{"不要", "不得", "禁止", "切勿", "严禁", "不能", "不可", "别", "never", "do not", "don't", "must not", "should not"}

func (rule) Check(_ context.Context, in check.Input) ([]check.Finding, error) {
	var found []check.Finding
	add := func(file string, line int, problem string, params map[string]any) {
		found = append(found, check.Finding{
			Rule:     id,
			Severity: check.MustFix,
			File:     file,
			Line:     line,
			Message:  "check." + id + "." + problem,
			Params:   params,
		})
	}
	err := textlines.Each(in.Files, func(file string, line int, text string) {
		if r, ok := hiddenChar(text); ok {
			add(file, line, "hidden_char", map[string]any{"codepoint": fmt.Sprintf("U+%04X", r)})
		}
		if matchesUnnegated(ignoreInstructions, text) {
			add(file, line, "ignore_instructions", nil)
		}
		if matchesUnnegated(exfiltrate, text) {
			add(file, line, "exfiltrate", nil)
		}
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

// hiddenChar returns the first character on the line that a person does not see.
func hiddenChar(text string) (rune, bool) {
	for _, r := range text {
		switch {
		case r >= 0x200B && r <= 0x200F, // zero width space, joiners, direction marks
			r >= 0x202A && r <= 0x202E,   // bidirectional embeddings and overrides
			r >= 0x2060 && r <= 0x2064,   // word joiner, invisible operators
			r >= 0x2066 && r <= 0x2069,   // bidirectional isolates
			r == 0xFEFF,                  // a byte order mark inside the text
			r >= 0xE0000 && r <= 0xE007F: // tag characters
			return r, true
		}
	}
	return 0, false
}

func matchesUnnegated(res []*regexp.Regexp, text string) bool {
	for _, re := range res {
		for _, m := range re.FindAllStringIndex(text, -1) {
			if !negated(text[:m[0]]) {
				return true
			}
		}
	}
	return false
}

// negated reports whether the few words before a match forbid it.
func negated(before string) bool {
	runes := []rune(strings.ToLower(before))
	window := string(runes[max(0, len(runes)-16):])
	for _, n := range negations {
		if strings.Contains(window, n) {
			return true
		}
	}
	return false
}
