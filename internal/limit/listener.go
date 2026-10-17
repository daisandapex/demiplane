// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package limit

import (
	"net"
	"sync"
)

// Listener bounds the connections a net.Listener hands out: at most max open
// at once (Accept waits for one to close) and at most perClient from one
// client key (AddrKey of the remote address; extra connections from that
// client are closed as soon as they are accepted). Zero or negative disables
// either limit.
func Listener(l net.Listener, max, perClient int) net.Listener {
	if max <= 0 && perClient <= 0 {
		return l
	}
	return &limitListener{Listener: l, slots: NewGate(max), perClient: perClient,
		open: map[string]int{}, closed: make(chan struct{})}
}

type limitListener struct {
	net.Listener
	slots     Gate
	perClient int

	mu        sync.Mutex
	open      map[string]int
	closed    chan struct{}
	closeOnce sync.Once
}

// Close closes the listener and wakes an Accept waiting for a slot.
func (l *limitListener) Close() error {
	l.closeOnce.Do(func() { close(l.closed) })
	return l.Listener.Close()
}

func (l *limitListener) Accept() (net.Conn, error) {
	for {
		if l.slots != nil {
			select {
			case l.slots <- struct{}{}:
			case <-l.closed:
				return nil, net.ErrClosed
			}
		}
		c, err := l.Listener.Accept()
		if err != nil {
			l.slots.Release()
			return nil, err
		}
		key := AddrKey(c.RemoteAddr().String())
		if !l.admit(key) {
			c.Close()
			l.slots.Release()
			continue
		}
		return &limitConn{Conn: c, l: l, key: key}, nil
	}
}

func (l *limitListener) admit(key string) bool {
	if l.perClient <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.open[key] >= l.perClient {
		return false
	}
	l.open[key]++
	return true
}

func (l *limitListener) done(key string) {
	if l.perClient > 0 {
		l.mu.Lock()
		if l.open[key]--; l.open[key] <= 0 {
			delete(l.open, key)
		}
		l.mu.Unlock()
	}
	l.slots.Release()
}

type limitConn struct {
	net.Conn
	l    *limitListener
	key  string
	once sync.Once
}

func (c *limitConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.l.done(c.key) })
	return err
}
