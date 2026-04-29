# Architecture

This document explains how `outrelay-relay` actually works: what
components live in the binary, how a stream travels through it, and
which pieces handle the edge cases (reconnect, P2P promotion, peer
relays, policy enforcement). It is meant for a reader who has cloned
the repo for the first time and wants to navigate the code.

For the surrounding system (controller, agent, wire protocol) see the
parent [OutRelay](https://github.com/boanlab/OutRelay) repo.

## Mental model

There are three parties:

- **Agents** sit inside private networks and only ever make outbound
  connections. They are both consumers and providers of services.
- **Relays** (this binary) sit at network edges that both sides can
  reach outbound. They splice consumer ↔ provider streams.
- **Controller** is the single source of truth for the (tenant, name)
  → (agent_uri, relay_id) map and for policy rules. The relay
  *queries* it on every resolve and *subscribes* to it for policy.

The relay itself is stateless on the data plane: every state-bearing
fact (which agent provides which service, what policy applies) is
fetched from the controller. The only locally held state is the
URI → live-QUIC-connection map, which exists because the relay has to
know which physical connection to write `INCOMING_STREAM` onto.

```
   ┌────────┐  outbound QUIC+mTLS  ┌─────────┐  outbound QUIC+mTLS  ┌────────┐
   │ Agent  ├─────────────────────▶│  Relay  │◀─────────────────────┤ Agent  │
   │  (C)   │  OPEN_STREAM         │         │  INCOMING_STREAM     │  (P)   │
   └────────┘                      └────┬────┘                      └────────┘
                                        │ gRPC
                                        ▼
                                  ┌───────────┐
                                  │Controller │  registry, policy, audit
                                  └───────────┘
```

## Packages

Each `pkg/` directory has a single, well-defined job. Reading them in
this order matches the request flow.

### `pkg/edge`

The QUIC accept loop and the ORP frame dispatcher. This is where the
relay's behavior is. `edge.Server` holds:

- the agent registry (delegated to `pkg/registry`),
- the policy engine and decision cache (delegated to `pkg/policy`),
- the inter-relay pool (`pkg/intra`),
- the optional **forwarding plane** (`pkg/forward`), used when
  policy resolves a stream to `relay_mode=forward`,
- the **resume matcher** — pairs the two halves of a `STREAM_RESUME`
  so a stream can survive an agent reconnect,
- the **migrated LRU** — remembers streams that were just promoted to
  P2P, so a fast `MIGRATE_TO_RELAY` demotion can pick up where the
  splice left off,
- a `pairs` map: `stream_id` → `(consumer URI, provider URI)`, the
  routing table for control-plane frames that need to bounce between
  the two ends of a splice (candidate exchange, checkpoints).

After OPEN_STREAM and STREAM_ACCEPT, `edge.Server` sends each agent
exactly one stream-mode signal on stream-0 ctrl: `STREAM_READY` if
the policy decision is `relay_mode=splice` (default), or
`ALLOC_GRANTED` if it is `relay_mode=forward`. The agents branch on
that signal to either run the splice path (relay-mediated bytes) or
the forwarding-plane path (relay sees only ciphertext).

Every accepted QUIC connection is dispatched by the URI SAN's role:
`agent` runs the splice path; `relay` runs the inter-relay forwarding
path.

### `pkg/registry`

A thin facade. `Resolve(callerURI, serviceName)` calls the
controller's gRPC `Resolve` and returns one of three things:

1. a local `Provider` (the connection lives on this relay),
2. `ErrServiceNotFound`,
3. `ErrProviderRemote` plus a `*Remote{RelayID, Endpoint, AgentURI}`
   so the caller can hand off to `pkg/intra`.

`RegisterAgent` / `LookupAgent` is a local-only `URI → Provider` map.
The provider abstraction is what lets the relay open
`INCOMING_STREAM` toward the right physical QUIC connection without
the rest of the code knowing about transport details.

### `pkg/intra`

The inter-relay forwarding plane. `Pool` is a lazy cache of QUIC
connections keyed by relay id; on first `Get` it dials and on
subsequent calls reuses. `ForwardStream` opens a fresh stream on a
peer connection and writes a `FORWARD_STREAM` frame containing the
target service / method / source-agent URI; the peer relay's reply
(`STREAM_ACCEPT` / `STREAM_REJECT`) is parsed inline. A failed dial is
not cached — every `Get` retries.

### `pkg/forward`

The mini-TURN UDP forwarding plane consumed when policy resolves a
stream to `relay_mode=forward`. `Plane` binds a separate UDP socket
(opened with `--listen-forward`), assigns monotonic allocation ids,
and forwards opaque packets between two registered allocations:

- Wire: `[peer_alloc: u32 BE][payload: N]`. `peer_alloc=0` is a
  registration packet whose payload is `[my_alloc: u32 BE]`.
- After registration, every datagram with prefix `peer_alloc != 0`
  is forwarded — payload only — to the registered endpoint.
- Allocations are released on `Forget`, which `edge.go` calls when
  the relay-side data streams torn down.

The plane sees only ciphertext; the agents establish their own
end-to-end QUIC over the forwarded path, so the relay skips QUIC
encrypt/decrypt on the data plane and is mostly limited by UDP
copy throughput.

### `pkg/splice`

Bidirectional `io.CopyBuffer` with a 64 KiB pooled buffer per
direction and `HalfCloser` awareness. When one direction EOFs, the
other side's write half is closed so the peer learns there is no more
data — but the read direction stays open until the peer closes. The
relay never inspects payload bytes; this package is the data path.

### `pkg/policy`

Two pieces:

- `Engine` is a hot-swappable snapshot. `Decide(caller, target,
  method)` reads an `atomic.Pointer[Snapshot]` — no lock on the hot
  path. `Snapshot` indexes rules by exact target and falls back to a
  wildcard list. The matcher uses simple `*` globs.
- `Watcher` consumes the controller's `Policy.Watch` stream and
  rebuilds the snapshot. The initial snapshot is buffered and
  applied wholesale on `SNAPSHOT_END`; later add/remove events are
  applied incrementally and flush the decision cache.

`Cache` is a per-(caller, target, method) decision cache with a
5-second TTL and a 64 K soft cap. Every snapshot rebuild flushes it.

`P2PMode` (`allowed` / `forbidden` / `required`) is carried alongside
the allow/deny verdict. The relay reads it after the decision —
`required` arms a promotion timer, `forbidden` keeps the splice on
the relay, `allowed` is the default opportunistic case.
`CombineP2PModes` merges the caller-side and callee-side rules with
precedence FORBIDDEN > REQUIRED > ALLOWED.

`RelayMode` (`splice` / `forward`) controls the data path: the
default is `splice` (the relay terminates QUIC and copies bytes);
`forward` switches the stream to the mini-TURN forwarding plane
(`pkg/forward`), where the relay handles only opaque UDP and the
agents run their own end-to-end QUIC.

### `pkg/audit`

A non-blocking `Emit(*AuditEvent)` queue (1024 deep) drained by a
goroutine that ships events to the controller's `Audit.Record`. A
full queue drops events with a warn log; a failed RPC is logged and
moved on. The data plane never blocks on audit.

## The lifecycle of one stream

The default path: an agent dials the relay, registers a service it
provides, and another agent (the consumer) opens a stream toward
that service.

```
Agent C                    Relay                       Agent P
   │                         │                           │
   │  QUIC+mTLS connect       │                          │
   │ ──────────────────────▶  │                          │
   │  (ctrl stream 0 opens)   │                          │
   │  HELLO                   │                          │
   │ ──────────────────────▶  │                          │
   │  HELLO_ACK               │                          │
   │ ◀──────────────────────  │                          │
   │                         │  RegisterAgent(uri, conn) │
   │                         │                           │
   │  open data stream + OPEN_STREAM(target, method, sid)│
   │ ──────────────────────▶  │                          │
   │                         │  Resolve(target) ──▶ ctrl │
   │                         │  ◀── (provider URI, relay)│
   │                         │  policy.Decide(C, target) │
   │                         │  policy.Decide(P, C)      │
   │                         │  open new stream + INCOMING_STREAM(C,
   │                         │                  target, method, sid) ─▶
   │                         │  ◀────────────────────────  STREAM_ACCEPT
   │                         │  splice.Bidirectional(consumer, provider)
   │ ◀══════════════════════ application bytes ══════════════════════▶ │
```

Notable details:

- The **consumer's** stream id (`sid`) is what stitches the
  control-plane frames that come later (candidates, checkpoints,
  migrations). `recordPair(sid, {C, P})` is the routing table.
- Policy is checked **twice** — once from the caller's perspective
  ("can C reach `target`?") and once from the callee's ("can P accept
  a call from C?"). Either side can deny. P2P modes combine.
- If `Resolve` returns `ErrProviderRemote`, the splice path forks
  into inter-relay forwarding (next section). The consumer never
  sees the difference; it gets the same `STREAM_ACCEPT` either way.
- A `stream_id` collision (a fresh `OPEN_STREAM` reusing an id that
  is already mapped to a different `(C, P)`) is rejected with code
  `409`. The consumer is expected to regenerate.

## Inter-relay forwarding

The provider lives on a different relay. The receiving relay does
not know how to reach the provider directly; it dials the peer
relay's advertised endpoint (resolved through the controller) and
re-issues the request as a forwarded stream.

```
Agent C ──▶ Relay R1 ── FORWARD_STREAM ──▶ Relay R2 ── INCOMING_STREAM ──▶ Agent P
            (consumer-                    (provider-                       │
             side splice) ◀── STREAM_ACCEPT (echoed end-to-end) ◀──────────│
            splice(C, peer) ────────  splice(peer, P) ─────────────────────│
```

`pkg/intra` opens a single long-lived QUIC connection per peer relay.
On each forwarded request a fresh stream carries `FORWARD_STREAM` and
the peer's reply. Dial failures are surfaced as 502 to the consumer.
A stream-open failure on a cached peer connection invalidates the
cache entry (`Pool.Drop`), so the next attempt redials — this is the
only "circuit breaker" the relay has, and it is intentional: the peer
fan-out is small.

Both sides of the chain run the same `splice.Bidirectional`. Bytes
flow C ↔ R1 ↔ R2 ↔ P, never staged in disk or in a longer-than-buffer
queue.

## Stream resume (relay failure recovery)

When the active relay dies (or the connection drops) the agents
detect it independently — one side typically by an explicit
ApplicationError, the other by an idle timeout. They reconnect to
*any* relay in the pool and send `STREAM_RESUME` carrying the same
stream id and their byte position counters.

The receiving relay parks one half in the **resume matcher**:

```
Agent C ── STREAM_RESUME(sid, pos_c, peer_ack_c) ──▶ Relay
                                                       │ (parked)
Agent P ── STREAM_RESUME(sid, pos_p, peer_ack_p) ──▶ Relay
                                                       │ (matches)
                                Relay echoes peer's STREAM_RESUME
                                  back to each side, then splices.
```

Both halves must arrive within `ResumeWindow` (30 s). If only one
arrives, that half is closed when the timer fires and the agent
surfaces a connection error to its application. The relay does not
interpret the byte counters; the agents use the echoed `peer_ack`
position to drive retransmission from their local ring buffers.

The window is wide because reconnect detection is asymmetric — one
side may notice within 100 ms while the other waits out a multi-second
idle timeout.

## P2P promotion and demotion

When policy permits and the network allows, the agents try to
upgrade the relay-mediated splice to a direct QUIC connection
between themselves. The relay participates only as a STUN-equivalent
and as a candidate-exchange forwarder.

1. **Server-reflexive observation.** Each agent sends
   `OBSERVED_ADDR_QUERY` on its control stream; the relay replies
   with the agent's `RemoteAddr` (the NAT-mapped public endpoint).
2. **Candidate exchange.** Agents send `CANDIDATE_OFFER` and
   `CANDIDATE_ANSWER` carrying their candidate addresses. The relay
   looks up the splice peer in the `pairs` map and re-serializes the
   frame onto the peer's control stream — it does not interpret the
   payload.
3. **Migration.** Once the agents establish a direct path, each
   sends `MIGRATE_TO_P2P` on its control stream. The relay records
   the (consumer, provider) tuple in the **migrated LRU** with a
   60-second TTL.
4. **Demotion (optional).** If the direct path breaks, an agent can
   send `MIGRATE_TO_RELAY` on a fresh stream. The relay re-uses the
   resume matcher to re-pair the two halves. If the migrated LRU
   still has the entry, the demotion is logged with the prior
   metadata and the entry is discarded on success.

`p2p_mode=required` enforcement runs alongside (4): if
`MIGRATE_TO_P2P` does not arrive within `RequiredPromotionTimeout`
(10 s), the relay closes the consumer stream so the splice tears down
and the application surfaces the error. Keeping the splice would
silently violate the policy.

## Control-plane frame routing

Some frames need to bounce between the two ends of a splice without
ever being interpreted by the relay. They are routed via the `pairs`
map:

| Frame                | Sender → relay  | Relay action                                  |
|----------------------|-----------------|-----------------------------------------------|
| `STREAM_CHECKPOINT`  | either side     | re-emit verbatim onto peer's control stream   |
| `CANDIDATE_OFFER`    | either side     | re-emit verbatim onto peer's control stream   |
| `CANDIDATE_ANSWER`   | either side     | re-emit verbatim onto peer's control stream   |
| `OBSERVED_ADDR_QUERY`| sender          | reply with sender's QUIC RemoteAddr           |
| `PING`               | sender          | reply `PONG`                                  |
| `MIGRATE_TO_P2P`     | either side     | record in migrated LRU                        |
| `MIGRATE_TO_RELAY`   | either side     | submit half to resume matcher                 |
| `REGISTER`           | provider        | call controller.RegisterService, reply `REGISTER_ACK` |

Frames whose target peer is unknown (id never paired, splice already
torn down) are dropped silently. The control plane is best-effort by
design.

## Concurrency model

- One goroutine per accepted QUIC connection (`serveConn`), which
  spawns one more goroutine per accepted stream on that connection.
- The control loop on stream 0 runs on its own goroutine; control-plane
  writes onto an agent's stream 0 are serialized by `AgentConn.ctrlMu`,
  because the control loop is reading and other goroutines (candidate
  forward, checkpoint forward) may write.
- Splice itself is two goroutines per direction inside
  `splice.Bidirectional`.
- The policy engine's `Decide` is lock-free
  (`atomic.Pointer[Snapshot]`); only `Apply` (rebuild) takes the
  engine mutex.
- The resume matcher and migrated LRU each protect a single mutex.
- The intra pool's per-relay-id slot is dialed under-lock once; a
  losing-race dialer closes its extra connection.

The hot path (`OPEN_STREAM` → `Resolve` → splice) takes no
package-level locks beyond the `pairsMu` insert and the policy-cache
read/write.

## Failure modes

| Symptom                                  | What is happening                                                                 |
|------------------------------------------|-----------------------------------------------------------------------------------|
| `STREAM_REJECT 403`                      | policy denied (caller-side or callee-side)                                        |
| `STREAM_REJECT 404`                      | controller has no provider for `target`                                           |
| `STREAM_REJECT 409`                      | `stream_id` collision against an active splice                                    |
| `STREAM_REJECT 502 inter-relay …`        | peer relay unreachable or pool dial failed                                        |
| `STREAM_REJECT 503 provider rejected`    | provider sent `STREAM_REJECT` after `INCOMING_STREAM`                             |
| relay exits non-zero on startup          | mTLS material missing/invalid, or `UpsertRelay` to controller failed              |
| streams open but stall                   | provider not actually listening on its local addr; check controller registry     |
| audit queue full                         | controller `Audit.Record` is slow; events drop, data plane keeps moving           |

Every one of these is observable through the structured log lines
and the `/debug/metrics` counters listed in
[`getting-started/`](../getting-started/README.md).

## Where to read next

- `pkg/edge/edge.go` — the dispatch loop, top to bottom.
- `pkg/edge/*_e2e_test.go` — runnable references for each flow.
- The parent OutRelay repo for the wire-format definitions and the
  controller's gRPC services.
