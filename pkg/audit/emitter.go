// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 BoanLab @ Dankook University

// Package audit emits stream-open decisions to the controller. The
// relay calls Emit once per OPEN_STREAM after policy evaluation; the
// emitter buffers and forwards to controller.Audit.Record. Failures
// are best-effort: the data plane never blocks on audit, and a full
// queue drops events rather than stalling stream open.
package audit

import (
	"context"
	"log/slog"

	pb "github.com/boanlab/OutRelay/lib/control/v1"
)

// QueueDepth bounds the in-flight buffer between the data plane and
// the gRPC sender goroutine. Hitting this drops events silently
// rather than stalling stream open.
const QueueDepth = 1024

// Emitter wraps a controller Audit client.
type Emitter struct {
	client pb.AuditClient
	logger *slog.Logger
	queue  chan *pb.AuditEvent
}

func NewEmitter(client pb.AuditClient, logger *slog.Logger) *Emitter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Emitter{
		client: client,
		logger: logger,
		queue:  make(chan *pb.AuditEvent, QueueDepth),
	}
}

// Run drains the queue and ships events to the controller until ctx
// cancels. Send failures are logged at warn level and dropped.
func (e *Emitter) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev := <-e.queue:
			_, err := e.client.Record(ctx, &pb.RecordAuditRequest{Event: ev})
			if err != nil {
				e.logger.Warn("audit emit", "err", err)
			}
		}
	}
}

// Emit enqueues an event. Non-blocking: if the buffer is full the
// event is dropped (logged once at warn level on first overflow).
func (e *Emitter) Emit(ev *pb.AuditEvent) {
	select {
	case e.queue <- ev:
	default:
		e.logger.Warn("audit queue full; event dropped",
			"caller", ev.Caller, "target", ev.Target)
	}
}
