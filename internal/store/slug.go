// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
	"regexp"
)

// namedSlugRe constrains user-supplied (?slug=) names to a URL-safe, single
// path segment. No '/', so path traversal is structurally impossible; the
// explicit "." / ".." checks below close the dot-only cases.
var namedSlugRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// reservedSlugs are names that collide with API routes and must not be claimed
// as artifact slugs.
var reservedSlugs = map[string]bool{
	"publish":   true,
	"list":      true,
	"docs":      true,
	"help":      true,
	"help.json": true,
	"llms.txt":  true,
}

// AddReservedSlugs registers additional names that must not be claimed as
// artifact slugs. Modules that mount top-level routes call it (via core, at
// startup) so an artifact can never shadow a module route. NOT safe for
// concurrent use: invoke once during startup, before serving.
func AddReservedSlugs(names ...string) {
	for _, n := range names {
		if n != "" {
			reservedSlugs[n] = true
		}
	}
}

// InputError marks a client-input problem (bad slug, invalid option combination)
// that callers should surface as HTTP 400 — distinct from an internal server or
// storage fault, which is a 500. Classify with errors.As, not string matching.
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

func inputErrorf(format string, a ...any) *InputError {
	return &InputError{Msg: fmt.Sprintf(format, a...)}
}

// ValidateNamedSlug reports whether name is acceptable as a stable, named slug.
// All rejections are *InputError so the HTTP layer maps them to 400.
func ValidateNamedSlug(name string) error {
	if name == "" {
		return inputErrorf("slug is empty")
	}
	if name == "." || name == ".." {
		return inputErrorf("slug %q is a reserved path name", name)
	}
	if reservedSlugs[name] {
		return inputErrorf("slug %q is reserved (it collides with a built-in route)", name)
	}
	if !namedSlugRe.MatchString(name) {
		return inputErrorf("slug %q is not URL-safe (allowed: letters, digits, '.', '_', '-'; max 128 chars)", name)
	}
	return nil
}

// Friendly slugs are "adjective-creature" pairs (D&D-themed) drawn from the
// embedded wordlists below (slug_words.go) — e.g. shadow-specter, radiant-owlbear.
// They are memorable and typeable for the one-shot "drop an index.html, bookmark
// the URL" case.
//
// CAVEAT (documented in README): friendly slugs are LOW-ENTROPY. With the
// embedded lists the keyspace is large enough to avoid accidental collisions on
// a trusted network, but it is NOT a capability-URL secret — do not treat a
// friendly slug as an unguessable link. High-entropy, unguessable slugs are the
// ?private=true capability-slug mode, not this.

// randInt returns a uniformly random int in [0, n) using crypto/rand.
func randInt(n int) (int, error) {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0, err
	}
	return int(v.Int64()), nil
}

// pick returns a random element of words.
func pick(words []string) (string, error) {
	i, err := randInt(len(words))
	if err != nil {
		return "", err
	}
	return words[i], nil
}

// GenerateFriendlySlug builds one candidate "adjective-creature" slug.
// Collision handling lives in the store (it knows what already exists).
func GenerateFriendlySlug() (string, error) {
	adj, err := pick(adjectives)
	if err != nil {
		return "", err
	}
	creature, err := pick(creatures)
	if err != nil {
		return "", err
	}
	return adj + "-" + creature, nil
}

// capabilitySlugBytes is the entropy of a capability (private) slug. 18 bytes
// = 144 bits, base64url-encoded to 24 unguessable characters — a real
// "anyone with the link" secret, unlike the low-entropy friendly slug.
const capabilitySlugBytes = 18

// GenerateCapabilitySlug returns a high-entropy, URL-safe slug for private
// artifacts. The slug itself is the secret (capability URL), so it is generated
// from crypto/rand and is computationally unguessable.
func GenerateCapabilitySlug() (string, error) {
	b := make([]byte, capabilitySlugBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate capability slug: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// generateFriendlySlugSuffixed appends a short numeric suffix, used when the
// base keyspace is crowded enough that plain pairs keep colliding.
func generateFriendlySlugSuffixed() (string, error) {
	base, err := GenerateFriendlySlug()
	if err != nil {
		return "", err
	}
	// 4-digit suffix: enough headroom once the unsuffixed space saturates.
	suffix, err := randInt(10000)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%04d", base, suffix), nil
}
