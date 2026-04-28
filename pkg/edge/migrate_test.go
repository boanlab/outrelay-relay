// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 BoanLab @ Dankook University

package edge

// Internal test: pokes at unexported migratedLRU semantics.

import (
	"testing"
	"time"

	"github.com/boanlab/OutRelay/lib/resume"
)

func TestMigratedLRUNoteAndLookup(t *testing.T) {
	t.Parallel()
	l := newMigratedLRU()
	id := resume.StreamID(0xabc)
	l.Note(&migratedStream{
		id:          id,
		consumerURI: "outrelay://acme/agent/cccc",
		providerURI: "outrelay://acme/agent/pppp",
	})
	if l.Len() != 1 {
		t.Fatalf("len=%d", l.Len())
	}
	got := l.Lookup(id)
	if got == nil || got.consumerURI != "outrelay://acme/agent/cccc" {
		t.Fatalf("lookup mismatch: %+v", got)
	}
	l.Discard(id)
	if l.Len() != 0 {
		t.Fatalf("after discard, len=%d", l.Len())
	}
}

func TestMigratedLRUExpiresAfterTTL(t *testing.T) {
	t.Parallel()
	l := newMigratedLRU()
	id := resume.StreamID(0xfeed)
	// Inject an entry whose migratedAt is older than MigrationTTL.
	l.m[id] = &migratedStream{
		id:          id,
		consumerURI: "c",
		providerURI: "p",
		migratedAt:  time.Now().Add(-2 * MigrationTTL),
	}
	if got := l.Lookup(id); got != nil {
		t.Fatalf("expected expired entry, got %+v", got)
	}
}
