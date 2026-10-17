// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package reply

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daisandapex/demiplane/internal/limit"
	"github.com/daisandapex/demiplane/internal/module"
	"github.com/daisandapex/demiplane/internal/store"
)

// admissionSetup mounts both planes' routes on one mux over a store holding:
// "on" (replies enabled), "off" (replies not enabled), "locked" (replies
// enabled, password "pw") and "gone" (replies enabled, private).
func admissionSetup(t *testing.T, l Limits) (*httptest.Server, *Module) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, p := range []store.PutOptions{
		{Slug: "on", Replies: true},
		{Slug: "off"},
		{Slug: "locked", Replies: true, Password: "pw"},
		{Replies: true, Private: true},
	} {
		if _, err := st.Put(p, strings.NewReader("<html>x</html>")); err != nil {
			t.Fatal(err)
		}
	}
	h := &fakeHost{st: st, dataDir: t.TempDir()}
	m := &Module{}
	m.setLimits(l)
	mux := http.NewServeMux()
	m.Routes(mux, h)
	m.ContentRoutes(mux, h)
	t.Cleanup(func() { m.rs.close() })
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, m
}

// submit posts one answer (content plane) or one reply (control plane).
func submit(t *testing.T, ts *httptest.Server, plane, slug, pass string) (int, http.Header) {
	t.Helper()
	var req *http.Request
	if plane == "content" {
		req, _ = http.NewRequest(http.MethodPost, ts.URL+"/answer/"+slug, strings.NewReader(url.Values{"body": {"an answer"}}.Encode()))
		req.Header.Set("Content-Type", formCT)
	} else {
		req, _ = http.NewRequest(http.MethodPost, ts.URL+"/reply/"+slug, strings.NewReader(`{"kind":"comment","body":"a reply"}`))
		req.Header.Set("Content-Type", "application/json")
	}
	if pass != "" {
		req.SetBasicAuth("u", pass)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode, resp.Header
}

func storedCount(t *testing.T, m *Module, slug string) int {
	t.Helper()
	n, err := m.rs.count(slug)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRepliesRequireOptInAndPassword(t *testing.T) {
	ts, m := admissionSetup(t, openLimits())
	for _, plane := range []string{"content", "control"} {
		t.Run(plane, func(t *testing.T) {
			if code, _ := submit(t, ts, plane, "off", ""); code != http.StatusNotFound {
				t.Errorf("reply to an artifact without replies enabled = %d, want 404", code)
			}
			if code, _ := submit(t, ts, plane, "nope", ""); code != http.StatusNotFound {
				t.Errorf("reply to a missing artifact = %d, want 404", code)
			}
			code, hdr := submit(t, ts, plane, "locked", "")
			// The artifact's own realm, the one GET /{slug} challenges with, so
			// a browser answers it with the password the viewer already gave.
			if want := module.PasswordChallenge("locked"); code != http.StatusUnauthorized || hdr.Get("WWW-Authenticate") != want {
				t.Errorf("reply to a password page without it = %d (%q), want 401 with %q", code, hdr.Get("WWW-Authenticate"), want)
			}
			if code, _ := submit(t, ts, plane, "locked", "wrong"); code != http.StatusUnauthorized {
				t.Errorf("reply with a wrong password = %d, want 401", code)
			}
			if code, _ := submit(t, ts, plane, "locked", "pw"); code != http.StatusCreated {
				t.Errorf("reply with the password = %d, want 201", code)
			}
			if code, _ := submit(t, ts, plane, "on", ""); code != http.StatusCreated {
				t.Errorf("reply to an enabled public artifact = %d, want 201", code)
			}
		})
	}
	if n := storedCount(t, m, "off"); n != 0 {
		t.Errorf("%d replies stored for a disabled artifact", n)
	}
	if n := storedCount(t, m, "locked"); n != 2 {
		t.Errorf("%d replies stored for the password page, want 2 (one per plane, with the password)", n)
	}
	// The reply form itself is gated the same way.
	for slug, want := range map[string]int{"off": 404, "locked": 401, "on": 200} {
		resp, err := http.Get(ts.URL + "/reply/" + slug)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("GET /reply/%s = %d, want %d", slug, resp.StatusCode, want)
		}
	}
}

func TestReplyRateLimitsPerClientAndTarget(t *testing.T) {
	l := openLimits()
	l.RateClient = limit.Rate{Every: time.Hour, Burst: 3}
	ts, m := admissionSetup(t, l)
	var created, limited int
	for i := 0; i < 8; i++ {
		code, hdr := submit(t, ts, "content", "on", "")
		switch code {
		case http.StatusCreated:
			created++
		case http.StatusTooManyRequests:
			limited++
			if hdr.Get("Retry-After") == "" {
				t.Fatal("429 without Retry-After")
			}
		default:
			t.Fatalf("status %d", code)
		}
	}
	if created != 3 || limited != 5 {
		t.Fatalf("created %d, limited %d; want 3 and 5", created, limited)
	}
	if n := storedCount(t, m, "on"); n != 3 {
		t.Fatalf("stored %d, want 3: a rate-limited submission must not be recorded", n)
	}

	l = openLimits()
	l.RateTarget = limit.Rate{Every: time.Hour, Burst: 2}
	ts, _ = admissionSetup(t, l)
	for i, want := range []int{201, 201, 429} {
		if code, _ := submit(t, ts, "control", "on", ""); code != want {
			t.Fatalf("target submission %d = %d, want %d", i, code, want)
		}
	}
	// Another target is unaffected.
	if code, _ := submit(t, ts, "control", "locked", "pw"); code != http.StatusCreated {
		t.Fatalf("other target = %d, want 201", code)
	}
}

// TestReplyCapIsAtomic starts one short of the per-artifact cap and submits
// concurrently: exactly one submission may be admitted.
func TestReplyCapIsAtomic(t *testing.T) {
	l := openLimits()
	l.PerArtifact = 1000
	ts, m := admissionSetup(t, l)
	for i := 0; i < 999; i++ {
		if _, err := m.rs.add("on", "comment", "seed", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	const n = 24
	codes := make([]int, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			plane := "content"
			if i%2 == 1 {
				plane = "control"
			}
			codes[i], _ = submit(t, ts, plane, "on", "")
		}(i)
	}
	close(start)
	wg.Wait()
	var created int
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusTooManyRequests:
		default:
			t.Fatalf("status %d", c)
		}
	}
	if created != 1 {
		t.Fatalf("%d submissions admitted at 999 of 1000, want exactly 1", created)
	}
	if got := storedCount(t, m, "on"); got != 1000 {
		t.Fatalf("stored %d replies, want 1000", got)
	}
}

func TestReplyAggregateLimits(t *testing.T) {
	l := openLimits()
	l.Total = 3
	ts, m := admissionSetup(t, l)
	for i, want := range []int{201, 201, 201, 507} {
		if code, _ := submit(t, ts, "content", "on", ""); code != want {
			t.Fatalf("submission %d = %d, want %d", i, code, want)
		}
	}
	if code, _ := submit(t, ts, "control", "locked", "pw"); code != http.StatusInsufficientStorage {
		t.Fatalf("other artifact past the total = %d, want 507", code)
	}

	l = openLimits()
	l.TotalBytes = maxReplyBody + 20 // room for two "an answer" (9 bytes) replies
	ts, m = admissionSetup(t, l)
	big := strings.Repeat("x", maxReplyBody)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/answer/on", strings.NewReader(url.Values{"body": {big}}.Encode()))
	req.Header.Set("Content-Type", formCT)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first large answer = %d, want 201", resp.StatusCode)
	}
	for i, want := range []int{201, 201, 507} {
		if code, _ := submit(t, ts, "content", "on", ""); code != want {
			t.Fatalf("byte-limited submission %d = %d, want %d", i, code, want)
		}
	}
	if got := storedCount(t, m, "on"); got != 3 {
		t.Fatalf("stored %d, want 3", got)
	}
}

func TestReplyRetentionFreesRoom(t *testing.T) {
	l := openLimits()
	l.Total = 2
	l.Retention = 24 * time.Hour
	ts, m := admissionSetup(t, l)
	old := time.Now().Add(-48 * time.Hour)
	for i := 0; i < 2; i++ {
		if _, err := m.rs.add("on", "comment", "old", old); err != nil {
			t.Fatal(err)
		}
	}
	m.rs.mu.Lock()
	m.rs.lastSweep = time.Time{} // due for a retention pass
	m.rs.mu.Unlock()
	if code, _ := submit(t, ts, "content", "on", ""); code != http.StatusCreated {
		t.Fatalf("submission after expired replies = %d, want 201 (retention should free room)", code)
	}
	reps, err := m.rs.list("on", "all")
	if err != nil {
		t.Fatal(err)
	}
	if len(reps) != 1 || reps[0].Body != "an answer" {
		t.Fatalf("after retention: %+v, want only the new reply", reps)
	}
}

// TestRefusedSubmissionCreatesNoHookJob: a submission refused by any
// admission check queues no hook event.
func TestRefusedSubmissionCreatesNoHookJob(t *testing.T) {
	l := openLimits()
	l.RateClient = limit.Rate{Every: time.Hour, Burst: 1}
	ts, m := admissionSetup(t, l)
	m.hook = hookConfig{execCmd: "true"}
	fired := make(chan Reply, 16)
	m.hookDone = func(rep Reply) { fired <- rep }
	submit(t, ts, "content", "off", "")    // disabled
	submit(t, ts, "content", "locked", "") // no password
	if code, _ := submit(t, ts, "content", "on", ""); code != http.StatusCreated {
		t.Fatalf("admitted submission = %d", code)
	}
	submit(t, ts, "content", "on", "") // rate limited
	waitHook(t, fired)
	select {
	case rep := <-fired:
		t.Fatalf("a refused submission fired a hook: %+v", rep)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestReplyTotalsHoldAcrossStores opens the reply database twice, as two
// processes would, and fills it through both: the aggregate limit holds
// across them.
func TestReplyTotalsHoldAcrossStores(t *testing.T) {
	dir := t.TempDir()
	a, err := openReplyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	b, err := openReplyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	l := openLimits()
	l.Total = 10
	var wg sync.WaitGroup
	var mu sync.Mutex
	stored := 0
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rs := a
			if i%2 == 1 {
				rs = b
			}
			if _, err := rs.addLimited("s"+strconv.Itoa(i), "comment", "x", time.Now(), l); err == nil {
				mu.Lock()
				stored++
				mu.Unlock()
			} else if !errors.Is(err, errReplyStoreFull) {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	var n int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM replies`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if stored != 10 || n != 10 {
		t.Fatalf("stored %d (table has %d) through two stores with a total of 10", stored, n)
	}
	var sn, sb int
	if err := a.db.QueryRow(`SELECT n, bytes FROM reply_stats`).Scan(&sn, &sb); err != nil {
		t.Fatal(err)
	}
	if sn != 10 || sb != 10 {
		t.Fatalf("totals row says %d replies, %d bytes; want 10 and 10", sn, sb)
	}
}

// TestReplyTotalsSeededFromExistingRows: a database from before the totals
// table starts with totals that match its rows.
func TestReplyTotalsSeededFromExistingRows(t *testing.T) {
	dir := t.TempDir()
	rs, err := openReplyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := rs.add("s", "comment", "hello", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{`DROP TRIGGER replies_stats_insert`, `DROP TRIGGER replies_stats_delete`,
		`DROP TRIGGER replies_stats_update`, `DROP TABLE reply_stats`} {
		if _, err := rs.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	rs.close()
	rs, err = openReplyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.close()
	var n, b int
	if err := rs.db.QueryRow(`SELECT n, bytes FROM reply_stats`).Scan(&n, &b); err != nil {
		t.Fatal(err)
	}
	if n != 3 || b != 15 {
		t.Fatalf("seeded totals %d replies, %d bytes; want 3 and 15", n, b)
	}
}
