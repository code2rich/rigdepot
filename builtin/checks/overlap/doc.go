// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package overlap is the check rule for skills an AI tool cannot tell apart. Advisory.
//
// An AI tool picks a skill by its description. When two skills that are published together
// describe themselves in nearly the same words, one request can pick either, and which one wins
// is down to chance. This rule compares a skill's description with those of the other skills in
// Input.Peers. Without peers, as when one resource is checked on its own, it has nothing to say.
//
// The comparison works on words for English and on pairs of adjacent characters for Chinese, so
// it does not need a dictionary: two descriptions overlap when most of what either says is said
// by the other (Jaccard similarity of the two sets at or above the threshold).
//
// Message keys, all under check.overlap.:
//
//	similar  the description is close to another skill's    {peer, similarity}  (similarity in percent)
package overlap
