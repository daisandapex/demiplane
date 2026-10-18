// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// oneFriendlySlug makes every friendly draw the same base slug for the test.
func oneFriendlySlug(t *testing.T) {
	t.Helper()
	oldA, oldC := adjectives, creatures
	adjectives, creatures = []string{"solo"}, []string{"slug"}
	t.Cleanup(func() { adjectives, creatures = oldA, oldC })
}

// readBody returns slug's stored bytes.
func readBody(t *testing.T, s *Store, slug string) string {
	t.Helper()
	_, f, err := s.Get(slug)
	if err != nil {
		t.Fatalf("get %s: %v", slug, err)
	}
	defer f.Close()
	b, _ := io.ReadAll(f)
	return string(b)
}

// TestConcurrentGeneratedPublishesGetDistinctSlugs: two unnamed publishes
// that draw the same slug and are both past allocation before either
// commits do not end up sharing it: each gets its own slug and keeps its own
// bytes. Run with one Store and with two Stores on one directory (as serve
// and SSH receive would be).
func TestConcurrentGeneratedPublishesGetDistinctSlugs(t *testing.T) {
	for _, handles := range []int{1, 2} {
		t.Run(map[int]string{1: "one store", 2: "two stores"}[handles], func(t *testing.T) {
			oneFriendlySlug(t)
			dir := t.TempDir()
			stores := make([]*Store, handles)
			for i := range stores {
				s, err := Open(dir)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { s.Close() })
				stores[i] = s
			}
			// Hold both publishes after allocation and the write, before the
			// commit, until both are there.
			var arrived sync.WaitGroup
			arrived.Add(2)
			beforeMetaCommit = func(string) {
				arrived.Done()
				arrived.Wait()
			}
			t.Cleanup(func() { beforeMetaCommit = nil })

			bodies := []string{"first body", "second body"}
			arts := make([]Artifact, 2)
			errs := make([]error, 2)
			var done sync.WaitGroup
			for i := range bodies {
				done.Add(1)
				go func() {
					defer done.Done()
					arts[i], errs[i] = stores[i%handles].Put(PutOptions{}, strings.NewReader(bodies[i]))
				}()
			}
			done.Wait()
			for i, err := range errs {
				if err != nil {
					t.Fatalf("publish %d: %v", i, err)
				}
			}
			if arts[0].Slug == arts[1].Slug {
				t.Fatalf("both publishes were given %q", arts[0].Slug)
			}
			for i, a := range arts {
				if got := readBody(t, stores[0], a.Slug); got != bodies[i] {
					t.Errorf("%s serves %q, want %q", a.Slug, got, bodies[i])
				}
			}
		})
	}
}

// TestGeneratedPublishMovesWhenANamedPublishTakesItsSlug: a named publish may
// take a slug a generated publish has reserved but not committed. The named
// one owns the slug; the generated one commits under a new slug with its own
// bytes and retained source, and no write intent is left behind.
func TestGeneratedPublishMovesWhenANamedPublishTakesItsSlug(t *testing.T) {
	oneFriendlySlug(t)
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	held, release := make(chan string), make(chan struct{})
	var first atomic.Bool
	beforeMetaCommit = func(slug string) {
		// Only the generated publish (the first to get here) waits; the named
		// one passes straight through.
		if first.CompareAndSwap(false, true) {
			held <- slug
			<-release
		}
	}
	t.Cleanup(func() { beforeMetaCommit = nil })

	type result struct {
		art Artifact
		err error
	}
	gen := make(chan result, 1)
	go func() {
		a, err := s.Put(PutOptions{Source: []byte("# generated"), RenderSpec: "{}"}, strings.NewReader("generated body"))
		gen <- result{a, err}
	}()
	reserved := <-held
	named, err := s.Put(PutOptions{Slug: reserved}, strings.NewReader("named body"))
	if err != nil {
		t.Fatalf("named publish to %s: %v", reserved, err)
	}
	close(release)
	r := <-gen
	if r.err != nil {
		t.Fatalf("generated publish: %v", r.err)
	}
	if r.art.Slug == named.Slug {
		t.Fatalf("the generated publish committed over the named one at %s", named.Slug)
	}
	if got := readBody(t, s, named.Slug); got != "named body" {
		t.Errorf("%s serves %q, want the named body", named.Slug, got)
	}
	if got := readBody(t, s, r.art.Slug); got != "generated body" {
		t.Errorf("%s serves %q, want the generated body", r.art.Slug, got)
	}
	if rec, err := s.Source(r.art.Slug); err != nil || string(rec.Body) != "# generated" {
		t.Errorf("generated publish's source at %s: %q, %v", r.art.Slug, rec.Body, err)
	}
	if r.art.Filename != r.art.Slug {
		t.Errorf("generated publish's default filename %q does not follow its slug %q", r.art.Filename, r.art.Slug)
	}
	var pending int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pending_writes`).Scan(&pending); err != nil || pending != 0 {
		t.Errorf("write intents left: %d (%v)", pending, err)
	}
}

// TestConcurrentPrivatePublishesStayDistinct: generated capability slugs go
// through the same reservation; many concurrent private publishes all get
// distinct slugs and keep their bytes.
func TestConcurrentPrivatePublishesStayDistinct(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	const n = 16
	arts := make([]Artifact, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st := a
			if i%2 == 1 {
				st = b
			}
			art, err := st.Put(PutOptions{Private: true}, strings.NewReader(strings.Repeat("x", i+1)))
			if err != nil {
				t.Error(err)
				return
			}
			arts[i] = art
		}()
	}
	wg.Wait()
	seen := map[string]bool{}
	for i, art := range arts {
		if seen[art.Slug] {
			t.Fatalf("slug %s given twice", art.Slug)
		}
		seen[art.Slug] = true
		if got := readBody(t, a, art.Slug); got != strings.Repeat("x", i+1) {
			t.Errorf("%s holds %d bytes, want %d", art.Slug, len(got), i+1)
		}
	}
}

// relocateGenerated publishes, under a quota, a generated artifact (with a
// retained source) whose reserved slug a named publish takes before it
// commits, so it is moved to a new slug. during, when set, runs after the
// named publish and before the generated one is released. It returns the
// relocated and the named artifacts.
func relocateGenerated(t *testing.T, s *Store, during func(release func())) (Artifact, Artifact) {
	t.Helper()
	held, release := make(chan string), make(chan struct{})
	var first atomic.Bool
	beforeMetaCommit = func(slug string) {
		if first.CompareAndSwap(false, true) {
			held <- slug
			<-release
		}
	}
	t.Cleanup(func() { beforeMetaCommit = nil })
	type result struct {
		art Artifact
		err error
	}
	gen := make(chan result, 1)
	go func() {
		a, err := s.Put(PutOptions{Source: []byte("# generated source"), RenderSpec: "{}"}, strings.NewReader("generated body"))
		gen <- result{a, err}
	}()
	reserved := <-held
	named, err := s.Put(PutOptions{Slug: reserved}, strings.NewReader("named body"))
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	rel := func() { once.Do(func() { close(release) }) }
	if during != nil {
		during(rel)
	}
	rel()
	r := <-gen
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.art.Slug == named.Slug {
		t.Fatalf("not relocated: both at %s", named.Slug)
	}
	return r.art, named
}

// TestRelocatedGeneratedPublishIsAccounted: the files of a generated publish
// moved to a new slug stay counted under their new names, so deleting the
// artifact releases them and usage matches what is left.
func TestRelocatedGeneratedPublishIsAccounted(t *testing.T) {
	oneFriendlySlug(t)
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.SetQuota(1 << 20); err != nil {
		t.Fatal(err)
	}
	moved, named := relocateGenerated(t, s, nil)
	assertAccounted(t, s)
	if err := s.Delete(moved.Slug); err != nil {
		t.Fatal(err)
	}
	assertAccounted(t, s)
	if err := s.Delete(named.Slug); err != nil {
		t.Fatal(err)
	}
	assertAccounted(t, s)
}

// TestRecountOverlappingARelocationIsNotApplied: a recount that walked the
// generation's files under their old names and would apply after one has
// moved is not applied (a rename is a quota transition); the recount that
// follows counts them where they are, and deleting the artifact releases
// them.
func TestRecountOverlappingARelocationIsNotApplied(t *testing.T) {
	oneFriendlySlug(t)
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.SetQuota(1 << 20); err != nil {
		t.Fatal(err)
	}
	walked, resume := make(chan struct{}), make(chan struct{})
	var firstWalk atomic.Bool
	afterUsageWalk = func() {
		if firstWalk.CompareAndSwap(false, true) {
			close(walked)
			<-resume
		}
	}
	t.Cleanup(func() { afterUsageWalk = nil })
	recounted := make(chan error, 1)
	beforeGenRename = func(file int) {
		switch file {
		case 0: // before the blob moves: a recount walks the old names
			go func() { recounted <- s.RecountUsage() }()
			<-walked
		case 1: // the blob has moved: let that recount try to apply
			close(resume)
			if err := <-recounted; err != nil {
				t.Error(err)
			}
		}
	}
	t.Cleanup(func() { beforeGenRename = nil })
	moved, _ := relocateGenerated(t, s, nil)
	assertAccounted(t, s)
	if err := s.Delete(moved.Slug); err != nil {
		t.Fatal(err)
	}
	assertAccounted(t, s)
}

func pendingWrites(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pending_writes`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !cond(); {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached in 10s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
