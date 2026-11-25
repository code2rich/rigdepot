// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package access

// The events this package publishes. They carry ids, never secrets: an
// invitation's link is not in InviteCreated.

// ProductCreated: a product was created.
type ProductCreated struct{ ProductID, Code string }

// MembershipChanged: someone joined a product, left it or got another role
// in it. Delivery recomputes what their computers should have.
type MembershipChanged struct {
	ProductID, UserID string
	Old, New          Role // empty Old: joined; empty New: left
}

// JoinRequested: someone asked to join a product; its owners decide.
type JoinRequested struct{ RequestID, UserID, ProductID string }

// JoinDecided: a request to join a product was approved or rejected.
type JoinDecided struct {
	RequestID, UserID, ProductID string
	Approved                     bool
	Reason                       string // why it was rejected
}

// UserStatusChanged: an account was activated, disabled or restored.
type UserStatusChanged struct {
	UserID   string
	Old, New Status
}

// AdminChanged: someone became or stopped being a platform administrator.
type AdminChanged struct {
	UserID string
	Admin  bool
}

// InviteCreated: an invitation was created for an e-mail address.
type InviteCreated struct{ InviteID, Email string }

// EventName implements Event.
func (ProductCreated) EventName() string { return "access.product_created" }

// EventName implements Event.
func (MembershipChanged) EventName() string { return "access.membership_changed" }

// EventName implements Event.
func (JoinRequested) EventName() string { return "access.join_requested" }

// EventName implements Event.
func (JoinDecided) EventName() string { return "access.join_decided" }

// EventName implements Event.
func (UserStatusChanged) EventName() string { return "access.user_status_changed" }

// EventName implements Event.
func (AdminChanged) EventName() string { return "access.admin_changed" }

// EventName implements Event.
func (InviteCreated) EventName() string { return "access.invite_created" }
