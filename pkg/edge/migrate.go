// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 BoanLab @ Dankook University

package edge

import (
	"sync"
	"time"

	"github.com/boanlab/OutRelay/lib/resume"
)

// MigrationTTL bounds how long the relay holds stream metadata after
// MIGRATE_TO_P2P. Within this window a fast demotion via
// MIGRATE_TO_RELAY can pick up where the splice left off without the
// agents redoing service resolution / policy evaluation.
const MigrationTTL = 60 * time.Second

// migratedStream is one LRU entry tracking a stream the relay has
// just released to direct P2P. The migratedAt timestamp drives expiry.
type migratedStream struct {
	id          resume.StreamID
	consumerURI string
	providerURI string
	migratedAt  time.Time
}

// migratedLRU is a small TTL-only cache (no size cap; entries expire
// after MigrationTTL). The relay's promotion / demotion paths poke
// this; correctness does not depend on entries being present —
// MIGRATE_TO_RELAY uses the existing resumeMatcher to pair halves —
// but the cache supports auditing.
type migratedLRU struct {
	mu sync.Mutex
	m  map[resume.StreamID]*migratedStream
}

func newMigratedLRU() *migratedLRU {
	return &migratedLRU{m: map[resume.StreamID]*migratedStream{}}
}

// Note records that stream_id has migrated to P2P. Idempotent on
// repeated calls (the latest timestamp wins).
func (l *migratedLRU) Note(s *migratedStream) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s.migratedAt = time.Now()
	l.m[s.id] = s
	l.gcLocked(time.Now())
}

// Lookup returns the stored entry if present and not expired. The
// returned pointer aliases the map entry; callers treat it as
// read-only.
func (l *migratedLRU) Lookup(id resume.StreamID) *migratedStream {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.m[id]
	if !ok {
		return nil
	}
	if time.Since(e.migratedAt) >= MigrationTTL {
		delete(l.m, id)
		return nil
	}
	return e
}

// Discard removes an entry; called after a successful demotion match
// so the same stream id can be re-promoted later without colliding
// against the stale LRU row.
func (l *migratedLRU) Discard(id resume.StreamID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.m, id)
}

// Len returns the live entry count for tests / metrics.
func (l *migratedLRU) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gcLocked(time.Now())
	return len(l.m)
}

func (l *migratedLRU) gcLocked(now time.Time) {
	for k, v := range l.m {
		if now.Sub(v.migratedAt) >= MigrationTTL {
			delete(l.m, k)
		}
	}
}
