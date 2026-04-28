// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 BoanLab @ Dankook University

package policy_test

import (
	"testing"

	"github.com/boanlab/outrelay-relay/pkg/policy"
)

func TestCachePutGet(t *testing.T) {
	t.Parallel()
	c := policy.NewCache()
	r := policy.Result{Decision: policy.DecisionAllow, Reason: "rule-1"}
	c.Put("c", "t", "m", r)
	got, ok := c.Get("c", "t", "m")
	if !ok {
		t.Fatal("Get miss after Put")
	}
	if got != r {
		t.Fatalf("got %+v want %+v", got, r)
	}
}

func TestCacheFlush(t *testing.T) {
	t.Parallel()
	c := policy.NewCache()
	c.Put("c", "t", "m", policy.Result{Decision: policy.DecisionAllow})
	c.Flush()
	if _, ok := c.Get("c", "t", "m"); ok {
		t.Fatal("Get hit after Flush")
	}
}
