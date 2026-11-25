// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package authnapi

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	"go.yaml.in/yaml/v4"

	"github.com/code2rich/rigdepot/api"
	"github.com/code2rich/rigdepot/internal/server/access"
)

// security is what an operation accepts, from its security list in the
// contract (or the contract's default list: a session or a device token).
type security struct {
	id       string // operationId
	public   bool   // nothing but {}: no credential is read at all
	optional bool   // {} among others: a credential is checked when there is one
	session  bool
	device   bool
	token    bool
	scope    access.TokenScope // x-token-scope: what an access token needs at least
}

// The names of the contract's security schemes.
const (
	schemeSession     = "session"
	schemeDevice      = "device"
	schemeAccessToken = "accessToken"
)

// operations maps the operations' route patterns ("POST /api/v1/auth/login",
// as the generated router registers them) to what they accept.
type operations struct {
	mux *http.ServeMux
	ops map[string]security // by pattern
}

// find returns the route pattern of the operation r is for and what it
// accepts; false for a request that is not an operation of the contract.
func (o *operations) find(r *http.Request) (string, security, bool) {
	_, pattern := o.mux.Handler(r)
	s, ok := o.ops[pattern]
	return pattern, s, ok
}

var (
	contractOnce sync.Once
	contractOps  *operations
	contractErr  error
)

// contract returns the operations of api/openapi.yaml as built into the
// program, read once. The security of each operation comes from the
// contract itself, so that the middleware cannot disagree with it.
func contract() (*operations, error) {
	contractOnce.Do(func() { contractOps, contractErr = parseSecurity(api.Spec) })
	return contractOps, contractErr
}

type requirement = map[string][]string

var methods = []string{"get", "put", "post", "delete", "patch", "head", "options"}

func parseSecurity(spec []byte) (*operations, error) {
	var doc struct {
		Security []requirement                   `yaml:"security"`
		Paths    map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(spec, &doc); err != nil {
		return nil, fmt.Errorf("authnapi: read the contract: %w", err)
	}
	out := &operations{mux: http.NewServeMux(), ops: map[string]security{}}
	nothing := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	for path, item := range doc.Paths {
		for _, m := range methods {
			node, ok := item[m]
			if !ok {
				continue
			}
			var op struct {
				OperationID string         `yaml:"operationId"`
				Security    *[]requirement `yaml:"security"`
				TokenScope  string         `yaml:"x-token-scope"`
			}
			if err := node.Decode(&op); err != nil {
				return nil, fmt.Errorf("authnapi: %s %s: %w", m, path, err)
			}
			reqs := doc.Security
			if op.Security != nil {
				reqs = *op.Security
			}
			s := security{id: op.OperationID, scope: access.TokenScope(op.TokenScope)}
			for _, req := range reqs {
				if len(req) == 0 {
					s.optional = true
				}
				for name := range req {
					switch name {
					case schemeSession:
						s.session = true
					case schemeDevice:
						s.device = true
					case schemeAccessToken:
						s.token = true
					default:
						return nil, fmt.Errorf("authnapi: %s: unknown security scheme %q", op.OperationID, name)
					}
				}
			}
			if len(reqs) == 0 {
				return nil, fmt.Errorf("authnapi: %s accepts no credential and is not marked public with {}", op.OperationID)
			}
			s.public = s.optional && !s.session && !s.device && !s.token
			if s.token {
				switch s.scope {
				case access.TokenRead, access.TokenPublish:
				default:
					return nil, fmt.Errorf("authnapi: %s accepts access tokens without a valid x-token-scope", op.OperationID)
				}
			}
			pattern := strings.ToUpper(m) + " " + path
			out.mux.Handle(pattern, nothing)
			out.ops[pattern] = s
		}
	}
	return out, nil
}

// covers reports whether an access token's scope is enough for need.
// Publish includes read.
func covers(have, need access.TokenScope) bool {
	switch have {
	case access.TokenPublish:
		return need == access.TokenRead || need == access.TokenPublish
	case access.TokenRead:
		return need == access.TokenRead
	}
	return false
}
