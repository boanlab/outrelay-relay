// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 BoanLab @ Dankook University

// Package splice carries bytes bidirectionally between two streams
// after the relay has paired them. The relay never inspects the
// payload, just forwards bytes.
//
// The implementation uses io.CopyBuffer with a sync.Pool of 64 KiB
// buffers, one buffer per direction.
package splice

import (
	"errors"
	"io"
	"sync"
)

// BufferSize is the per-direction copy buffer.
const BufferSize = 64 * 1024

var bufPool = sync.Pool{
	New: func() any {
		b := make([]byte, BufferSize)
		return &b
	},
}

// HalfCloser is a stream that supports independent shutdown of the
// write half. quic.Stream and many net.Conn implementations satisfy
// this. We use it (when available) to propagate an EOF on one direction
// without tearing down the other.
type HalfCloser interface {
	CloseWrite() error
}

// Bidirectional pipes a <-> b until either side EOFs or errors. It
// returns the first non-EOF error observed, or nil if both directions
// closed cleanly.
//
// Both streams are left to the caller to close fully — Bidirectional
// only signals half-close (CloseWrite) on the destination once the
// source EOFs, so the peer learns that no more data is coming.
func Bidirectional(a, b io.ReadWriter) error {
	errCh := make(chan error, 2)
	go func() { errCh <- copyOne(b, a) }() // a -> b
	go func() { errCh <- copyOne(a, b) }() // b -> a

	var firstErr error
	for range 2 {
		if err := <-errCh; err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// copyOne copies src -> dst using a pooled buffer, then half-closes
// dst's write side if supported. EOF is normal completion.
func copyOne(dst io.Writer, src io.Reader) error {
	bufp := bufPool.Get().(*[]byte)
	defer bufPool.Put(bufp)

	_, err := io.CopyBuffer(dst, src, *bufp)
	if hc, ok := dst.(HalfCloser); ok {
		_ = hc.CloseWrite()
	}
	if err == nil || errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
