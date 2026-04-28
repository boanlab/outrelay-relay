// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 BoanLab @ Dankook University

package policy_test

import (
	"strconv"
	"testing"
	"time"

	"github.com/boanlab/outrelay-relay/pkg/policy"
)

func TestMatchGlobBasic(t *testing.T) {
	t.Parallel()
	cases := []struct {
		pattern string
		s       string
		want    bool
	}{
		{"*", "anything", true},
		{"", "", true},
		{"", "x", false},
		{"svc-*", "svc-payments", true},
		{"svc-*", "other", false},
		{"outrelay://acme/svc-*", "outrelay://acme/svc-billing", true},
		{"outrelay://acme/svc-*", "outrelay://other/svc-billing", false},
		{"POST /charge", "POST /charge", true},
		{"POST /charge", "GET /charge", false},
		{"POST *", "POST /anything", true},
		{"a*b*c", "axxbyyc", true},
		{"a*b*c", "axxc", false},
	}
	for _, tc := range cases {
		t.Run(tc.pattern+"|"+tc.s, func(t *testing.T) {
			rules := []*policy.Rule{{
				ID: "r", CallerPattern: "*", TargetPattern: tc.pattern, Decision: policy.DecisionAllow,
			}}
			snap := policy.BuildSnapshot(rules)
			got := snap.Decide("any", tc.s, "", time.Now()).Decision == policy.DecisionAllow
			if got != tc.want {
				t.Fatalf("got %v want %v for pattern=%q s=%q", got, tc.want, tc.pattern, tc.s)
			}
		})
	}
}

func TestMatcherDefaultDeny(t *testing.T) {
	t.Parallel()
	snap := policy.BuildSnapshot(nil)
	r := snap.Decide("c", "t", "m", time.Now())
	if r.Decision != policy.DecisionDeny {
		t.Fatalf("empty rules must deny, got %v", r)
	}
}

func TestMatcherFirstMatchWins(t *testing.T) {
	t.Parallel()
	snap := policy.BuildSnapshot([]*policy.Rule{
		{ID: "specific-deny", CallerPattern: "outrelay://acme/svc-evil", TargetPattern: "svc-payments", Decision: policy.DecisionDeny},
		{ID: "broad-allow", CallerPattern: "*", TargetPattern: "svc-payments", Decision: policy.DecisionAllow},
	})
	got := snap.Decide("outrelay://acme/svc-evil", "svc-payments", "", time.Now())
	if got.Decision != policy.DecisionDeny || got.Reason != "specific-deny" {
		t.Fatalf("got %+v", got)
	}
	got2 := snap.Decide("outrelay://acme/svc-good", "svc-payments", "", time.Now())
	if got2.Decision != policy.DecisionAllow || got2.Reason != "broad-allow" {
		t.Fatalf("got %+v", got2)
	}
}

func TestMatcherExpiredSkipped(t *testing.T) {
	t.Parallel()
	now := time.Now()
	rules := []*policy.Rule{
		{ID: "expired", CallerPattern: "*", TargetPattern: "svc-x", Decision: policy.DecisionAllow,
			ExpiresUnixMs: now.Add(-time.Hour).UnixMilli()},
	}
	snap := policy.BuildSnapshot(rules)
	r := snap.Decide("c", "svc-x", "", now)
	if r.Decision != policy.DecisionDeny {
		t.Fatalf("expired rule should be skipped, got %+v", r)
	}
}

func TestMatcherWildcardTarget(t *testing.T) {
	t.Parallel()
	snap := policy.BuildSnapshot([]*policy.Rule{
		{ID: "wild", CallerPattern: "*", TargetPattern: "svc-*", Decision: policy.DecisionAllow},
	})
	r := snap.Decide("anyone", "svc-payments", "", time.Now())
	if r.Decision != policy.DecisionAllow {
		t.Fatalf("wildcard target should match, got %+v", r)
	}
	r2 := snap.Decide("anyone", "other", "", time.Now())
	if r2.Decision != policy.DecisionDeny {
		t.Fatalf("non-matching target should deny, got %+v", r2)
	}
}

func TestEngineLiveSwap(t *testing.T) {
	t.Parallel()
	e := policy.NewEngine()
	if e.Decide("c", "t", "m").Decision != policy.DecisionDeny {
		t.Fatal("empty engine should default-deny")
	}
	e.Add(&policy.Rule{ID: "ok", CallerPattern: "*", TargetPattern: "t", Decision: policy.DecisionAllow})
	if e.Decide("c", "t", "m").Decision != policy.DecisionAllow {
		t.Fatal("after Add should allow")
	}
	e.Remove("ok")
	if e.Decide("c", "t", "m").Decision != policy.DecisionDeny {
		t.Fatal("after Remove should deny")
	}
}

// TestEnginePerformance asserts the hot-path budget: with 1000 rules
// loaded, p99 of Decide stays under 1 ms. Skipped under -short.
func TestEnginePerformance(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("perf test")
	}
	rules := make([]*policy.Rule, 0, 1000)
	for i := range 1000 {
		rules = append(rules, &policy.Rule{
			ID:            "rule-" + strconv.Itoa(i),
			CallerPattern: "outrelay://acme/svc-caller-" + strconv.Itoa(i),
			TargetPattern: "svc-target-" + strconv.Itoa(i),
			Decision:      policy.DecisionAllow,
		})
	}
	e := policy.NewEngine()
	e.Set(rules)

	const iters = 5000
	durations := make([]time.Duration, iters)
	for i := range iters {
		j := i % 1000
		caller := "outrelay://acme/svc-caller-" + strconv.Itoa(j)
		target := "svc-target-" + strconv.Itoa(j)
		start := time.Now()
		_ = e.Decide(caller, target, "")
		durations[i] = time.Since(start)
	}

	// Compute p99.
	for i := range len(durations) {
		for j := i + 1; j < len(durations); j++ {
			if durations[j] < durations[i] {
				durations[i], durations[j] = durations[j], durations[i]
			}
		}
	}
	p99 := durations[(iters*99)/100]
	t.Logf("p99 over %d iters with %d policies: %v", iters, 1000, p99)
	if p99 > time.Millisecond {
		t.Fatalf("p99 %v exceeds 1ms target", p99)
	}
}
