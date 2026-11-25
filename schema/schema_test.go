// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package schema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"go.yaml.in/yaml/v4"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// all lists every schema this package publishes.
var all = []string{Repo, Meta, Manifest, Profile}

// draft is the dialect every schema is written in.
const draft = "https://json-schema.org/draft/2020-12/schema"

// readSchema returns a schema as the generic JSON value it is.
func readSchema(t *testing.T, name string) map[string]any {
	t.Helper()
	data, err := fs.ReadFile(FS(), name)
	if err != nil {
		t.Fatalf("%s is not embedded: %v", name, err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("%s is not valid JSON: %v", name, err)
	}
	obj, ok := doc.(map[string]any)
	if !ok {
		t.Fatalf("%s is a %T, want an object", name, doc)
	}
	return obj
}

// compile registers every schema under its $id, the way a reader of these formats has to, and
// returns the compiled one. Nothing is fetched: a reference that is not registered is an error.
func compile(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	var id string
	for _, n := range all {
		doc := readSchema(t, n)
		url, _ := doc["$id"].(string)
		if url == "" {
			t.Fatalf("%s has no $id", n)
		}
		if err := c.AddResource(url, doc); err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		if n == name {
			id = url
		}
	}
	sch, err := c.Compile(id)
	if err != nil {
		t.Fatalf("%s does not compile: %v", name, err)
	}
	return sch
}

func TestEverySchemaIsPublishedUnderOneBaseAddress(t *testing.T) {
	embedded := map[string]bool{}
	err := fs.WalkDir(FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			embedded[p] = true
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var base string
	for _, name := range all {
		if !embedded[name] {
			t.Errorf("%s is listed but not embedded", name)
			continue
		}
		delete(embedded, name)
		doc := readSchema(t, name)
		if got := doc["$schema"]; got != draft {
			t.Errorf("%s: $schema is %v, want %s", name, got, draft)
		}
		id, _ := doc["$id"].(string)
		b, ok := strings.CutSuffix(id, "/schema/"+name)
		if !ok || !strings.HasPrefix(b, "https://") {
			t.Errorf("%s: $id is %q; it has to be an https address that ends in /schema/%s", name, id, name)
			continue
		}
		if base == "" {
			base = b
		} else if b != base {
			t.Errorf("%s is published under %s, the others under %s", name, b, base)
		}
		for _, key := range []string{"title", "description"} {
			if s, _ := doc[key].(string); s == "" {
				t.Errorf("%s has no %s; it is what an editor shows the person writing the file", name, key)
			}
		}
		compile(t, name)
	}
	for name := range embedded {
		t.Errorf("%s is embedded but has no constant in this package and no fixtures", name)
	}
}

// An older version of the programs must be able to read what a newer one wrote, and teams may
// add fields of their own. A schema that closes an object breaks both.
func TestNoSchemaForbidsUnknownFields(t *testing.T) {
	var walk func(name, at string, v any)
	walk = func(name, at string, v any) {
		switch v := v.(type) {
		case map[string]any:
			for _, key := range []string{"additionalProperties", "unevaluatedProperties"} {
				if v[key] == false {
					t.Errorf("%s: %s/%s is false; unknown fields have to stay allowed", name, at, key)
				}
			}
			for key, child := range v {
				walk(name, at+"/"+key, child)
			}
		case []any:
			for i, child := range v {
				walk(name, fmt.Sprintf("%s/%d", at, i), child)
			}
		}
	}
	for _, name := range all {
		walk(name, "#", readSchema(t, name))
	}
}

// fixtureDir returns the directory with the samples of a schema: testdata/<schema>/ next to it,
// where <schema> is the file name without ".schema.json".
func fixtureDir(name string) string {
	short := strings.TrimSuffix(path.Base(name), ".schema.json")
	return filepath.Join(filepath.FromSlash(path.Dir(name)), "testdata", short)
}

// loadFixture reads a sample into the generic value a validator takes.
func loadFixture(file string) (any, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	switch filepath.Ext(file) {
	case ".json":
		return jsonschema.UnmarshalJSON(bytes.NewReader(data))
	case ".yaml":
		return loadYAML(data)
	}
	return nil, fmt.Errorf("fixtures are .yaml or .json files")
}

// loadYAML reads one YAML document into the JSON data model: maps with string keys, lists,
// strings, numbers, booleans and null.
//
// A date written without quotes (at: 2026-11-24) is a timestamp to a YAML reader. JSON has no
// such type and the formats describe these fields as strings, so the text is kept as written.
func loadYAML(data []byte) (any, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Kind == yaml.DocumentNode {
		if len(doc.Content) != 1 {
			return nil, fmt.Errorf("want exactly one YAML document")
		}
		return fromYAML(doc.Content[0])
	}
	return fromYAML(&doc)
}

func fromYAML(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.MappingNode:
		out := make(map[string]any, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, value := n.Content[i], n.Content[i+1]
			if key.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("line %d: a key has to be a plain value", key.Line)
			}
			if _, dup := out[key.Value]; dup {
				return nil, fmt.Errorf("line %d: the key %q appears twice", key.Line, key.Value)
			}
			v, err := fromYAML(value)
			if err != nil {
				return nil, err
			}
			out[key.Value] = v
		}
		return out, nil
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, item := range n.Content {
			v, err := fromYAML(item)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case yaml.ScalarNode:
		var v any
		if err := n.Decode(&v); err != nil {
			return nil, fmt.Errorf("line %d: %w", n.Line, err)
		}
		if _, isTime := v.(time.Time); isTime {
			return n.Value, nil
		}
		return v, nil
	}
	return nil, fmt.Errorf("line %d: aliases and anchors are not used in these formats", n.Line)
}

// problem is one place where a sample does not match its schema.
type problem struct {
	at   []string // location in the sample
	kind jsonschema.ErrorKind
	leaf bool // nothing more specific below it
}

func (p problem) String() string {
	return fmt.Sprintf("/%s: %T %+v", strings.Join(p.at, "/"), p.kind, p.kind)
}

func problems(err error) []problem {
	var verr *jsonschema.ValidationError
	if !errors.As(err, &verr) {
		return nil
	}
	var out []problem
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		out = append(out, problem{at: e.InstanceLocation, kind: e.ErrorKind, leaf: len(e.Causes) == 0})
		if _, isName := e.ErrorKind.(*kind.PropertyNames); isName {
			// What lies below says why the name is refused, and is located inside the name
			// instead of inside the sample.
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(verr)
	return out
}

// names reports whether a problem is about the given field: it sits exactly there, or it is the
// parent's complaint that this field is missing or that its name is not allowed.
func (p problem) names(field []string) bool {
	if len(field) == 0 {
		return false
	}
	parent, last := field[:len(field)-1], field[len(field)-1]
	switch k := p.kind.(type) {
	case *kind.Required:
		return slices.Equal(p.at, parent) && slices.Contains(k.Missing, last)
	case *kind.PropertyNames:
		// The validator does not report where the object with the refused name is, so the
		// name alone has to identify it.
		return k.Property == last
	case *kind.AnyOf:
		return slices.Equal(p.at, field)
	}
	return p.leaf && slices.Equal(p.at, field)
}

// belongsTo reports whether a problem is a consequence of the one fault a sample has: it lies at
// or below the field, or is the parent's complaint about it.
func (p problem) belongsTo(field []string) bool {
	if p.names(field) {
		return true
	}
	if len(p.at) >= len(field) && slices.Equal(p.at[:len(field)], field) {
		return true
	}
	// The wrappers above the fault (the root, a $ref, an allOf) only say that something below
	// them failed.
	return !p.leaf
}

func TestFixtures(t *testing.T) {
	for _, name := range all {
		t.Run(name, func(t *testing.T) {
			sch := compile(t, name)
			dir := fixtureDir(name)

			valid := fixtureFiles(t, filepath.Join(dir, "valid"))
			for _, file := range valid {
				doc, err := loadFixture(file)
				if err != nil {
					t.Errorf("%s: %v", file, err)
					continue
				}
				if err := sch.Validate(doc); err != nil {
					t.Errorf("%s has to pass and does not:\n%v", file, err)
				}
			}

			invalid := fixtureFiles(t, filepath.Join(dir, "invalid"))
			for _, file := range invalid {
				stem := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
				fieldName, reason, ok := strings.Cut(stem, "--")
				if !ok || fieldName == "" || reason == "" {
					t.Errorf("%s: name an invalid sample <field>--<what is wrong>, such as stages.1.id--upper-case", file)
					continue
				}
				field := strings.Split(fieldName, ".")
				doc, err := loadFixture(file)
				if err != nil {
					t.Errorf("%s: the sample has to be well-formed, so that it is the schema that refuses it: %v", file, err)
					continue
				}
				err = sch.Validate(doc)
				if err == nil {
					t.Errorf("%s has to fail and passes", file)
					continue
				}
				found := problems(err)
				named := false
				for _, p := range found {
					named = named || p.names(field)
					if !p.belongsTo(field) {
						t.Errorf("%s: a sample shows one fault, at %s, but this one also fails at %s", file, fieldName, p)
					}
				}
				if !named {
					t.Errorf("%s fails, but not at %s:\n%v", file, fieldName, err)
				}
			}
		})
	}
}

// fixtureFiles returns the samples in a directory. A schema without samples is not tested, so
// an empty or missing directory fails.
func fixtureFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("every schema needs samples: %v", err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("%s: samples are files, not directories", filepath.Join(dir, e.Name()))
			continue
		}
		files = append(files, filepath.Join(dir, e.Name()))
	}
	if len(files) == 0 {
		t.Fatalf("%s is empty; every schema needs samples", dir)
	}
	sort.Strings(files)
	return files
}

// dig follows keys into a schema.
func dig(t *testing.T, doc map[string]any, keys ...string) any {
	t.Helper()
	var cur any = doc
	for i, key := range keys {
		obj, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("schema has no object at /%s", strings.Join(keys[:i], "/"))
		}
		if cur, ok = obj[key]; !ok {
			t.Fatalf("schema has nothing at /%s", strings.Join(keys[:i+1], "/"))
		}
	}
	return cur
}

func stringsOf(t *testing.T, v any) []string {
	t.Helper()
	list, ok := v.([]any)
	if !ok {
		t.Fatalf("want a list, got %T", v)
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		out = append(out, fmt.Sprint(item))
	}
	sort.Strings(out)
	return out
}

// The schemas and the Go types in ext/ describe the same formats. Wherever both spell out a
// rule, they have to spell out the same one.
func TestSchemasAgreeWithTheGoTypes(t *testing.T) {
	meta := readSchema(t, Meta)
	manifest := readSchema(t, Manifest)
	profile := readSchema(t, Profile)

	if got := dig(t, meta, "$defs", "slug", "pattern"); got != resource.CodePattern {
		t.Errorf("code pattern: schema %q, resource.CodePattern %q", got, resource.CodePattern)
	}
	if got := fmt.Sprint(dig(t, meta, "$defs", "code", "minLength")); got != fmt.Sprint(resource.MinCodeLength) {
		t.Errorf("shortest code: schema %s, resource.MinCodeLength %d", got, resource.MinCodeLength)
	}
	if got := fmt.Sprint(dig(t, meta, "$defs", "code", "maxLength")); got != fmt.Sprint(resource.MaxCodeLength) {
		t.Errorf("longest code: schema %s, resource.MaxCodeLength %d", got, resource.MaxCodeLength)
	}
	if got := dig(t, meta, "$defs", "secretName", "pattern"); got != resource.SecretNamePattern {
		t.Errorf("secret name pattern: schema %q, resource.SecretNamePattern %q", got, resource.SecretNamePattern)
	}
	for _, at := range [][]string{{"properties", "resource"}} {
		entry := dig(t, manifest, "$defs", "entry").(map[string]any)
		if got := dig(t, entry, append(at, "pattern")...); got != resource.CodePattern {
			t.Errorf("manifest %v: pattern %q, resource.CodePattern %q", at, got, resource.CodePattern)
		}
		if got := fmt.Sprint(dig(t, entry, append(at, "maxLength")...)); got != fmt.Sprint(resource.MaxCodeLength) {
			t.Errorf("manifest %v: longest code %s, resource.MaxCodeLength %d", at, got, resource.MaxCodeLength)
		}
	}

	enums := []struct {
		what string
		got  any
		want []string
	}{
		{"policies", dig(t, meta, "$defs", "policy", "enum"),
			[]string{string(resource.PolicyRequired), string(resource.PolicyRecommended), string(resource.PolicyOptional)}},
		{"audiences", dig(t, meta, "$defs", "audience", "enum"),
			[]string{string(resource.AudienceRD), string(resource.AudienceMgmt)}},
		{"profile formats", dig(t, profile, "properties", "mcp", "properties", "format", "enum"),
			[]string{string(adapter.JSON), string(adapter.JSONC), string(adapter.TOML)}},
		{"manifest formats", dig(t, manifest, "$defs", "entry", "properties", "format", "enum"),
			[]string{string(adapter.JSON), string(adapter.JSONC), string(adapter.TOML)}},
		{"skill layouts", dig(t, profile, "properties", "skills", "properties", "layout", "enum"),
			[]string{adapter.LayoutDir}},
	}
	for _, e := range enums {
		want := slices.Clone(e.want)
		sort.Strings(want)
		if got := stringsOf(t, e.got); !slices.Equal(got, want) {
			t.Errorf("%s: schema %v, Go %v", e.what, got, want)
		}
	}

	// The two reserved scopes cannot be product codes.
	reserved := stringsOf(t, dig(t, readSchema(t, Repo), "properties", "products", "items", "properties", "code", "not", "enum"))
	if want := []string{string(resource.ScopeMe), string(resource.ScopeOrg)}; !slices.Equal(reserved, want) {
		t.Errorf("reserved product codes: schema %v, Go %v", reserved, want)
	}

	// Every default stage is a well-formed stage identifier.
	stageID := compileFragment(t, Meta, "#/$defs/stageId")
	for _, s := range resource.DefaultStages() {
		if err := stageID.Validate(s.ID); err != nil {
			t.Errorf("default stage %q is not a valid stage identifier: %v", s.ID, err)
		}
	}
}

func compileFragment(t *testing.T, name, fragment string) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	var id string
	for _, n := range all {
		doc := readSchema(t, n)
		url := doc["$id"].(string)
		if err := c.AddResource(url, doc); err != nil {
			t.Fatal(err)
		}
		if n == name {
			id = url
		}
	}
	sch, err := c.Compile(id + fragment)
	if err != nil {
		t.Fatal(err)
	}
	return sch
}

// jsonFields returns the JSON names of a struct's fields.
func jsonFields(v any) []string {
	typ := reflect.TypeOf(v)
	var out []string
	for i := range typ.NumField() {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// A field that exists in a schema and not in the Go type is silently dropped when a file is
// read; one that exists only in the Go type can never be written. Both sides list the same
// fields.
func TestSchemasAndGoTypesListTheSameFields(t *testing.T) {
	repo := readSchema(t, Repo)
	meta := readSchema(t, Meta)
	profile := readSchema(t, Profile)
	cases := []struct {
		what   string
		schema any
		typ    any
	}{
		{"resource.Meta", dig(t, meta, "properties"), resource.Meta{}},
		{"resource.Text", dig(t, meta, "$defs", "text", "properties"), resource.Text{}},
		{"resource.Stage", dig(t, repo, "properties", "stages", "items", "properties"), resource.Stage{}},
		{"adapter.Profile", dig(t, profile, "properties"), adapter.Profile{}},
		{"adapter.Detect", dig(t, profile, "properties", "detect", "properties"), adapter.Detect{}},
		{"adapter.VersionProbe", dig(t, profile, "properties", "detect", "properties", "version", "properties"), adapter.VersionProbe{}},
		{"adapter.Root", dig(t, profile, "properties", "roots", "additionalProperties", "properties"), adapter.Root{}},
		{"adapter.Verification", dig(t, profile, "properties", "verified", "properties"), adapter.Verification{}},
		{"adapter.SkillsTarget", dig(t, profile, "properties", "skills", "properties"), adapter.SkillsTarget{}},
		{"adapter.FilesTarget", dig(t, profile, "$defs", "filesTarget", "properties"), adapter.FilesTarget{}},
		{"adapter.MCPTarget", dig(t, profile, "properties", "mcp", "properties"), adapter.MCPTarget{}},
		{"adapter.SharedDir", dig(t, profile, "properties", "alsoReads", "items", "properties"), adapter.SharedDir{}},
	}
	for _, c := range cases {
		props, ok := c.schema.(map[string]any)
		if !ok {
			t.Fatalf("%s: the schema has no properties there", c.what)
		}
		var inSchema []string
		for name := range props {
			inSchema = append(inSchema, name)
		}
		sort.Strings(inSchema)
		if inGo := jsonFields(c.typ); !slices.Equal(inSchema, inGo) {
			t.Errorf("%s:\n schema lists %v\n Go lists     %v", c.what, inSchema, inGo)
		}
	}
}

// Every valid sample is a file somebody could really write, so it has to land in the Go type
// with nothing lost. Samples that show unknown fields are read the lenient way, as the programs
// do.
func TestValidFixturesDecodeIntoTheGoTypes(t *testing.T) {
	cases := []struct {
		schema string
		into   func() any
	}{
		{Meta, func() any { return &resource.Meta{} }},
		{Profile, func() any { return &adapter.Profile{} }},
	}
	for _, c := range cases {
		for _, file := range fixtureFiles(t, filepath.Join(fixtureDir(c.schema), "valid")) {
			doc, err := loadFixture(file)
			if err != nil {
				t.Errorf("%s: %v", file, err)
				continue
			}
			asJSON, err := json.Marshal(doc)
			if err != nil {
				t.Errorf("%s: %v", file, err)
				continue
			}
			dec := json.NewDecoder(bytes.NewReader(asJSON))
			if !strings.HasPrefix(filepath.Base(file), "unknown-fields") {
				dec.DisallowUnknownFields()
			}
			if err := dec.Decode(c.into()); err != nil {
				t.Errorf("%s passes the schema but does not decode into the Go type: %v", file, err)
			}
		}
	}
}
