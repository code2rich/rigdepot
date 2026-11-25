// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// load parses and validates the contract. Validation includes every example
// against its schema.
func load(t *testing.T, data []byte) *openapi3.T {
	t.Helper()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(data)
	if err != nil {
		t.Fatalf("openapi.yaml does not load: %v", err)
	}
	if err := doc.Validate(context.Background(), openapi3.EnableExamplesValidation()); err != nil {
		t.Fatalf("openapi.yaml is not a valid OpenAPI document: %v", err)
	}
	return doc
}

func TestContractIsValid(t *testing.T) {
	load(t, Spec)
}

// operation is one operation with where it is.
type operation struct {
	method, path string
	op           *openapi3.Operation
}

func (o operation) String() string { return o.method + " " + o.path }

func operations(doc *openapi3.T) []operation {
	var out []operation
	for _, path := range slices.Sorted(maps.Keys(doc.Paths.Map())) {
		item := doc.Paths.Value(path)
		for _, method := range slices.Sorted(maps.Keys(item.Operations())) {
			out = append(out, operation{method: method, path: path, op: item.Operations()[method]})
		}
	}
	return out
}

func isJSON(mediaType string) bool {
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

// examplesOf returns the example values of one media type, by name. A media
// type without any example yields none.
func examplesOf(mt *openapi3.MediaType) map[string]any {
	out := map[string]any{}
	if mt.Example != nil {
		out["example"] = mt.Example
	}
	for name, ref := range mt.Examples {
		if ref != nil && ref.Value != nil {
			out[name] = ref.Value.Value
		}
	}
	return out
}

// checkExamples requires at least one example per media type and checks each
// JSON example against its schema, rejecting fields the schema does not
// define. kin-openapi lets unknown fields through, so a typo in an example
// would otherwise go unnoticed.
func checkExamples(t *testing.T, where string, content openapi3.Content, asRequest bool) {
	t.Helper()
	for _, mediaType := range slices.Sorted(maps.Keys(content)) {
		mt := content[mediaType]
		examples := examplesOf(mt)
		if len(examples) == 0 {
			t.Errorf("%s %s: no example", where, mediaType)
			continue
		}
		if !isJSON(mediaType) || mt.Schema == nil {
			continue
		}
		visit := openapi3.VisitAsResponse()
		if asRequest {
			visit = openapi3.VisitAsRequest()
		}
		for name, value := range examples {
			value = normalize(t, value)
			if err := mt.Schema.Value.VisitJSON(value, visit, openapi3.MultiErrors(), openapi3.EnableFormatValidation()); err != nil {
				t.Errorf("%s %s example %q does not match its schema: %v", where, mediaType, name, err)
			}
			for _, unknown := range unknownFields(mt.Schema.Value, value, "") {
				t.Errorf("%s %s example %q has a field its schema does not define: %s", where, mediaType, name, unknown)
			}
		}
	}
}

// normalize turns a value into what encoding/json produces, which is what
// the schema visitor expects.
func normalize(t *testing.T, v any) any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// unknownFields lists the paths of object fields in value that schema does
// not declare. Objects with additionalProperties accept anything and are only
// followed into declared properties.
func unknownFields(schema *openapi3.Schema, value any, at string) []string {
	if schema == nil {
		return nil
	}
	var out []string
	switch v := value.(type) {
	case map[string]any:
		for _, key := range slices.Sorted(maps.Keys(v)) {
			prop, declared := schema.Properties[key]
			switch {
			case declared:
				out = append(out, unknownFields(prop.Value, v[key], at+"."+key)...)
			case schema.AdditionalProperties.Schema != nil:
				out = append(out, unknownFields(schema.AdditionalProperties.Schema.Value, v[key], at+"."+key)...)
			case schema.AdditionalProperties.Has != nil && *schema.AdditionalProperties.Has:
			default:
				out = append(out, at+"."+key)
			}
		}
	case []any:
		if schema.Items != nil {
			for i, item := range v {
				out = append(out, unknownFields(schema.Items.Value, item, fmt.Sprintf("%s[%d]", at, i))...)
			}
		}
	}
	return out
}

func TestEveryOperationHasExamples(t *testing.T) {
	doc := load(t, Spec)
	for _, o := range operations(doc) {
		if body := o.op.RequestBody; body != nil {
			checkExamples(t, o.String()+" request", body.Value.Content, true)
		}
		success := 0
		for _, status := range slices.Sorted(maps.Keys(o.op.Responses.Map())) {
			resp := o.op.Responses.Value(status).Value
			if strings.HasPrefix(status, "2") {
				success++
			}
			checkExamples(t, o.String()+" response "+status, resp.Content, false)
		}
		if success == 0 && o.op.Responses.Value("302") == nil {
			t.Errorf("%s: no success response", o)
		}
		if o.op.Responses.Default() == nil {
			t.Errorf("%s: no default response; every operation can fail with a problem document", o)
		}
	}
}

var (
	operationID = regexp.MustCompile(`^[a-z][a-zA-Z0-9]+$`)
	pathSegment = regexp.MustCompile(`^([a-z0-9]+(-[a-z0-9]+)*|\{[a-z]+(_[a-z]+)*\})$`)
	jsonField   = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
)

// rootPaths are the only paths outside /api/v1: what an installer is fetched
// from.
var rootPaths = []string{"/install.sh", "/install.ps1", "/downloads/{file}"}

func TestConventions(t *testing.T) {
	doc := load(t, Spec)
	tags := map[string]bool{}
	for _, tag := range doc.Tags {
		tags[tag.Name] = true
	}
	ids := map[string]string{}
	for _, o := range operations(doc) {
		if !operationID.MatchString(o.op.OperationID) {
			t.Errorf("%s: operationId %q is not lowerCamelCase", o, o.op.OperationID)
		}
		if prev, dup := ids[o.op.OperationID]; dup {
			t.Errorf("%s: operationId %q is already used by %s", o, o.op.OperationID, prev)
		}
		ids[o.op.OperationID] = o.String()
		if o.op.Summary == "" {
			t.Errorf("%s: no summary", o)
		}
		if len(o.op.Tags) != 1 || !tags[o.op.Tags[0]] {
			t.Errorf("%s: needs exactly one of the declared tags, has %v", o, o.op.Tags)
		}
		if !strings.HasPrefix(o.path, "/api/v1/") && !slices.Contains(rootPaths, o.path) {
			t.Errorf("%s: outside /api/v1", o)
		}
		for _, seg := range strings.Split(strings.TrimPrefix(o.path, "/"), "/") {
			if !pathSegment.MatchString(seg) && !slices.Contains(rootPaths, o.path) {
				t.Errorf("%s: path segment %q is neither kebab-case nor a snake_case parameter", o, seg)
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(doc.Components.Schemas)) {
		for _, field := range fieldNames(doc.Components.Schemas[name].Value) {
			if !jsonField.MatchString(field) {
				t.Errorf("schema %s: field %q is not snake_case", name, field)
			}
		}
	}
}

func fieldNames(s *openapi3.Schema) []string {
	var out []string
	for name, prop := range s.Properties {
		out = append(out, name)
		if prop.Ref == "" && prop.Value != nil {
			out = append(out, fieldNames(prop.Value)...)
			if prop.Value.Items != nil && prop.Value.Items.Ref == "" {
				out = append(out, fieldNames(prop.Value.Items.Value)...)
			}
		}
	}
	sort.Strings(out)
	return out
}

// acceptsAccessToken reports whether an operation lists the accessToken
// scheme. Operations without their own list inherit the document's.
func security(doc *openapi3.T, op *openapi3.Operation) openapi3.SecurityRequirements {
	if op.Security != nil {
		return *op.Security
	}
	return doc.Security
}

func TestAuthentication(t *testing.T) {
	doc := load(t, Spec)
	for _, o := range operations(doc) {
		var schemes []string
		for _, req := range security(doc, o.op) {
			schemes = append(schemes, slices.Collect(maps.Keys(req))...)
		}
		scope, hasScope := o.op.Extensions["x-token-scope"]
		token := slices.Contains(schemes, "accessToken")
		switch {
		case token && !hasScope:
			t.Errorf("%s: accepts access tokens but has no x-token-scope", o)
		case !token && hasScope:
			t.Errorf("%s: has x-token-scope but does not accept access tokens", o)
		case token && scope != "read" && scope != "publish":
			t.Errorf("%s: x-token-scope is %v, want read or publish", o, scope)
		}
		if token && o.method != http.MethodGet && scope != "publish" {
			t.Errorf("%s: an access token may only change something with the publish scope", o)
		}
	}
}

// TestSecretsStayInBrowserOrTerminal pins the operations that carry a secret
// or a credential: an access token, which a program holds, must never be
// accepted there, and the ones the design reserves for the browser accept
// nothing else.
func TestSecretsStayInBrowserOrTerminal(t *testing.T) {
	doc := load(t, Spec)
	browserOnly := []string{
		"createAccessToken", "updateSettings", "createIdentityProvider", "updateIdentityProvider",
		"deleteIdentityProvider", "updateSource", "approveDeviceRequest", "denyDeviceRequest", "getDeviceRequest",
	}
	deviceOnly := []string{"getSealedSecrets", "reportSync"}
	for _, o := range operations(doc) {
		var schemes []string
		for _, req := range security(doc, o.op) {
			schemes = append(schemes, slices.Collect(maps.Keys(req))...)
		}
		sort.Strings(schemes)
		switch {
		case slices.Contains(browserOnly, o.op.OperationID):
			if !slices.Equal(schemes, []string{"session"}) {
				t.Errorf("%s: must accept only a browser session, accepts %v", o, schemes)
			}
		case slices.Contains(deviceOnly, o.op.OperationID):
			if !slices.Equal(schemes, []string{"device"}) {
				t.Errorf("%s: must accept only a device token, accepts %v", o, schemes)
			}
		case o.op.OperationID == "setMySecret" || o.op.OperationID == "deleteMySecret":
			if slices.Contains(schemes, "accessToken") {
				t.Errorf("%s: personal secrets are set in the browser or one's own terminal, never by a program", o)
			}
		}
	}
}

// TestConcurrencyControl checks that every operation guarded by If-Match
// documents both ways it can fail, and that the objects it changes say what
// their ETag is.
func TestConcurrencyControl(t *testing.T) {
	doc := load(t, Spec)
	guarded := 0
	for _, o := range operations(doc) {
		var ifMatch *openapi3.Parameter
		for _, p := range o.op.Parameters {
			if p.Value.In == openapi3.ParameterInHeader && p.Value.Name == "If-Match" {
				ifMatch = p.Value
			}
		}
		if ifMatch == nil {
			continue
		}
		guarded++
		for _, status := range []string{"412", "428"} {
			if o.op.Responses.Value(status) == nil {
				t.Errorf("%s: takes If-Match but does not document %s", o, status)
			}
		}
	}
	if guarded == 0 {
		t.Error("no operation takes If-Match")
	}
	for _, name := range []string{"ManagedResource", "Draft", "PlatformSettings", "Source", "IdentityProvider"} {
		if _, ok := doc.Components.Schemas[name].Value.Properties["etag"]; !ok {
			t.Errorf("schema %s is edited with If-Match but has no etag field", name)
		}
	}
}

// TestBrokenExamplesAreCaught makes sure the example checks are not vacuous:
// an example with a wrong type, a missing required field or an unknown field
// must each be reported.
func TestBrokenExamplesAreCaught(t *testing.T) {
	doc := load(t, Spec)
	schema := doc.Components.Schemas["Product"].Value
	good := map[string]any{"code": "shop", "name": "Shop", "owners": []any{}, "member_count": 1.0, "resource_count": 0.0}
	if err := schema.VisitJSON(good, openapi3.VisitAsResponse()); err != nil {
		t.Fatalf("a valid product is rejected: %v", err)
	}
	if unknown := unknownFields(schema, good, ""); len(unknown) != 0 {
		t.Fatalf("a valid product has unknown fields: %v", unknown)
	}
	wrongType := maps.Clone(good)
	wrongType["member_count"] = "many"
	missing := maps.Clone(good)
	delete(missing, "name")
	for name, v := range map[string]map[string]any{"wrong type": wrongType, "missing field": missing} {
		if err := schema.VisitJSON(v, openapi3.VisitAsResponse()); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	typo := maps.Clone(good)
	typo["membercount"] = 1.0
	if unknown := unknownFields(schema, typo, ""); !slices.Equal(unknown, []string{".membercount"}) {
		t.Errorf("unknown field not reported: %v", unknown)
	}
}
