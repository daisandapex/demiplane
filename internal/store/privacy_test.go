// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"strings"
	"testing"
)

// TestHasPublicHidesPrivate locks the reply-oracle fix: HasPublic must report
// false for a private (capability-slug) artifact so a public surface (the reply
// form) cannot confirm a private artifact's existence via a 200-vs-404 oracle.
func TestHasPublicHidesPrivate(t *testing.T) {
	s := newTestStore(t)

	pub, err := s.Put(PutOptions{Slug: "public-one"}, strings.NewReader("x"))
	if err != nil {
		t.Fatalf("put public: %v", err)
	}
	priv, err := s.Put(PutOptions{Private: true}, strings.NewReader("x"))
	if err != nil {
		t.Fatalf("put private: %v", err)
	}

	if ok, err := s.HasPublic(pub.Slug); err != nil || !ok {
		t.Errorf("HasPublic(public) = %v (err %v), want true", ok, err)
	}
	if ok, err := s.HasPublic(priv.Slug); err != nil || ok {
		t.Errorf("HasPublic(private) = %v (err %v), want false — private existence leaked", ok, err)
	}
	if ok, err := s.HasPublic("no-such-slug"); err != nil || ok {
		t.Errorf("HasPublic(missing) = %v (err %v), want false", ok, err)
	}
}

// TestPrivateStickyOnRepublish locks the de-privatization fix: once an artifact
// is private, re-publishing to its slug without ?private (as an attacker who
// learned the capability slug would, in no-auth mode) must NOT flip it public or
// surface it in List.
func TestPrivateStickyOnRepublish(t *testing.T) {
	s := newTestStore(t)

	priv, err := s.Put(PutOptions{Private: true}, strings.NewReader("secret"))
	if err != nil {
		t.Fatalf("put private: %v", err)
	}
	if !priv.Private {
		t.Fatal("seed artifact is not private")
	}

	// Re-publish to the known slug as a plain (public) artifact.
	if _, err := s.Put(PutOptions{Slug: priv.Slug}, strings.NewReader("attacker-content")); err != nil {
		t.Fatalf("republish to known slug: %v", err)
	}

	// It must still read as private everywhere that privacy is enforced.
	got, f, err := s.Get(priv.Slug)
	if err != nil {
		t.Fatalf("get after republish: %v", err)
	}
	f.Close()
	if !got.Private {
		t.Error("artifact.Private = false after public republish — de-privatization not prevented")
	}
	if ok, err := s.HasPublic(priv.Slug); err != nil || ok {
		t.Errorf("HasPublic after republish = %v (err %v), want false", ok, err)
	}
	arts, err := s.List(DefaultOwner)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, a := range arts {
		if a.Slug == priv.Slug {
			t.Error("de-privatized artifact appeared in List — /list exposure not prevented")
		}
	}
}
