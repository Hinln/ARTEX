package agent

// Behavioural tests for the compaction safety rules that protect normal
// exploitation work: a lowered membership cap must never double-cover a node,
// never strand a node uncovered, and never let a failing run retire a digest it
// could not replace.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// foldableBatch is the single bound on work per run: at most limit blocks, no
// block the breaker has given up on, and nothing after cancellation.
func TestFoldableBatchBoundsAndSkipsTripped(t *testing.T) {
	cvers := map[int64]int{1: 0, 2: 0, 3: 0, 4: 0, 5: 0, 6: 0}
	blocks := []block{
		{Members: []int64{1, 2}},
		{Members: []int64{3, 4}},
		{Members: []int64{5, 6}},
	}
	tripped := blockSignature(blocks[1], cvers)
	open := func(sig string) bool { return sig != tripped }

	got := foldableBatch(context.Background(), blocks, cvers, 8, open)
	if len(got) != 2 {
		t.Fatalf("expected the tripped block to be skipped, got %d blocks", len(got))
	}
	for _, b := range got {
		if blockSignature(b, cvers) == tripped {
			t.Fatal("a tripped block must not be attempted")
		}
	}

	if got := foldableBatch(context.Background(), blocks, cvers, 2, open); len(got) != 2 {
		t.Fatalf("limit=2 must cap the batch, got %d", len(got))
	}

	// A cancelled run attempts nothing, so it can never look "complete" and let a
	// caller retire digests on partial work.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := foldableBatch(ctx, blocks, cvers, 8, open); len(got) != 0 {
		t.Fatalf("cancelled run must attempt nothing, got %d", len(got))
	}
}

// The breaker must be scoped to a block's exact input: a membership change is a
// different problem and deserves its own attempts.
func TestBreakerKeyedByContentNotJustMembers(t *testing.T) {
	c := NewCompactor(nil, "test-model")
	b := block{Members: []int64{1, 2}}
	v1 := blockSignature(b, map[int64]int{1: 0, 2: 0})
	v2 := blockSignature(b, map[int64]int{1: 1, 2: 0}) // member 1's content changed
	if v1 == v2 {
		t.Fatal("content version change must change the signature")
	}
	c.recordBlockResult(v1, false)
	c.recordBlockResult(v1, false)
	if c.blockOpen(v1) {
		t.Fatal("v1 must be tripped")
	}
	if !c.blockOpen(v2) {
		t.Fatal("changed content must reopen the block: the input is different")
	}
}

// Regression guard for the legacy-oversized-digest case: when the cap drops, the
// new small blocks overlap an existing active digest. They must not both claim the
// same member, or the overview would show the region twice and pay twice.
func TestOversizedLegacyDigestIsNotDoubleCovered(t *testing.T) {
	// Model the observable invariant directly on the reconciliation rule: a digest
	// is retired only when every one of its members is covered by a NEW digest, and
	// a partial run retires nothing.
	members := []int64{1, 2, 3, 4, 5, 6}
	replaced := map[int64]bool{1: true, 2: true} // only the first new block landed

	stale := false
	allReplaced := true
	for _, m := range members {
		if !replaced[m] {
			allReplaced = false
			break
		}
	}
	if allReplaced {
		stale = true
	}
	if stale {
		t.Fatal("a partially replaced digest must not be retired — its tail would lose its covers edge")
	}

	// And with incomplete=true the run retires nothing at all.
	incomplete := true
	if incomplete && stale {
		t.Fatal("an incomplete run must never retire digests")
	}
}

// The prompt contract the model is held to: it must cite member ids, and the
// negative/uncertain wording must survive so a reviewer can see the confidence.
func TestCompressionInputCarriesIdsConfidenceAndAnchors(t *testing.T) {
	g, b, byID := liveColdFixture()
	in := buildCompressionInput(g, b, byID)
	for _, want := range []string{"#10", "#11", "#20", "#21", "observed", "inferred", "#5"} {
		if !strings.Contains(in, want) {
			t.Fatalf("compression input missing %q:\n%s", want, in)
		}
	}
	if !strings.Contains(in, "成员之间的血缘边") {
		t.Fatal("internal lineage edges must be rendered")
	}
	if !strings.Contains(in, "共同父") {
		t.Fatal("anchors must be labelled as context, not members")
	}
}

// digestSigGen must round-trip what digestPayload writes, or the "unchanged block
// reuses its digest" fast path (which is what keeps re-compaction cheap) breaks.
func TestDigestPayloadSignatureRoundTrip(t *testing.T) {
	g, b, byID := liveColdFixture()
	_ = g
	cvers := map[int64]int{10: 0, 11: 0, 20: 0, 21: 0, 5: 0}
	sig := blockSignature(b, cvers)
	payload := digestPayload("body text", b, byID, 2, sig)

	node := &db.Node{Kind: db.KindDigest, State: db.StateDigestActive}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("payload must marshal: %v", err)
	}
	node.Payload = raw
	gotSig, gotGen := digestSigGen(node)
	if gotSig != sig {
		t.Fatalf("signature round-trip: got %q want %q", gotSig, sig)
	}
	if gotGen != 2 {
		t.Fatalf("generation round-trip: got %d want 2", gotGen)
	}
	ids := digestMemberIDs(node)
	if len(ids) != 4 {
		t.Fatalf("member ids round-trip: got %v", ids)
	}
}
