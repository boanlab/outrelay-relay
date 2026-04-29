// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 BoanLab @ Dankook University

package forward_test

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/boanlab/outrelay-relay/pkg/forward"
)

// TestPlaneForwarding boots a plane, simulates two agents A and B
// that register their alloc ids, and verifies a packet from A
// addressed to B's alloc shows up at B's UDP socket.
func TestPlaneForwarding(t *testing.T) {
	t.Parallel()

	plane, err := forward.NewPlane("127.0.0.1:0", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = plane.Run(ctx) }()

	relayAddr := plane.Endpoint()

	// Two pre-allocated ids (the relay would normally hand these
	// out via AllocGranted to the consumer / provider agents).
	allocA := plane.Allocate()
	allocB := plane.Allocate()
	if allocA == 0 || allocB == 0 || allocA == allocB {
		t.Fatalf("bad alloc ids: A=%d B=%d", allocA, allocB)
	}

	// Each agent opens its own UDP socket.
	connA, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer connA.Close()
	connB, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer connB.Close()

	// Register both with the plane: prefix=0 (registration) +
	// payload=[my_alloc].
	register := func(c *net.UDPConn, alloc uint32) {
		t.Helper()
		buf := make([]byte, 8)
		binary.BigEndian.PutUint32(buf[0:4], 0)
		binary.BigEndian.PutUint32(buf[4:8], alloc)
		if _, err := c.WriteTo(buf, net.UDPAddrFromAddrPort(relayAddr)); err != nil {
			t.Fatal(err)
		}
	}
	register(connA, allocA)
	register(connB, allocB)

	// Wait for the plane's registration goroutine to record both.
	// It's an async UDP read so we need to give it a beat.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, okA := plane.Lookup(allocA)
		_, okB := plane.Lookup(allocB)
		if okA && okB {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := plane.Lookup(allocA); !ok {
		t.Fatalf("plane never registered alloc A")
	}
	if _, ok := plane.Lookup(allocB); !ok {
		t.Fatalf("plane never registered alloc B")
	}

	// A sends a data packet addressed to B (prefix = allocB).
	payload := []byte("hello from A")
	buf := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(buf[0:4], allocB)
	copy(buf[4:], payload)
	if _, err := connA.WriteTo(buf, net.UDPAddrFromAddrPort(relayAddr)); err != nil {
		t.Fatal(err)
	}

	// B reads. The plane strips the 4-byte prefix; what arrives
	// at B is the raw payload.
	_ = connB.SetReadDeadline(time.Now().Add(2 * time.Second))
	recv := make([]byte, 1500)
	n, _, err := connB.ReadFromUDPAddrPort(recv)
	if err != nil {
		t.Fatalf("B read: %v", err)
	}
	if got := string(recv[:n]); got != string(payload) {
		t.Fatalf("B got %q, want %q", got, payload)
	}
}

// TestPlaneDropsUnregistered makes sure a data packet for an
// unknown peer alloc is silently dropped (no echo, no panic).
func TestPlaneDropsUnregistered(t *testing.T) {
	t.Parallel()

	plane, err := forward.NewPlane("127.0.0.1:0", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = plane.Run(ctx) }()

	connA, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer connA.Close()

	buf := make([]byte, 4+5)
	binary.BigEndian.PutUint32(buf[0:4], 0xDEADBEEF) // never allocated
	copy(buf[4:], "lost")
	if _, err := connA.WriteTo(buf, net.UDPAddrFromAddrPort(plane.Endpoint())); err != nil {
		t.Fatal(err)
	}
	// Read with a short deadline — we expect timeout.
	_ = connA.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	recv := make([]byte, 64)
	if _, _, err := connA.ReadFromUDPAddrPort(recv); !errIsTimeout(err) {
		t.Fatalf("expected timeout, got %v", err)
	}
}

func errIsTimeout(err error) bool {
	if err == nil {
		return false
	}
	if err == io.EOF {
		return false
	}
	type timeouter interface{ Timeout() bool }
	if t, ok := err.(timeouter); ok {
		return t.Timeout()
	}
	return false
}
