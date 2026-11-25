// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// The design tokens of the web console live in web/tokens/tokens.json. gen
// turns them into the CSS custom properties the styles use, writing the dark
// theme twice (once for "follow the system", once for "switched to dark by
// hand"), and refuses colours that are too faint to read.

const (
	tokensFile   = "web/tokens/tokens.json"
	tokensOutput = "web/src/styles/tokens.gen.css"
)

type tokenFile struct {
	Description string       `json:"description"`
	Groups      []tokenGroup `json:"groups"`
	Contrast    struct {
		Description string   `json:"description"`
		Min         float64  `json:"min"`
		Text        []string `json:"text"`
		Surfaces    []string `json:"surfaces"`
	} `json:"contrast"`
}

type tokenGroup struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Tokens      []designToken `json:"tokens"`
}

// designToken has either one value for both themes, or a light and a dark one.
type designToken struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Light string `json:"light"`
	Dark  string `json:"dark"`
}

func (t designToken) light() string {
	if t.Value != "" {
		return t.Value
	}
	return t.Light
}

func (t designToken) dark() string {
	if t.Value != "" {
		return t.Value
	}
	return t.Dark
}

var tokenName = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// readTokens reads and validates tokens.json. It returns nil when the file
// does not exist (a tree without the web console).
func readTokens(root string) (*tokenFile, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(tokensFile)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var tf tokenFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&tf); err != nil {
		return nil, fmt.Errorf("%s: %w", tokensFile, err)
	}
	var problems []string
	seen := map[string]bool{}
	for _, g := range tf.Groups {
		if g.Name == "" || len(g.Tokens) == 0 {
			problems = append(problems, fmt.Sprintf("group %q needs a name and at least one token", g.Name))
		}
		for _, t := range g.Tokens {
			switch {
			case !tokenName.MatchString(t.Name):
				problems = append(problems, fmt.Sprintf("%q is not a token name: lower-case words joined by hyphens", t.Name))
			case seen[t.Name]:
				problems = append(problems, fmt.Sprintf("token %q appears twice", t.Name))
			case t.Value != "" && (t.Light != "" || t.Dark != ""):
				problems = append(problems, fmt.Sprintf("token %q has a value and theme values; give one or the other", t.Name))
			case t.Value == "" && (t.Light == "" || t.Dark == ""):
				problems = append(problems, fmt.Sprintf("token %q needs a value, or both a light and a dark value", t.Name))
			}
			seen[t.Name] = true
			for _, v := range []string{t.Value, t.Light, t.Dark} {
				if strings.ContainsAny(v, ";{}<>") {
					problems = append(problems, fmt.Sprintf("token %q: %q is not a single CSS value", t.Name, v))
				}
			}
		}
	}
	problems = append(problems, tf.contrastProblems()...)
	if len(problems) > 0 {
		return nil, fmt.Errorf("%s:\n  %s", tokensFile, strings.Join(problems, "\n  "))
	}
	return &tf, nil
}

func (tf *tokenFile) byName() map[string]designToken {
	m := map[string]designToken{}
	for _, g := range tf.Groups {
		for _, t := range g.Tokens {
			m[t.Name] = t
		}
	}
	return m
}

// contrastProblems applies the rule of the brand guide: every text colour
// reaches the minimum contrast on every surface, in both themes.
func (tf *tokenFile) contrastProblems() []string {
	c := tf.Contrast
	if c.Min < 1 || len(c.Text) == 0 || len(c.Surfaces) == 0 {
		return []string{"contrast needs min (at least 1), text and surfaces"}
	}
	tokens := tf.byName()
	var problems []string
	color := func(name, theme string) (rgb [3]float64, ok bool) {
		t, found := tokens[name]
		if !found {
			problems = append(problems, fmt.Sprintf("contrast names %q, which is not a token", name))
			return rgb, false
		}
		v := t.light()
		if theme == "dark" {
			v = t.dark()
		}
		rgb, err := parseHexColor(v)
		if err != nil {
			problems = append(problems, fmt.Sprintf("contrast: %s (%s) %v", name, theme, err))
			return rgb, false
		}
		return rgb, true
	}
	for _, theme := range []string{"light", "dark"} {
		for _, text := range c.Text {
			fg, ok := color(text, theme)
			if !ok {
				continue
			}
			for _, surface := range c.Surfaces {
				bg, ok := color(surface, theme)
				if !ok {
					continue
				}
				if r := contrastRatio(fg, bg); r < c.Min {
					problems = append(problems, fmt.Sprintf("%s on %s in the %s theme has a contrast of %.2f : 1; at least %g : 1 is needed",
						text, surface, theme, r, c.Min))
				}
			}
		}
	}
	return problems
}

func parseHexColor(v string) ([3]float64, error) {
	var rgb [3]float64
	if len(v) != 7 || v[0] != '#' {
		return rgb, fmt.Errorf("%q is not a #rrggbb colour; contrast can only be checked on those", v)
	}
	for i := range 3 {
		n, err := strconv.ParseUint(v[1+2*i:3+2*i], 16, 8)
		if err != nil {
			return rgb, fmt.Errorf("%q is not a #rrggbb colour", v)
		}
		rgb[i] = float64(n) / 255
	}
	return rgb, nil
}

// contrastRatio is the WCAG 2 contrast ratio of two sRGB colours.
func contrastRatio(a, b [3]float64) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func luminance(c [3]float64) float64 {
	lin := func(v float64) float64 {
		if v <= 0.03928 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c[0]) + 0.7152*lin(c[1]) + 0.0722*lin(c[2])
}

// tokensCSS writes the custom properties: every token in the light theme
// under :root, then the dark values twice, for the system preference (unless
// the person picked light) and for the person picking dark.
func tokensCSS(tf *tokenFile) []byte {
	var b bytes.Buffer
	b.WriteString(webHeader("/*", " */", tokensFile))
	b.WriteString("\n:root {\n")
	for _, g := range tf.Groups {
		fmt.Fprintf(&b, "  /* %s: %s */\n", g.Name, g.Description)
		for _, t := range g.Tokens {
			fmt.Fprintf(&b, "  --%s: %s;\n", t.Name, t.light())
		}
	}
	b.WriteString("  color-scheme: light;\n}\n")
	dark := func(indent string) {
		for _, g := range tf.Groups {
			for _, t := range g.Tokens {
				if t.Value == "" {
					fmt.Fprintf(&b, "%s--%s: %s;\n", indent, t.Name, t.Dark)
				}
			}
		}
		fmt.Fprintf(&b, "%scolor-scheme: dark;\n", indent)
	}
	b.WriteString("\n@media (prefers-color-scheme: dark) {\n  :root:not([data-theme=\"light\"]) {\n")
	dark("    ")
	b.WriteString("  }\n}\n\n:root[data-theme=\"dark\"] {\n")
	dark("  ")
	b.WriteString("}\n")
	return b.Bytes()
}

// webHeader is the first lines of a generated web file, as comments: the
// generated-code marker and the license identifier. Generated files need no
// copyright line (docs licensing: 文件头与 SPDX 标识).
func webHeader(open, close, source string) string {
	lines := []string{
		`Code generated by "go run ./tools/dev gen" from ` + source + ". DO NOT EDIT.",
		licenseLine,
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(open + " " + l + close + "\n")
	}
	return b.String()
}

// generateTokens returns tokens.gen.css, or nothing when there is no
// tokens.json.
func generateTokens(root string) ([]generated, error) {
	tf, err := readTokens(root)
	if err != nil || tf == nil {
		if err == nil && isFile(filepath.Join(root, filepath.FromSlash(tokensOutput))) {
			return nil, fmt.Errorf("%s is generated from %s, which is missing", tokensOutput, tokensFile)
		}
		return nil, err
	}
	return []generated{{path: tokensOutput, data: tokensCSS(tf)}}, nil
}
