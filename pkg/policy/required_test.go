// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 BoanLab @ Dankook University

package policy_test

import (
	"testing"

	pb "github.com/boanlab/OutRelay/lib/control/v1"

	"github.com/boanlab/outrelay-relay/pkg/policy"
)

// TestP2PModeFlowsThroughDecide verifies the P2PMode field on a
// PolicyRule survives FromPB and surfaces in the Decide result —
// so edge.handleConsumerStream can act on REQUIRED / FORBIDDEN.
func TestP2PModeFlowsThroughDecide(t *testing.T) {
	t.Parallel()
	cases := []struct {
		pb   pb.P2PMode
		want policy.P2PMode
	}{
		{pb.P2PMode_P2P_ALLOWED, policy.P2PModeAllowed},
		{pb.P2PMode_P2P_FORBIDDEN, policy.P2PModeForbidden},
		{pb.P2PMode_P2P_REQUIRED, policy.P2PModeRequired},
	}
	for _, tc := range cases {
		t.Run(tc.pb.String(), func(t *testing.T) {
			t.Parallel()
			rule := policy.FromPB(&pb.PolicyRule{
				Id:            "r1",
				Tenant:        "acme",
				CallerPattern: "*",
				TargetPattern: "svc-x",
				Decision:      pb.Decision_DECISION_ALLOW,
				P2PMode:       tc.pb,
			})
			if rule.P2PMode != tc.want {
				t.Fatalf("rule.P2PMode=%v want %v", rule.P2PMode, tc.want)
			}
			eng := policy.NewEngine()
			eng.Add(rule)
			res := eng.Decide("any", "svc-x", "")
			if res.P2PMode != tc.want {
				t.Fatalf("result.P2PMode=%v want %v", res.P2PMode, tc.want)
			}
		})
	}
}

// TestCombineP2PModes verifies the caller-side / callee-side merge:
// FORBIDDEN > REQUIRED > ALLOWED, symmetric in arguments.
func TestCombineP2PModes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		a, b policy.P2PMode
		want policy.P2PMode
	}{
		{policy.P2PModeAllowed, policy.P2PModeAllowed, policy.P2PModeAllowed},
		{policy.P2PModeAllowed, policy.P2PModeRequired, policy.P2PModeRequired},
		{policy.P2PModeRequired, policy.P2PModeAllowed, policy.P2PModeRequired},
		{policy.P2PModeRequired, policy.P2PModeRequired, policy.P2PModeRequired},
		{policy.P2PModeAllowed, policy.P2PModeForbidden, policy.P2PModeForbidden},
		{policy.P2PModeForbidden, policy.P2PModeAllowed, policy.P2PModeForbidden},
		{policy.P2PModeRequired, policy.P2PModeForbidden, policy.P2PModeForbidden},
		{policy.P2PModeForbidden, policy.P2PModeRequired, policy.P2PModeForbidden},
		{policy.P2PModeForbidden, policy.P2PModeForbidden, policy.P2PModeForbidden},
	}
	for _, tc := range cases {
		if got := policy.CombineP2PModes(tc.a, tc.b); got != tc.want {
			t.Errorf("Combine(%v,%v)=%v want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestP2PModeStringer(t *testing.T) {
	t.Parallel()
	cases := map[policy.P2PMode]string{
		policy.P2PModeAllowed:   "allowed",
		policy.P2PModeForbidden: "forbidden",
		policy.P2PModeRequired:  "required",
	}
	for m, want := range cases {
		if got := m.String(); got != want {
			t.Errorf("%v: got %q want %q", m, got, want)
		}
	}
}
