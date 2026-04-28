# Getting started

This guide covers the relay binary itself: how to build it, what its
flags mean, and how to read its observability surface. System-level
setup (PKI, controller, end-to-end demo) lives in the parent
[OutRelay](https://github.com/boanlab/OutRelay) repo — start there if
you want a full cluster-of-three-components running. Come back here
once you need to dive into relay internals.

## What the relay does, in one paragraph

Agents make a single outbound QUIC+mTLS connection to a relay. The
relay is stateless on the data plane: it accepts connections, asks the
controller "who provides service X for tenant Y?", opens a stream
toward the resolved provider agent, and splices the consumer's bytes
to the provider's bytes. When network conditions allow, agents
promote the splice to a direct P2P session and the relay drops out;
when a relay dies mid-flight, agents reconnect and resume on a peer
replica without losing in-flight bytes.

## Prerequisites

| Tool   | Version    | Why                                    |
|--------|------------|----------------------------------------|
| Go     | 1.25+      | matches `go.mod`                       |
| Docker | any recent | only for `make build-image`            |

To actually exercise the relay you also need a controller and at
least two agents, plus matching mTLS material. All of that lives in
the parent OutRelay repo — see its README for the integrated
quickstart.

## Build

```bash
make build           # bin/outrelay-relay (gofmt + golangci-lint + gosec gates first)
make test            # go test -race -count=1 ./...
make golangci-lint   # lint only
make build-image     # docker.io/boanlab/outrelay-relay:$(TAG)
```

The version string is stamped at link time from the Makefile's `TAG`
variable; `outrelay-relay --version` reports it. Override with `make
TAG=v1.2.3 build` (or `build-image`).

`go.mod` pins `github.com/boanlab/OutRelay` to a published tag, so the
controller library is fetched via the Go module proxy with no sibling
checkout required.

## Flags

```
outrelay-relay \
  --listen 0.0.0.0:7443 \
  --controller controller.local:7444 \
  --cert /etc/outrelay/tls.crt \
  --key  /etc/outrelay/tls.key \
  --ca   /etc/outrelay/ca.crt \
  --relay-id relay-a \
  --advertise outrelay-relay.outrelay.svc:7443 \
  --tenant acme
```

| Flag                | Default              | Notes                                                     |
|---------------------|----------------------|-----------------------------------------------------------|
| `--listen`          | `127.0.0.1:7443`     | QUIC listen address (UDP).                                |
| `--controller`      | `127.0.0.1:7444`     | Controller gRPC address. Required.                        |
| `--cert / --key`    | (required)           | Server cert + key. URI SAN must encode a `relay/<id>` identity. |
| `--ca`              | (required)           | CA bundle used to verify peer (agent / peer-relay) certs. |
| `--relay-id`        | `--listen`           | Identifier sent on `UpsertRelay`. Make per-Pod in K8s.    |
| `--advertise`       | `--listen`           | Address peer relays use when forwarding to this replica.  |
| `--tenant`          | `""`                 | Subscribe to this tenant's policy. Empty = no enforcement. |
| `--debug-listen`    | `127.0.0.1:9100`     | `/debug/metrics` + `/debug/pprof`. Empty disables.        |
| `--metrics-dump`    | `""`                 | JSONL file path; one snapshot per `--metrics-interval`.   |
| `--metrics-interval`| `10s`                | Period for the JSONL dump.                                |
| `--log-format`      | `text`               | `text` or `json`.                                         |

What happens at startup:

1. mTLS material loaded; CA bundle parsed.
2. gRPC dial to `--controller`, then `UpsertRelay(id, region,
   endpoint)`. The relay exits non-zero if this RPC fails — the
   controller is required.
3. If `--tenant` is set, `Policy.Watch` subscription opens (engine is
   closed-world until the snapshot arrives) and the audit emitter
   starts shipping decisions to `Audit.Record`. Without `--tenant`,
   the relay runs with no policy enforcement.
4. QUIC listener comes up on `--listen` and the accept loop starts.

## Kubernetes manifest

`deployments/20-outrelay-relay.yaml` is the reference Service +
Deployment. It runs in the `outrelay` namespace, mounts a
`outrelay-relay-tls` Secret at `/etc/outrelay`, and dials
`outrelay-controller.outrelay.svc.cluster.local:7444`. Two replicas
by default — drop to one for a minimal setup, or kill one Pod to
exercise the resume path.

The Secret, the controller Service, and the namespace itself are part
of the OutRelay system bootstrap; see the parent repo for how to wire
those up.

## Observability

```bash
# live counters and histograms
curl -s 127.0.0.1:9100/debug/metrics

# pprof
go tool pprof http://127.0.0.1:9100/debug/pprof/profile?seconds=30

# tail the periodic JSONL dump (if --metrics-dump is set)
tail -F /tmp/relay-metrics.jsonl
```

| Metric                     | What it tells you                                       |
|----------------------------|---------------------------------------------------------|
| `stream_active`            | streams currently spliced through this relay            |
| `stream_open_duration`     | time from OPEN_STREAM to splice begin                   |
| `policy_eval_duration`     | end-to-end policy decision latency                      |
| `policy_cache_hits/misses` | cache effectiveness                                     |
| `bytes_forwarded`          | total spliced bytes (consumer + provider directions)    |
| `intra_relay_hops`         | streams that crossed to a peer relay via FORWARD_STREAM |
| `stream_rejects`           | streams the relay rejected (policy / collision / 502)   |

## Troubleshooting (relay-side only)

**`upsert relay` exits non-zero on startup.** The relay can't reach
the controller, or its leaf cert URI SAN doesn't parse as a
`relay/<id>` identity. Check `--controller` reachability first, then
the cert.

**Agents connect but every stream rejects with 403.** Either
`--tenant` is set and no matching policy snapshot has arrived yet
(closed-world default) or a rule is denying. Watch `policy_cache_misses`
and the controller's `Policy.Watch` log.

**Streams resolve but never splice.** The provider's `STREAM_ACCEPT`
isn't coming back. Verify the provider agent's local service is up;
the relay logs `provider stream open failed` when its
`OpenIncoming` errors out.

**Inter-relay forwarding fails.** The relay reuses its own server
cert as a client cert when dialing peer relays — both sides need to
trust the same CA. If `intra: dial peer …` floods the log, check that
the peer's `--advertise` is reachable from this Pod.

## Next steps

- [`architecture/`](../architecture/README.md) — package map and
  per-flow walkthroughs.
- [`contribution/`](../contribution/README.md) — local checks, code
  conventions, PR flow.
- [OutRelay](https://github.com/boanlab/OutRelay) — system-level
  setup: PKI bootstrap, controller, agents, end-to-end demo.
