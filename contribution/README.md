# Contributing

Thanks for your interest in `outrelay-relay`. This guide covers the
mechanics of getting a change merged: layout, local checks, commit and
PR conventions, and what reviewers look for.

The full system design (controller + relay + agent) lives in the
[OutRelay](https://github.com/boanlab/OutRelay) repo. Issues that are
not relay-specific are usually best filed there.

## Repository layout

```
cmd/outrelay-relay/   relay binary entry point
pkg/edge/             QUIC accept loop and ORP frame dispatch
pkg/registry/         controller-backed service registry + local agent map
pkg/intra/            inter-relay QUIC pool and FORWARD_STREAM
pkg/splice/           bidirectional byte-pump for paired streams
pkg/policy/           decision engine, decision cache, policy watcher
pkg/audit/            best-effort audit emitter to the controller
deployments/          Kubernetes Service + Deployment manifests
.github/workflows/    CI (fmt / lint / gosec / test / image build)
```

A change that touches more than one of these almost always belongs in
a single PR — the packages are layered, not independent.

## Local checks

The Makefile is the source of truth for what CI runs. Before pushing:

```bash
make gofmt          # gofmt drift -> non-zero exit
make golangci-lint  # standard linters + project tweaks (.golangci.yml)
make gosec          # security scan
make test           # go test -race -count=1 ./...
make build          # bundles the three gates above + compiles the binary
```

`make build` is the one-shot pre-push command. If any gate fails, fix
the cause rather than skipping the gate (no `--no-verify`, no
`//nolint` without a reason).

`go.mod` pins `github.com/boanlab/OutRelay` to a published tag, so you
do not need a sibling checkout. Dependency bumps go through `go get`
followed by `go mod tidy`; commit both `go.mod` and `go.sum`.

## Code conventions

- **Formatting**: `gofmt`. CI rejects drift.
- **Linting**: golangci-lint v2 with the standard preset plus the
  errcheck exclusions in `.golangci.yml`. Tests get a relaxed errcheck
  (test cleanup is best-effort by convention).
- **Security**: `gosec` runs in CI. If a finding is a false positive,
  annotate the line with a `// #nosec G<rule> -- <reason>` comment that
  explains why; do not blanket-disable rules.
- **Errors**: wrap with `fmt.Errorf("%w", err)` when crossing a package
  boundary so callers can `errors.Is`/`errors.As`. Log-and-drop is
  fine on the data-plane hot path (audit, candidate forward,
  checkpoint forward) — those paths are best-effort by design.
- **Logging**: use the `*slog.Logger` already plumbed through the
  server. Prefer structured key/value pairs over string interpolation.

### Comments

The relay codebase keeps comments terse and load-bearing. The rule of
thumb is *write comments for someone reading the file cold*.

- Default to no comment. Names should carry the meaning.
- When a comment is justified, explain **why**, not what — a hidden
  invariant, a non-obvious ordering, the reason a path is best-effort.
- Do not reference internal design-doc section numbers, demo names, or
  past code states. Those rot. State the rule in plain language.
- Do not narrate change history (`Widened from 5 s to 30 s …`). Git
  log is authoritative.

Every new Go file starts with the SPDX header used elsewhere in the
tree:

```go
// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 BoanLab @ Dankook University
```

The same header (with `#` instead of `//`) goes on Make/YAML/Dockerfile
files. `make license` is not wired up; the `.licenserc.yaml` config and
`.github/workflows/license.yml` enforce headers in CI.

## Tests

- Production code lives next to its tests (`foo.go` + `foo_test.go`).
- End-to-end coverage (relay + in-process controller stub + agents)
  lives in `pkg/edge/*_e2e_test.go`. These wire real QUIC listeners on
  ephemeral ports and exercise full frame dispatch paths — they are
  the canonical reference when a flow is unclear.
- Use `t.Parallel()` unless the test mutates package-level state
  (`edge.RequiredPromotionTimeout`, for example).
- Do not rely on real time. If you need to shrink a timeout for tests,
  expose the knob as a `var` (see `RequiredPromotionTimeout`,
  `ResumeWindow`).

## Commits and pull requests

- One logical change per PR. A bug fix and an unrelated cleanup are
  two PRs.
- Commit subject in imperative mood, ≤ 72 chars (`fix migrate LRU
  race`, not `fixed it`). Body explains the *why*.
- Reference issues by `#NNN` in the body, not the subject.
- PR description should answer: what changed, why, how it was tested.
  If the change is observable on the wire (new ORP frame, behavior
  shift on an existing one), call that out so the agent and controller
  authors can plan a coordinated rollout.
- CI must pass before review. A draft PR with red CI is fine; a
  ready-for-review PR with red CI is not.

## Reporting issues

Bugs that are reproducible against `main` go in the issue tracker with:

- relay version (`outrelay-relay --version`),
- relevant flags / env,
- a minimal trace (debug HTTP `/debug/metrics`, JSONL dump, or the log
  excerpt around the failure),
- the controller version it was talking to.

Security-sensitive reports should not go in public issues. Email the
maintainers listed in the parent OutRelay repo.

## License

Contributions are accepted under the Apache 2.0 license — see
[`LICENSE`](../LICENSE). Submitting a PR means you assert you have the
right to release the code under that license.
