// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 BoanLab @ Dankook University

package edge

// Internal collision-detection unit test. recordPair is unexported,
// so this lives next to it in package edge.

import (
	"testing"
)

// TestRecordPairRejectsDifferentPair: a second recordPair for the
// same stream_id with a different (consumer, provider) tuple must
// fail rather than silently take over the active splice.
func TestRecordPairRejectsDifferentPair(t *testing.T) {
	t.Parallel()
	s := &Server{pairs: map[uint64]streamPair{}}

	original := streamPair{consumerURI: "outrelay://acme/agent/c1", providerURI: "outrelay://acme/agent/p1"}
	other := streamPair{consumerURI: "outrelay://acme/agent/c2", providerURI: "outrelay://acme/agent/p2"}

	if !s.recordPair(0xfeed, original) {
		t.Fatal("first recordPair should succeed")
	}
	if !s.recordPair(0xfeed, original) {
		t.Fatal("idempotent re-record of same pair should succeed")
	}
	if s.recordPair(0xfeed, other) {
		t.Fatal("collision (different pair under same id) should fail")
	}
	// Original entry must still be in place after the rejection.
	if got := s.pairs[0xfeed]; got != original {
		t.Fatalf("original pair clobbered: %+v", got)
	}
	s.forgetPair(0xfeed)
	if !s.recordPair(0xfeed, other) {
		t.Fatal("after forgetPair, the slot is reusable")
	}
}

// TestRecordPairZeroStreamIDIsNoop covers the safety guard — stream
// id 0 is treated as "no resume state" and recordPair short-circuits.
func TestRecordPairZeroStreamIDIsNoop(t *testing.T) {
	t.Parallel()
	s := &Server{pairs: map[uint64]streamPair{}}
	if !s.recordPair(0, streamPair{consumerURI: "x"}) {
		t.Fatal("recordPair(0) should always succeed (noop)")
	}
	if len(s.pairs) != 0 {
		t.Fatalf("zero id should not populate pairs map: %+v", s.pairs)
	}
}
