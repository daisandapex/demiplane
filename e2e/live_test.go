// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package e2e

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestLiveReloadSSE_REST connects to GET /_events/{slug}, republishes the
// slug, and asserts the `event: reload` frame actually arrives on the wire —
// proving the store's Notify -> hub -> SSE fan-out path end to end against a
// real HTTP connection, not a mocked notifier.
func TestLiveReloadSSE_REST(t *testing.T) {
	srv := startServer(t, serverOpts{Token: "live-token"})

	pub := srv.do(t, http.MethodPost, srv.ControlURL+"/publish?slug=live-doc",
		strings.NewReader("v1"), srv.authHeader())
	if pub.Status != http.StatusCreated {
		t.Fatalf("publish: status=%d body=%s", pub.Status, pub.Body)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.ContentURL+"/_events/live-doc", nil)
	if err != nil {
		t.Fatalf("new SSE request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("connect SSE: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SSE connect: status=%d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("SSE content-type = %q", ct)
	}

	reader := bufio.NewReader(resp.Body)

	// Drain the initial ": connected" comment frame before republishing, so the
	// republish-triggered event is unambiguous in the stream that follows.
	if err := readUntilBlankLine(reader); err != nil {
		t.Fatalf("read initial SSE frame: %v", err)
	}

	// Republish the same slug — this is the store.Put -> Notify(slug) trigger
	// the whole SSE feature exists to observe.
	repub := srv.do(t, http.MethodPost, srv.ControlURL+"/publish?slug=live-doc",
		strings.NewReader("v2"), srv.authHeader())
	if repub.Status != http.StatusCreated {
		t.Fatalf("republish: status=%d body=%s", repub.Status, repub.Body)
	}

	// Read frames until we see the reload event or the context deadline fires.
	events := make(chan string, 8)
	errs := make(chan error, 1)
	go func() {
		for {
			frame, err := readEventFrame(reader)
			if err != nil {
				errs <- err
				return
			}
			events <- frame
		}
	}()

	deadline := time.After(8 * time.Second)
	for {
		select {
		case frame := <-events:
			if strings.Contains(frame, "event: reload") {
				return // success
			}
			// heartbeat or the connect comment — keep reading
		case err := <-errs:
			t.Fatalf("reading SSE stream: %v", err)
		case <-deadline:
			t.Fatalf("timed out waiting for `event: reload` after republish")
		}
	}
}

// readUntilBlankLine consumes lines up to and including the first blank line
// (the frame terminator per the SSE spec), i.e. exactly one frame.
func readUntilBlankLine(r *bufio.Reader) error {
	_, err := readEventFrame(r)
	return err
}

// readEventFrame reads lines until a blank line (frame terminator) and
// returns the accumulated frame text.
func readEventFrame(r *bufio.Reader) (string, error) {
	var b strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return b.String(), err
		}
		b.WriteString(line)
		if strings.TrimRight(line, "\r\n") == "" {
			return b.String(), nil
		}
	}
}
