// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 BoanLab @ Dankook University

package splice_test

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"

	"github.com/boanlab/outrelay-relay/pkg/splice"
)

// TestBidirectional uses two net.Pipe pairs to verify bytes flow in
// both directions through splice.Bidirectional.
func TestBidirectional(t *testing.T) {
	t.Parallel()
	// Client A <-> server A (via net.Pipe), Client B <-> server B.
	// splice connects server A and server B.
	clientA, serverA := net.Pipe()
	clientB, serverB := net.Pipe()

	go func() {
		if err := splice.Bidirectional(serverA, serverB); err != nil {
			t.Errorf("splice: %v", err)
		}
	}()

	// A -> B
	wantAB := []byte("ping")
	if _, err := clientA.Write(wantAB); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(wantAB))
	if _, err := io.ReadFull(clientB, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, wantAB) {
		t.Fatalf("A->B: got %q want %q", got, wantAB)
	}

	// B -> A
	wantBA := []byte("pong")
	if _, err := clientB.Write(wantBA); err != nil {
		t.Fatal(err)
	}
	got2 := make([]byte, len(wantBA))
	if _, err := io.ReadFull(clientA, got2); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got2, wantBA) {
		t.Fatalf("B->A: got %q want %q", got2, wantBA)
	}

	// Closing one side propagates EOF through splice; tear down both.
	_ = clientA.Close()
	_ = clientB.Close()

	// Allow goroutines to drain.
	time.Sleep(20 * time.Millisecond)
}
