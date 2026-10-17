// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package limit

import (
	"net"
	"testing"
	"time"
)

func dialN(t *testing.T, addr string, n int) []net.Conn {
	t.Helper()
	var cs []net.Conn
	for i := 0; i < n; i++ {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		cs = append(cs, c)
	}
	t.Cleanup(func() {
		for _, c := range cs {
			c.Close()
		}
	})
	return cs
}

// acceptAll accepts in the background and reports each accepted conn.
func acceptAll(l net.Listener) chan net.Conn {
	ch := make(chan net.Conn, 64)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				close(ch)
				return
			}
			ch <- c
		}
	}()
	return ch
}

func TestListenerPerClientLimit(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := Listener(raw, 0, 2)
	defer l.Close()
	accepted := acceptAll(l)
	cs := dialN(t, raw.Addr().String(), 4)
	var got []net.Conn
	timeout := time.After(2 * time.Second)
	for len(got) < 2 {
		select {
		case c := <-accepted:
			got = append(got, c)
		case <-timeout:
			t.Fatalf("accepted %d, want 2", len(got))
		}
	}
	select {
	case c := <-accepted:
		t.Fatalf("a third connection from one client was accepted: %v", c.RemoteAddr())
	case <-time.After(200 * time.Millisecond):
	}
	// The refused connections were closed by the server.
	buf := make([]byte, 1)
	closed := 0
	for _, c := range cs {
		c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		if _, err := c.Read(buf); err != nil {
			if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
				closed++
			}
		}
	}
	if closed != 2 {
		t.Fatalf("%d connections closed by the server, want 2", closed)
	}
	// Closing an admitted connection frees its place.
	got[0].Close()
	dialN(t, raw.Addr().String(), 1)
	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("no connection accepted after one closed")
	}
}

func TestListenerTotalLimitWaits(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := Listener(raw, 2, 0)
	defer l.Close()
	accepted := acceptAll(l)
	dialN(t, raw.Addr().String(), 3)
	first := []net.Conn{<-accepted, <-accepted}
	select {
	case <-accepted:
		t.Fatal("third connection accepted past the total limit")
	case <-time.After(200 * time.Millisecond):
	}
	first[0].Close()
	first[0].Close() // a second Close must not free a second place
	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("waiting connection not accepted after one closed")
	}
	if n := len(l.(*limitListener).slots); n != 2 {
		t.Fatalf("%d slots held, want 2", n)
	}
}

func TestListenerCloseWakesWaitingAccept(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := Listener(raw, 1, 0)
	accepted := acceptAll(l)
	dialN(t, raw.Addr().String(), 1)
	<-accepted // holds the only slot; Accept now waits
	time.Sleep(50 * time.Millisecond)
	l.Close()
	select {
	case _, ok := <-accepted:
		if ok {
			t.Fatal("accepted a connection after Close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Accept still waiting after Close")
	}
}
