// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package access

import (
	"maps"
	"slices"
)

// Action is something a person can do on the platform. The values are the
// same names the audit log records (docs/architecture/06-server.md#权限): a
// new write operation is added here, with its row in rules, before it is
// written anywhere else.
type Action string

// Products and their members.
const (
	ActionProductRead    Action = "product.read"
	ActionProductCreate  Action = "product.create"
	ActionProductUpdate  Action = "product.update"
	ActionMemberRead     Action = "product.member.read"
	ActionMemberAdd      Action = "product.member.add"
	ActionMemberUpdate   Action = "product.member.update"
	ActionMemberRemove   Action = "product.member.remove"
	ActionJoinCreate     Action = "join_request.create"
	ActionJoinRead       Action = "join_request.read"
	ActionJoinApprove    Action = "join_request.approve"
	ActionJoinReject     Action = "join_request.reject"
	ActionUserRead       Action = "user.read"
	ActionUserProfile    Action = "user.profile.update"
	ActionUserDisable    Action = "user.disable"
	ActionUserEnable     Action = "user.enable"
	ActionUserAdminGrant Action = "user.admin.grant"
	ActionUserAdminDrop  Action = "user.admin.revoke"
	ActionUserCreate     Action = "user.create"
	ActionInviteCreate   Action = "invite.create"
	ActionInviteRead     Action = "invite.read"
	ActionInviteRevoke   Action = "invite.revoke"
	ActionInviteAccept   Action = "invite.accept"
)

// Resources, and the platform around them. The packages that own these
// operations (catalog, delivery, feedback, settings…) ask Can with them.
const (
	ActionResourceRead      Action = "resource.read"
	ActionResourceDraftRead Action = "resource.draft.read"
	ActionResourceCreate    Action = "resource.create"
	ActionResourceEdit      Action = "resource.edit"
	ActionResourcePublish   Action = "resource.publish"
	ActionResourceConfigure Action = "resource.configure"
	ActionResourceRollback  Action = "resource.rollback"
	ActionResourceDisable   Action = "resource.disable"
	ActionResourceEnable    Action = "resource.enable"
	ActionResourceWaive     Action = "resource.waive"
	ActionResourcePrefer    Action = "resource.prefer"
	ActionSourceManage      Action = "source.manage"
	ActionSecretDefine      Action = "secret.define"
	ActionFeedbackCreate    Action = "feedback.create"
	ActionFeedbackManage    Action = "feedback.manage"
	ActionAuditRead         Action = "audit.read"
	ActionSettingsManage    Action = "settings.manage"
	ActionAdapterManage     Action = "adapter.manage"
)

// reach is how far a platform administrator's grant goes.
type reach uint8

const (
	adminNone   reach = iota
	adminShared       // everything except another person's personal objects
	adminAll          // everything
)

// rule says who may do an action. A person may when at least one of the
// grants applies to them and the target. The columns are those of
// docs/architecture/06-server.md#权限: platform administrator, owner of the
// product, maintainer of the resource, member in scope, oneself.
type rule struct {
	admin      reach
	owner      bool // an owner of the target's product
	member     bool // a member or owner of the target's product
	anyOwner   bool // an owner of any product (to find people to add)
	inScope    bool // anyone who can see the target's scope: org - everyone active; product - its members; personal - its author
	maintainer bool // a maintainer of the target resource
	self       bool // the person the target belongs to
	anyone     bool // every active person
	pending    bool // also accounts that are not activated yet (status pending)
	write      bool // changes something: denials are recorded in the audit log
	// token is the access-token scope that is enough for this action; empty
	// means access tokens cannot do it at all.
	token TokenScope
}

// rules is the permission table. Keep one row per action and the rows in the
// order of the constants above; access/matrix_test.go spells out what every
// row means for each role and scope, and fails for an action it does not
// list.
var rules = map[Action]rule{
	ActionProductRead:    {anyone: true, pending: true, token: TokenRead},
	ActionProductCreate:  {admin: adminAll, write: true},
	ActionProductUpdate:  {admin: adminAll, write: true},
	ActionMemberRead:     {admin: adminAll, member: true, token: TokenRead},
	ActionMemberAdd:      {admin: adminAll, owner: true, write: true},
	ActionMemberUpdate:   {admin: adminAll, owner: true, write: true},
	ActionMemberRemove:   {admin: adminAll, owner: true, write: true},
	ActionJoinCreate:     {anyone: true, pending: true, write: true},
	ActionJoinRead:       {admin: adminAll, owner: true},
	ActionJoinApprove:    {admin: adminAll, owner: true, write: true},
	ActionJoinReject:     {admin: adminAll, owner: true, write: true},
	ActionUserRead:       {admin: adminAll, anyOwner: true, self: true, pending: true, token: TokenRead},
	ActionUserProfile:    {self: true, pending: true, write: true},
	ActionUserDisable:    {admin: adminAll, write: true},
	ActionUserEnable:     {admin: adminAll, write: true},
	ActionUserAdminGrant: {admin: adminAll, write: true},
	ActionUserAdminDrop:  {admin: adminAll, write: true},
	ActionUserCreate:     {admin: adminAll, write: true},
	ActionInviteCreate:   {admin: adminAll, write: true},
	ActionInviteRead:     {admin: adminAll},
	ActionInviteRevoke:   {admin: adminAll, write: true},
	// Accepting is done by someone who has no account yet: only the system
	// (the sign-in code, holding the one-time link) does it.
	ActionInviteAccept: {write: true},

	ActionResourceRead:      {admin: adminAll, inScope: true, maintainer: true, token: TokenRead},
	ActionResourceDraftRead: {admin: adminShared, owner: true, maintainer: true, self: true, token: TokenRead},
	ActionResourceCreate:    {admin: adminShared, owner: true, self: true, write: true, token: TokenPublish},
	ActionResourceEdit:      {admin: adminShared, owner: true, maintainer: true, self: true, write: true, token: TokenPublish},
	ActionResourcePublish:   {admin: adminShared, owner: true, maintainer: true, self: true, write: true, token: TokenPublish},
	ActionResourceConfigure: {admin: adminShared, owner: true, write: true},
	ActionResourceRollback:  {admin: adminShared, owner: true, self: true, write: true, token: TokenPublish},
	ActionResourceDisable:   {admin: adminAll, owner: true, self: true, write: true},
	ActionResourceEnable:    {admin: adminAll, owner: true, self: true, write: true},
	ActionResourceWaive:     {admin: adminShared, owner: true, maintainer: true, self: true, write: true, token: TokenPublish},
	ActionResourcePrefer:    {inScope: true, write: true},
	ActionSourceManage:      {admin: adminAll, write: true},
	ActionSecretDefine:      {admin: adminAll, write: true},
	ActionFeedbackCreate:    {anyone: true, write: true},
	ActionFeedbackManage:    {admin: adminShared, owner: true, maintainer: true, write: true},
	ActionAuditRead:         {admin: adminAll, token: TokenRead},
	ActionSettingsManage:    {admin: adminAll, write: true},
	ActionAdapterManage:     {admin: adminAll, write: true},
}

// Actions returns every action, sorted.
func Actions() []Action {
	return slices.Sorted(maps.Keys(rules))
}

// IsWrite reports whether an action changes something. Denied writes are
// recorded in the audit log.
func (a Action) IsWrite() bool { return rules[a].write }
