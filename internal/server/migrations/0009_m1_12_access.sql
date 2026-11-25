-- SPDX-License-Identifier: Apache-2.0
-- Copyright 2026 The Rigdepot Authors
--
-- Access (task M1-12): an invitation can name several products, and a person
-- has at most one pending request per product.

-- The products an invitation joins the new account to, as a member. This
-- replaces invites.product_id and invites.role, which stay unused: an
-- invitation names any number of products (CreateInvitesRequest.products).
CREATE TABLE invite_products (
    invite_id  TEXT NOT NULL REFERENCES invites (id),
    product_id TEXT NOT NULL REFERENCES products (id),
    PRIMARY KEY (invite_id, product_id)
);

-- Asking twice at the same moment still leaves one pending request.
CREATE UNIQUE INDEX join_requests_one_pending ON join_requests (user_id, product_id) WHERE status = 'pending';
