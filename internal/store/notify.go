// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

// Notifier receives a notification whenever an artifact is (re)published to a
// slug via Put. It is the store's tiny, dependency-free pub/sub seam: the SSE
// live-reload feature (internal/server/live.go, T2.1) implements it to fan a
// "reload" event out to the subscribers watching that slug.
//
// Keep implementations non-blocking: Notify runs on the publish goroutine, so an
// implementation must not block on it (buffer or drop, never wait on a consumer).
type Notifier interface {
	// Notify reports that slug's bytes just changed (a successful Put).
	Notify(slug string)
}

// SetNotifier registers n as the store's publish notifier; nil disables
// notifications (the default). Intended to be called ONCE at startup, before the
// server begins handling requests — it is not synchronized against concurrent
// Put calls.
func (s *Store) SetNotifier(n Notifier) { s.notifier = n }

// notify fires the registered notifier, if any. A guarded no-op when unset, so
// core carries no live-reload cost unless the feature is wired in.
func (s *Store) notify(slug string) {
	if s.notifier != nil {
		s.notifier.Notify(slug)
	}
}
