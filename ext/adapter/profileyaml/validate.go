// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package profileyaml

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/code2rich/rigdepot/ext/adapter"
)

// The patterns of schema/adapter/v1/profile.schema.json. The tests run this package against the
// schema's own samples, so a change on one side without the other fails there.
var (
	idPattern       = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	rootNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	envPattern      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	datePattern     = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
	extPattern      = regexp.MustCompile(`^\.[A-Za-z0-9]+$`)
	templatePattern = regexp.MustCompile(`^(~|\{[a-z][a-z0-9_-]*\})(/[^\\]*)?$`)
)

var goosValues = []string{"darwin", "linux", "windows"}

type validator struct {
	problems  []Problem
	roots     map[string]bool
	templates []template // checked against roots at the end
}

type template struct{ field, value string }

func (v *validator) add(field, format string, args ...any) {
	v.problems = append(v.problems, Problem{Field: field, Message: fmt.Sprintf(format, args...)})
}

func join(parent string, child any) string {
	c := fmt.Sprint(child)
	if parent == "" {
		return c
	}
	return parent + "." + c
}

// object returns v as a map and checks the required fields, or reports what it is instead.
func (v *validator) object(field string, x any, required ...string) (map[string]any, bool) {
	m, ok := x.(map[string]any)
	if !ok {
		v.add(field, "has to be a mapping, not %s", describe(x))
		return nil, false
	}
	for _, r := range required {
		if _, ok := m[r]; !ok {
			v.add(join(field, r), "is required")
		}
	}
	return m, true
}

func (v *validator) list(field string, x any, minItems int) ([]any, bool) {
	l, ok := x.([]any)
	if !ok {
		v.add(field, "has to be a list, not %s", describe(x))
		return nil, false
	}
	if len(l) < minItems {
		v.add(field, "needs at least %d item(s)", minItems)
		return nil, false
	}
	return l, true
}

func (v *validator) str(field string, x any, minLen int) (string, bool) {
	s, ok := x.(string)
	if !ok {
		v.add(field, "has to be text, not %s; put it in quotes", describe(x))
		return "", false
	}
	if len(s) < minLen {
		v.add(field, "must not be empty")
		return "", false
	}
	return s, true
}

func (v *validator) pattern(field string, x any, re *regexp.Regexp, what string) (string, bool) {
	s, ok := v.str(field, x, 0)
	if !ok {
		return "", false
	}
	if !re.MatchString(s) {
		v.add(field, "%q is not %s", s, what)
		return "", false
	}
	return s, true
}

func (v *validator) enum(field string, x any, values ...string) {
	if s, ok := x.(string); ok && slices.Contains(values, s) {
		return
	}
	v.add(field, "has to be one of %s, not %s", strings.Join(values, ", "), describe(x))
}

func (v *validator) strings(field string, x any, minItems int) {
	l, ok := v.list(field, x, minItems)
	for i, item := range l {
		if ok {
			v.str(join(field, i), item, 1)
		}
	}
}

func (v *validator) pathTemplate(field string, x any) {
	s, ok := v.pattern(field, x, templatePattern,
		`a path that starts with "~/" or with a root in braces ("{config}/skills"), with forward slashes`)
	if ok {
		v.templates = append(v.templates, template{field, s})
	}
}

func (v *validator) profile(doc any) {
	m, ok := v.object("", doc, "id", "name", "detect")
	if !ok {
		return
	}
	if x, ok := m["id"]; ok {
		v.pattern("id", x, idPattern, "lower-case letters and digits joined by single hyphens")
	}
	if x, ok := m["name"]; ok {
		v.str("name", x, 1)
	}
	if x, ok := m["roots"]; ok {
		v.rootsField(x)
	}
	if x, ok := m["detect"]; ok {
		v.detect(x)
	}
	if x, ok := m["verified"]; ok {
		v.verified(x)
	}
	if x, ok := m["skills"]; ok {
		if s, ok := v.object("skills", x, "dir", "layout"); ok {
			if d, ok := s["dir"]; ok {
				v.pathTemplate("skills.dir", d)
			}
			if l, ok := s["layout"]; ok {
				v.enum("skills.layout", l, adapter.LayoutDir)
			}
		}
	}
	for _, name := range []string{"commands", "agents"} {
		if x, ok := m[name]; ok {
			if s, ok := v.object(name, x, "dir", "ext"); ok {
				if d, ok := s["dir"]; ok {
					v.pathTemplate(name+".dir", d)
				}
				if e, ok := s["ext"]; ok {
					v.pattern(name+".ext", e, extPattern, "an extension with its dot, such as .md")
				}
			}
		}
	}
	if x, ok := m["mcp"]; ok {
		v.mcp(x)
	}
	if x, ok := m["alsoReads"]; ok {
		if l, ok := v.list("alsoReads", x, 0); ok {
			for i, item := range l {
				f := join("alsoReads", i)
				if s, ok := v.object(f, item, "kind", "dir"); ok {
					if k, ok := s["kind"]; ok {
						v.enum(f+".kind", k, "skill")
					}
					if d, ok := s["dir"]; ok {
						v.pathTemplate(f+".dir", d)
					}
				}
			}
		}
	}
	if x, ok := m["credentials"]; ok {
		v.strings("credentials", x, 0)
	}

	for _, t := range v.templates {
		if strings.HasPrefix(t.value, "{") {
			name := t.value[1:strings.IndexByte(t.value, '}')]
			if !v.roots[name] {
				v.add(t.field, "refers to the root %q, which roots does not declare", name)
			}
		}
	}
}

func (v *validator) rootsField(x any) {
	m, ok := v.object("roots", x)
	if !ok {
		return
	}
	v.roots = map[string]bool{}
	for name, r := range m {
		f := join("roots", name)
		if !rootNamePattern.MatchString(name) {
			v.add(f, "the name %q is not lower-case letters, digits, underscores and hyphens", name)
			continue
		}
		v.roots[name] = true
		root, ok := v.object(f, r, "default")
		if !ok {
			continue
		}
		if e, ok := root["env"]; ok {
			v.pattern(f+".env", e, envPattern, "the name of an environment variable (without $)")
		}
		if j, ok := root["join"]; ok {
			v.str(f+".join", j, 1)
		}
		if d, ok := root["default"]; ok {
			v.pathTemplate(f+".default", d)
		}
	}
}

func (v *validator) detect(x any) {
	m, ok := v.object("detect", x)
	if !ok {
		return
	}
	_, hasBins := m["bins"]
	_, hasDirs := m["dirs"]
	if !hasBins && !hasDirs {
		v.add("detect", "needs bins or dirs, or both")
	}
	if b, ok := m["bins"]; ok {
		v.strings("detect.bins", b, 1)
	}
	if d, ok := m["dirs"]; ok {
		if l, ok := v.list("detect.dirs", d, 1); ok {
			for i, item := range l {
				v.pathTemplate(join("detect.dirs", i), item)
			}
		}
	}
	if p, ok := m["version"]; ok {
		probe, ok := v.object("detect.version", p, "run", "match")
		if !ok {
			return
		}
		if r, ok := probe["run"]; ok {
			v.strings("detect.version.run", r, 1)
		}
		if mt, ok := probe["match"]; ok {
			if s, ok := v.str("detect.version.match", mt, 1); ok {
				re, err := regexp.Compile(s)
				switch {
				case err != nil:
					v.add("detect.version.match", "is not a regular expression: %v", err)
				case re.NumSubexp() < 1:
					v.add("detect.version.match", "needs a group around the version, such as (\\d+\\.\\d+\\.\\d+)")
				}
			}
		}
	}
}

func (v *validator) verified(x any) {
	m, ok := v.object("verified", x, "version", "os", "at", "how")
	if !ok {
		return
	}
	if s, ok := m["version"]; ok {
		v.str("verified.version", s, 1)
	}
	if o, ok := m["os"]; ok {
		if l, ok := v.list("verified.os", o, 1); ok {
			seen := map[string]bool{}
			for i, item := range l {
				f := join("verified.os", i)
				v.enum(f, item, goosValues...)
				if s, ok := item.(string); ok {
					if seen[s] {
						v.add(f, "%q is listed twice", s)
					}
					seen[s] = true
				}
			}
		}
	}
	if a, ok := m["at"]; ok {
		if s, ok := v.pattern("verified.at", a, datePattern, "a date written YYYY-MM-DD"); ok {
			if _, err := time.Parse(time.DateOnly, s); err != nil {
				v.add("verified.at", "%q is not a date that exists", s)
			}
		}
	}
	if h, ok := m["how"]; ok {
		v.str("verified.how", h, 1)
	}
}

func (v *validator) mcp(x any) {
	m, ok := v.object("mcp", x, "files", "format", "path")
	if !ok {
		return
	}
	if f, ok := m["files"]; ok {
		if l, ok := v.list("mcp.files", f, 1); ok {
			for i, item := range l {
				v.pathTemplate(join("mcp.files", i), item)
			}
		}
	}
	if f, ok := m["format"]; ok {
		v.enum("mcp.format", f, string(adapter.JSON), string(adapter.JSONC), string(adapter.TOML))
	}
	if p, ok := m["path"]; ok {
		v.strings("mcp.path", p, 1)
	}
}

// describe names the kind of a value for a message.
func describe(x any) string {
	switch x := x.(type) {
	case nil:
		return "empty"
	case string:
		return "the text " + strconv.Quote(x)
	case bool:
		return "a yes/no value"
	case int, int64, uint64, float64:
		return fmt.Sprintf("the number %v", x)
	case []any:
		return "a list"
	case map[string]any:
		return "a mapping"
	}
	return fmt.Sprintf("%T", x)
}
