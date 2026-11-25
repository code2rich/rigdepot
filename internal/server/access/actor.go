// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package access

import "context"

// Actor is who is acting: the person, where the request came from, the AI
// tool it came through and the credential it used. Every write of every
// domain service takes one; the audit log's "who" and "from where" come from
// it.
//
// Actor is also the principal of an HTTP request: the authentication
// middleware (package authn) puts it into the request's context with
// WithActor, and the HTTP layer takes it out with ActorFrom. It holds no
// permissions: those are read from the database on every decision, so that
// disabling an account or taking someone out of a product takes effect with
// the next request.
type Actor struct {
	UserID   string     // empty only for the system itself
	Origin   Origin     // web, cli or system
	ViaTool  string     // the AI tool the request came through, such as "claude-code"; empty on the web
	DeviceID string     // the computer, for requests with a device token
	TokenID  string     // the access token, for programs
	Scope    TokenScope // the access token's scope; empty when no access token is used
}

// Origin is where an action comes from.
type Origin string

// The origins of the audit log.
const (
	OriginWeb    Origin = "web"
	OriginCLI    Origin = "cli"
	OriginSystem Origin = "system"
)

// TokenScope is what an access token may be used for. What a token may do
// is its scope intersected with what the person who issued it may do at that
// moment.
type TokenScope string

// The scopes of access tokens. Publish includes read.
const (
	TokenRead    TokenScope = "read"
	TokenPublish TokenScope = "publish"
)

// System is the actor of background jobs and of the server itself. It may
// do everything and is recorded as "system" in the audit log.
var System = Actor{Origin: OriginSystem}

// IsSystem reports whether a is the server itself rather than a person.
func (a Actor) IsSystem() bool { return a.UserID == "" && a.Origin == OriginSystem }

type actorKey struct{}

// WithActor returns a context that carries a as the principal of the
// request.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// ActorFrom returns the principal of the request, and false when the request
// is not signed in.
func ActorFrom(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(actorKey{}).(Actor)
	if !ok || (a.UserID == "" && !a.IsSystem()) {
		return Actor{}, false
	}
	return a, true
}
