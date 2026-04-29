# outrelay-relay

Stateless QUIC relay that splices Agent ↔ Agent streams in user space. Part of the [OutRelay](https://github.com/boanlab/OutRelay) platform-agnostic outbound-only relay architecture.

This repo is the relay data plane. The control plane and the shared library live in [`OutRelay`](https://github.com/boanlab/OutRelay) and the per-workload sidecar in [`outrelay-agent`](https://github.com/boanlab/outrelay-agent); see those repos for system-level concerns (PKI, controller, agent identities). The relay-internal walk-throughs live under [`architecture/`](architecture/) and [`getting-started/`](getting-started/).

## Documentation

- [`getting-started/`](getting-started/README.md) — prerequisites, build, local run, Kubernetes, and observability.
- [`architecture/`](architecture/README.md) — package map and per-flow walkthroughs (stream open, resume, P2P promotion, inter-relay forwarding, policy enforcement).
- [`contribution/`](contribution/README.md) — local checks, code conventions, and PR flow.

## Layout

```
cmd/outrelay-relay/   # relay binary
pkg/
  edge/               # QUIC accept loop, mTLS, HELLO, control + data stream dispatch.
                      # Hosts the stream-resume matcher (used on agent reconnect),
                      # the P2P-promotion candidate forwarder (OBSERVED_ADDR_QUERY,
                      # CANDIDATE_OFFER/ANSWER, MIGRATE_TO_P2P/RELAY), REQUIRED-
                      # mode promotion enforcement, and the per-stream mode
                      # signalling (StreamReady for splice, AllocGranted for
                      # relay_mode=forward).
  forward/            # Mini-TURN UDP forwarding plane (relay_mode=forward). A
                      # separate UDP listener that strips the 4-byte peer
                      # allocation prefix and forwards opaque packets between
                      # registered agents. Skips QUIC encrypt/decrypt on the
                      # data plane.
  registry/           # facade over the controller's gRPC Registry plus the local
                      # URI → AgentConn map; surfaces ErrProviderRemote when the
                      # provider lives on a peer relay.
  splice/             # bidirectional io.CopyBuffer with a pooled 64 KiB buffer
                      # and HalfCloser awareness for clean half-close propagation.
  intra/              # inter-relay QUIC connection pool + ForwardStream.
  policy/             # LRU+TTL decision cache, live snapshot watcher, P2PMode
                      # flow-through (allowed / forbidden / required), and
                      # RelayMode (splice / forward).
  audit/              # best-effort audit emitter to the controller.
deployments/          # Kubernetes Service + Deployment manifests (namespace: outrelay).
```

## Build

```bash
make build           # bin/outrelay-relay (runs gofmt + golangci-lint + gosec first)
make test            # go test -race -count=1 ./...
make golangci-lint   # lint only
```

`go.mod` pins `github.com/boanlab/OutRelay` to a published version on GitHub, so the controller library is fetched via the Go module proxy with no sibling checkout required. Build the container image with `make build-image` (or `docker build -f Dockerfile -t outrelay-relay:dev .`).

## Run

```
outrelay-relay \
  --listen 0.0.0.0:7443 \
  --controller controller.local:7444 \
  --cert /etc/outrelay/tls.crt --key /etc/outrelay/tls.key \
  --ca /etc/outrelay/ca.crt \
  --relay-id relay-a --advertise outrelay-relay.outrelay.svc:7443 \
  --tenant acme \
  --metrics-dump /var/log/outrelay/metrics.jsonl
```

- `--tenant` enables policy enforcement (closed-world without a snapshot).
- `--listen-tcp host:port` adds a TCP+TLS+yamux listener for the UDP-blocked fallback path (agents enable it with `--relay-tcp`).
- `--listen-forward host:port` enables the mini-TURN UDP forwarding plane consumed when policy resolves a stream to `relay_mode=forward`.
- `--metrics-dump` writes one JSONL line per `--metrics-interval` (default 10 s).
- `/debug/metrics` and `/debug/pprof` are served on `--debug-listen` (default `127.0.0.1:9100`; pass `""` to disable).

## License

Apache 2.0 — see [`LICENSE`](LICENSE).
