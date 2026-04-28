// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 BoanLab @ Dankook University

// Package policy is the relay-side policy engine. The relay receives
// the full policy snapshot from the controller (Policy.Watch) and
// re-evaluates on every OPEN_STREAM. The engine is intentionally
// minimal — caller / target / method glob match plus a small set of
// flags — so policy decisions stay on the data-plane hot path
// without pulling in an external policy engine.
package policy

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/boanlab/OutRelay/lib/control/v1"
)

// Decision is the policy outcome.
type Decision int

const (
	DecisionDeny  Decision = 0
	DecisionAllow Decision = 1
)

func (d Decision) String() string {
	if d == DecisionAllow {
		return "allow"
	}
	return "deny"
}

// Result is what Decide returns: the decision plus a short human
// reason (matched policy id, "no rule", "expired", ...). The reason
// shows up in audit logs and STREAM_REJECT replies.
type Result struct {
	Decision Decision
	Reason   string
	// P2PMode mirrors the matched rule's p2p_mode. The relay uses it
	// post-decision: REQUIRED arms a promotion timer, the other
	// modes leave the splice as-is.
	P2PMode P2PMode
}

// P2PMode mirrors orpv1 / control.PolicyRule p2p_mode.
type P2PMode int

const (
	P2PModeAllowed   P2PMode = 0 // default — opportunistic
	P2PModeForbidden P2PMode = 1
	P2PModeRequired  P2PMode = 2
)

func (m P2PMode) String() string {
	switch m {
	case P2PModeForbidden:
		return "forbidden"
	case P2PModeRequired:
		return "required"
	default:
		return "allowed"
	}
}

// CombineP2PModes returns the effective mode when the caller-side
// and callee-side rules disagree. Precedence: FORBIDDEN > REQUIRED >
// ALLOWED. If either side forbids promotion, the stream stays on the
// relay; if either side requires promotion, the relay arms its
// enforcement timer.
func CombineP2PModes(a, b P2PMode) P2PMode {
	if a == P2PModeForbidden || b == P2PModeForbidden {
		return P2PModeForbidden
	}
	if a == P2PModeRequired || b == P2PModeRequired {
		return P2PModeRequired
	}
	return P2PModeAllowed
}

// Rule is one compiled policy rule. Patterns are compared with simple
// "*" globs; '*' matches any run of non-empty characters within a
// segment (we don't try to mimic shell globs because URI segments and
// HTTP methods don't contain '/' boundaries we'd want to honor).
type Rule struct {
	ID            string
	CallerPattern string
	TargetPattern string
	MethodPattern string // "" matches anything
	Decision      Decision
	ExpiresUnixMs int64 // 0 = never
	P2PMode       P2PMode
}

// Snapshot is an immutable policy index. It is built by the watch
// loop and atomically swapped — readers never lock.
type Snapshot struct {
	byTarget map[string][]*Rule // exact-target rules
	wildcard []*Rule            // rules whose TargetPattern contains '*'
}

// Decide returns the first matching rule's decision. If no rule
// matches, the default is deny (closed-world).
func (s *Snapshot) Decide(caller, target, method string, now time.Time) Result {
	if s == nil {
		return Result{Decision: DecisionDeny, Reason: "no policy snapshot"}
	}
	nowMs := now.UnixMilli()
	if rs, ok := s.byTarget[target]; ok {
		if r := matchFirst(rs, caller, target, method, nowMs); r != nil {
			return Result{Decision: r.Decision, Reason: r.ID, P2PMode: r.P2PMode}
		}
	}
	if r := matchFirst(s.wildcard, caller, target, method, nowMs); r != nil {
		return Result{Decision: r.Decision, Reason: r.ID, P2PMode: r.P2PMode}
	}
	return Result{Decision: DecisionDeny, Reason: "no matching rule"}
}

func matchFirst(rs []*Rule, caller, target, method string, nowMs int64) *Rule {
	for _, r := range rs {
		if r.ExpiresUnixMs > 0 && nowMs >= r.ExpiresUnixMs {
			continue
		}
		if !matchGlob(r.CallerPattern, caller) {
			continue
		}
		if !matchGlob(r.TargetPattern, target) {
			continue
		}
		if r.MethodPattern != "" && !matchGlob(r.MethodPattern, method) {
			continue
		}
		return r
	}
	return nil
}

// BuildSnapshot indexes rules by target. Rules whose TargetPattern is
// an exact string land in byTarget; rules with a '*' in the target
// fall back to a linear list scanned after the exact bucket misses.
func BuildSnapshot(rules []*Rule) *Snapshot {
	s := &Snapshot{byTarget: map[string][]*Rule{}}
	for _, r := range rules {
		if strings.ContainsRune(r.TargetPattern, '*') {
			s.wildcard = append(s.wildcard, r)
			continue
		}
		s.byTarget[r.TargetPattern] = append(s.byTarget[r.TargetPattern], r)
	}
	return s
}

// FromPB converts a wire policy rule to the engine's compact form.
func FromPB(r *pb.PolicyRule) *Rule {
	return &Rule{
		ID:            r.Id,
		CallerPattern: r.CallerPattern,
		TargetPattern: r.TargetPattern,
		MethodPattern: r.MethodPattern,
		Decision:      decisionFromPB(r.Decision),
		ExpiresUnixMs: r.ExpiresUnixMs,
		P2PMode:       p2pModeFromPB(r.P2PMode),
	}
}

func p2pModeFromPB(m pb.P2PMode) P2PMode {
	switch m {
	case pb.P2PMode_P2P_FORBIDDEN:
		return P2PModeForbidden
	case pb.P2PMode_P2P_REQUIRED:
		return P2PModeRequired
	default:
		return P2PModeAllowed
	}
}

func decisionFromPB(d pb.Decision) Decision {
	if d == pb.Decision_DECISION_DENY {
		return DecisionDeny
	}
	return DecisionAllow
}

// Engine is a hot-swappable policy snapshot. Decide goes lock-free
// via atomic.Pointer.
type Engine struct {
	cur atomic.Pointer[Snapshot]

	mu    sync.Mutex
	rules map[string]*Rule // id -> rule (working set; rebuild Snapshot from it)
}

func NewEngine() *Engine {
	e := &Engine{rules: map[string]*Rule{}}
	e.cur.Store(BuildSnapshot(nil))
	return e
}

// Decide is the hot path; lock-free.
func (e *Engine) Decide(caller, target, method string) Result {
	return e.cur.Load().Decide(caller, target, method, time.Now())
}

// Apply mutates the working set and atomically swaps in a new
// snapshot. evMutator runs under the engine's mutex; the caller
// supplies it so adds and removes can be batched without redundant
// rebuilds.
func (e *Engine) Apply(mutate func(rules map[string]*Rule)) {
	e.mu.Lock()
	mutate(e.rules)
	rules := make([]*Rule, 0, len(e.rules))
	for _, r := range e.rules {
		rules = append(rules, r)
	}
	snap := BuildSnapshot(rules)
	e.mu.Unlock()
	e.cur.Store(snap)
}

// Set replaces the working set wholesale (used to absorb the initial
// snapshot from Policy.Watch).
func (e *Engine) Set(rules []*Rule) {
	e.Apply(func(m map[string]*Rule) {
		for k := range m {
			delete(m, k)
		}
		for _, r := range rules {
			m[r.ID] = r
		}
	})
}

// Add inserts or replaces a single rule.
func (e *Engine) Add(r *Rule) {
	e.Apply(func(m map[string]*Rule) { m[r.ID] = r })
}

// Remove deletes a rule by id (no-op if absent).
func (e *Engine) Remove(id string) {
	e.Apply(func(m map[string]*Rule) { delete(m, id) })
}

// matchGlob returns true if s matches pattern, where '*' is the only
// wildcard and matches any (possibly empty) run of characters. An
// empty pattern matches only an empty s.
func matchGlob(pattern, s string) bool {
	if pattern == "" {
		return s == ""
	}
	if !strings.Contains(pattern, "*") {
		return pattern == s
	}
	if pattern == "*" {
		return true
	}
	parts := strings.Split(pattern, "*")
	// Must start with the first piece.
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	// Each middle piece must be findable in order.
	for _, p := range parts[1 : len(parts)-1] {
		idx := strings.Index(s, p)
		if idx < 0 {
			return false
		}
		s = s[idx+len(p):]
	}
	// Must end with the last piece.
	last := parts[len(parts)-1]
	return strings.HasSuffix(s, last)
}
