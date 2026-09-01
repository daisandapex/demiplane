// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/daisandapex/demiplane/internal/render"
	"github.com/daisandapex/demiplane/internal/store"
)

// rerender.go owns the re-render path for ?render=md pages: POST /rerender/{slug}
// rebakes one page from the markdown the store retained at publish, and
// POST /rerender rebakes every page that has a retained source.
//
// Why: rendering happens once, at publish. Before source retention (store/source.go)
// the markdown was not kept, so improving the renderer meant re-publishing every
// page by hand from whatever copy of the source the operator could still find —
// and during the 2026-08-30 render overhaul five pages had no surviving copy at
// all. With the source retained, upgrading an instance is: install the new binary,
// POST /rerender.
//
// Scope, deliberately: this rebakes. It is not version history, it does not diff,
// and it does not edit. There is exactly one stored source per slug, and a rebake
// overwrites the page with what the CURRENT renderer makes of it.
//
// Reproducing the page: the theme, chrome flags and stylesheet come from the
// instance's CURRENT configuration, which is the entire point — that is what a
// renderer or theme upgrade changes. The per-publish arguments that the request
// carried (title/slug, series, reply box, forward flow) cannot be recovered from
// configuration, so they are captured at publish as a renderSpec and replayed
// here. The colophon's publish timestamp replays too: a rebake is not a
// re-publish and must not restamp the page as new.
//
// Legacy artifacts (published before retention, or never rendered) have no
// source. That is an expected state, not a failure: the single-slug route
// answers 200 with rerendered=false and a reason, and the bulk route counts them
// as skipped. An operator sweeping an old instance gets a report, not an error.
//
// Plane: control, next to /publish and /list, and behind the same bearer auth —
// it rewrites artifact bytes. The cross-origin write guard (ADR 0003) covers it
// automatically as a POST on the control handler, so a hosted artifact's JS
// cannot drive a rebake.
func init() {
	registerCoreControlRoute([]string{"rerender"}, func(mux *http.ServeMux, s *Server) {
		mux.HandleFunc("POST /rerender/{slug}", s.requireAuth(s.handleRerender))
		mux.HandleFunc("POST /rerender", s.requireAuth(s.handleRerenderAll))
	})
}

// renderSpec is the per-publish half of a rendered page's inputs — the part that
// comes from the publish REQUEST rather than from instance configuration. It is
// stored opaquely by the store (as PutOptions.RenderSpec) and is owned entirely
// here.
//
// Every field is optional on read: an unknown or empty spec rebakes with the
// defaults, which yields a correct page (minus series navigation or a reply box)
// rather than a failure. Fields are only ever ADDED, so an artifact published by
// an older binary still deserializes.
type renderSpec struct {
	// NamedSlug is the ?slug= the publish used, "" for an auto-generated slug.
	// It seeds the page title and the colophon's slug line, and an auto-generated
	// slug deliberately gets neither (it is not a stable link target).
	NamedSlug string `json:"named_slug,omitempty"`
	// Series is the explicit ?series= family, for colophon prev/next.
	Series string `json:"series,omitempty"`
	// ReplySlug is the slug the baked reply box posts to ("" = no reply box).
	ReplySlug string `json:"reply_slug,omitempty"`
	// ReplyNext is the ?next= forward-flow target ("" = none).
	ReplyNext string `json:"reply_next,omitempty"`
	// Published is the original publish time, replayed into the colophon so a
	// rebake does not restamp the page.
	Published time.Time `json:"published,omitempty"`
}

// marshalRenderSpec serializes a spec for storage. A marshal failure is not worth
// failing a publish over — the page is already rendered and correct; the cost is
// a later rebake that falls back to defaults — so it degrades to an empty spec
// and logs.
func marshalRenderSpec(spec renderSpec) string {
	b, err := json.Marshal(spec)
	if err != nil {
		log.Printf("render spec marshal failed (source retained without it): %v", err)
		return ""
	}
	return string(b)
}

// renderSpecFor builds the spec captured at publish. Kept beside the struct so
// the publish path and the rebake path cannot drift apart on field meaning.
func renderSpecFor(named, series, replySlug, replyNext string, published time.Time) renderSpec {
	return renderSpec{
		NamedSlug: named,
		Series:    series,
		ReplySlug: replySlug,
		ReplyNext: replyNext,
		Published: published,
	}
}

// rerenderResult reports what happened to one slug.
type rerenderResult struct {
	Slug string `json:"slug"`
	// Rerendered is false when the artifact was skipped (no retained source).
	Rerendered bool `json:"rerendered"`
	// Reason explains a skip; empty when Rerendered is true.
	Reason string `json:"reason,omitempty"`
	// Size is the rebaked page's byte length; 0 on a skip.
	Size int64 `json:"size,omitempty"`
}

const noSourceReason = "no retained markdown source (published before source retention, or not a ?render=md page)"

// handleRerender rebakes a single artifact.
func (s *Server) handleRerender(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")

	res, err := s.rerenderSlug(r, slug)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		log.Printf("rerender %q failed: %v", slug, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, res)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if !res.Rerendered {
		fmt.Fprintf(w, "skipped %s: %s\n", res.Slug, res.Reason)
		return
	}
	fmt.Fprintf(w, "rerendered %s (%d bytes)\n", res.Slug, res.Size)
}

// handleRerenderAll walks every stored artifact and rebakes the ones that
// retain a source, counting the rest as skipped — so the report an operator gets
// after upgrading an instance accounts for the whole store, not just the part
// that happened to be rebakeable. One artifact's
// failure does not abandon the sweep: it is reported and the sweep continues, so
// a single corrupt page cannot leave an instance half-upgraded with no account of
// which pages made it.
func (s *Server) handleRerenderAll(w http.ResponseWriter, r *http.Request) {
	slugs, err := s.store.Slugs(store.DefaultOwner)
	if err != nil {
		log.Printf("rerender sweep list failed: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	results := make([]rerenderResult, 0, len(slugs))
	var rerendered, skipped int
	failed := map[string]string{}
	for _, slug := range slugs {
		res, err := s.rerenderSlug(r, slug)
		if err != nil {
			log.Printf("rerender %q failed: %v", slug, err)
			failed[slug] = "rerender failed; see the server log"
			continue
		}
		results = append(results, res)
		if res.Rerendered {
			rerendered++
		} else {
			skipped++
		}
	}

	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, map[string]any{
			"rerendered": rerendered,
			"skipped":    skipped,
			"failed":     failed,
			"results":    results,
		})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "rerendered %d, skipped %d, failed %d\n", rerendered, skipped, len(failed))
	for _, res := range results {
		if res.Rerendered {
			fmt.Fprintf(w, "  rerendered %s (%d bytes)\n", res.Slug, res.Size)
		}
	}
	for slug, reason := range failed {
		fmt.Fprintf(w, "  failed %s: %s\n", slug, reason)
	}
}

// rerenderSlug rebakes one slug's retained source with the current renderer and
// swaps the result in. A missing source is reported as a skip (nil error); only a
// genuinely unknown slug (store.ErrNotFound) and real I/O failures are errors.
func (s *Server) rerenderSlug(r *http.Request, slug string) (rerenderResult, error) {
	rec, err := s.store.Source(slug)
	if errors.Is(err, store.ErrNoSource) {
		return rerenderResult{Slug: slug, Reason: noSourceReason}, nil
	}
	if err != nil {
		return rerenderResult{}, err
	}

	var spec renderSpec
	if rec.Spec != "" {
		if uerr := json.Unmarshal([]byte(rec.Spec), &spec); uerr != nil {
			// A spec we cannot read is not a reason to refuse to rebake: the
			// source is intact and the defaults produce a correct page. Log it
			// and carry on rather than leaving the artifact stuck on old output.
			log.Printf("rerender %q: unreadable render spec, using defaults: %v", slug, uerr)
			spec = renderSpec{}
		}
	}
	published := spec.Published
	if published.IsZero() {
		published = rec.CreatedAt
	}

	html := render.Markdown(rec.Body, render.Options{
		Theme:       s.renderTheme,
		CSS:         s.renderCSS,
		Title:       spec.NamedSlug,
		Header:      s.renderHeader,
		Footer:      s.renderFooter,
		FooterLink:  s.renderFooterLink,
		MetaHeader:  s.renderMetaHeader,
		ReplySlug:   spec.ReplySlug,
		ReplyNext:   spec.ReplyNext,
		Colophon:    true,
		Slug:        spec.NamedSlug,
		Published:   published,
		Siblings:    s.seriesSiblings(spec.Series, spec.NamedSlug),
		ContentBase: s.contentBase(r),
		IndexURL:    s.requestBase(r) + "/gallery",
	})

	art, err := s.store.ReplaceBody(slug, bytes.NewReader(html))
	if err != nil {
		return rerenderResult{}, err
	}
	return rerenderResult{Slug: slug, Rerendered: true, Size: art.Size}, nil
}
