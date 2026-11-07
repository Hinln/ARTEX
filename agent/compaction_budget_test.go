package agent

import (
	"context"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

// dbRelDerived mirrors db.RelDerivedFrom for the pure-graph tests.
const dbRelDerived = db.RelDerivedFrom

// stubProvider is a minimal llm.Provider that records requests and replays a
// scripted outcome. The compaction tests below only need Complete.
type stubProvider struct {
	mu      sync.Mutex
	calls   int
	inputs  []string
	maxToks []int
	reply   func(call int) (string, string, error) // body, stop reason, err
}

func (s *stubProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	s.mu.Lock()
	s.calls++
	n := s.calls
	s.inputs = append(s.inputs, req.Messages[0].Text())
	s.maxToks = append(s.maxToks, req.MaxTokens)
	s.mu.Unlock()
	body, stop, err := s.reply(n)
	if err != nil {
		return llm.Message{}, "", llm.Usage{}, err
	}
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(body)}}, stop, llm.Usage{}, nil
}

func (s *stubProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {}
}

// §7 hardening: one connected component larger than MaxMembers is split into
// consecutive id-ordered chunks instead of becoming one enormous block. A
// production block reached 3,023 members and 1M+ input tokens.
func TestGroupSplitsOversizedComponent(t *testing.T) {
	var nodes []cgNode
	var edges []cgEdge
	// 1..130 as a single chain of settled facts: one connected component.
	for i := int64(1); i <= 130; i++ {
		nodes = append(nodes, fact(i))
		if i > 1 {
			edges = append(edges, cgEdge{From: i - 1, Rel: dbRelDerived, To: i})
		}
	}
	g := newColdGraph(nodes, edges)
	set := map[int64]bool{}
	for i := int64(1); i <= 130; i++ {
		set[i] = true
	}
	p := coldParams{R: 6, K: 2, MaxMembers: 64}
	blocks := g.group(set, p)
	if len(blocks) < 2 {
		t.Fatalf("oversized component must split, got %d block(s)", len(blocks))
	}
	total := 0
	for _, b := range blocks {
		if len(b.Members) > p.MaxMembers {
			t.Fatalf("block has %d members, cap is %d", len(b.Members), p.MaxMembers)
		}
		if len(b.Members) < p.K {
			t.Fatalf("chunk of %d must have been folded into a neighbour (K=%d)", len(b.Members), p.K)
		}
		total += len(b.Members)
	}
	if total != 130 {
		t.Fatalf("chunks must cover every member exactly once: got %d, want 130", total)
	}
}

func TestChunkIDsFoldsLoneRemainder(t *testing.T) {
	ids := []int64{1, 2, 3, 4, 5}
	got := chunkIDs(ids, 2)
	// 2/2/1 → the lone trailing id must not stay a singleton (unfoldable at K=2).
	for _, c := range got {
		if len(c) < 2 {
			t.Fatalf("chunk %v has fewer than K=2 members", c)
		}
	}
	var flat []int64
	for _, c := range got {
		flat = append(flat, c...)
	}
	if len(flat) != len(ids) {
		t.Fatalf("chunkIDs lost members: %v vs %v", flat, ids)
	}
	// Unbounded (MaxMembers=0) keeps the single component intact.
	if got := chunkIDs(ids, 0); len(got) != 1 {
		t.Fatalf("MaxMembers=0 must not split, got %d chunks", len(got))
	}
}

// A body cut off at the token cap must be rejected: storing a half sentence as a
// digest is worse than leaving the block unfolded, because the planner trusts it.
func TestCompressRejectsTruncatedBody(t *testing.T) {
	prov := &stubProvider{reply: func(int) (string, string, error) {
		return "这是一段被截断的结论，最后一句写到一半", "max_tokens", nil
	}}
	c := NewCompactor(prov, "test-model")
	g := newColdGraph([]cgNode{fact(1), fact(2)}, nil)
	b := block{Members: []int64{1, 2}}
	_, err := c.compress(context.Background(), g, b, map[int64]*db.Node{})
	if err == nil {
		t.Fatal("a max_tokens stop reason must not be stored as a digest body")
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCompressAcceptanceAndBudgetScaling(t *testing.T) {
	prov := &stubProvider{reply: func(int) (string, string, error) {
		return "完整结论（#1,#2）。", "end_turn", nil
	}}
	c := NewCompactor(prov, "test-model")
	g := newColdGraph([]cgNode{fact(1), fact(2)}, nil)

	small := block{Members: []int64{1, 2}}
	if _, err := c.compress(context.Background(), g, small, map[int64]*db.Node{}); err != nil {
		t.Fatalf("complete body must be accepted: %v", err)
	}

	var big block
	for i := int64(1); i <= 64; i++ {
		big.Members = append(big.Members, i)
	}
	if _, err := c.compress(context.Background(), g, big, map[int64]*db.Node{}); err != nil {
		t.Fatalf("complete body must be accepted: %v", err)
	}
	if prov.maxToks[0] != compressMinTokens {
		t.Fatalf("2-member block budget = %d, want floor %d", prov.maxToks[0], compressMinTokens)
	}
	if prov.maxToks[1] <= prov.maxToks[0] {
		t.Fatalf("budget must scale with block size: %d vs %d", prov.maxToks[0], prov.maxToks[1])
	}
	if prov.maxToks[1] > compressMaxTokens {
		t.Fatalf("budget %d exceeds cap %d", prov.maxToks[1], compressMaxTokens)
	}
}

// A block whose identical input keeps failing must stop being retried: the model
// is charged for every attempt, and a deterministic failure never heals.
func TestBlockCircuitBreaker(t *testing.T) {
	c := NewCompactor(nil, "test-model")
	sig := "sig-1"
	if !c.blockOpen(sig) {
		t.Fatal("a fresh block must be open")
	}
	c.recordBlockResult(sig, false)
	if !c.blockOpen(sig) {
		t.Fatalf("must stay open until %d failures", blockTripAfter)
	}
	c.recordBlockResult(sig, false)
	if c.blockOpen(sig) {
		t.Fatalf("after %d failures the block must stop being retried", blockTripAfter)
	}
	// A different signature (membership/content changed) is a different input.
	if !c.blockOpen("sig-2") {
		t.Fatal("breaker must be per-signature")
	}
	// Success clears it.
	c.recordBlockResult(sig, true)
	if !c.blockOpen(sig) {
		t.Fatal("success must reset the block breaker")
	}
}

// A run where every block failed starts the exploration-level backoff, so a
// dead backend is not re-probed every planner round.
func TestRunBackoffAfterRepeatedFailures(t *testing.T) {
	c := NewCompactor(nil, "test-model")
	const exp = int64(7)

	for i := 0; i < tripAfter; i++ {
		if !c.tryStart(exp) {
			t.Fatalf("run %d should start", i)
		}
		c.finish(exp, true)
		c.mu.Lock()
		c.lastRun[exp] = c.lastRun[exp].Add(-2 * c.cooldown) // simulate time passing
		c.mu.Unlock()
	}
	c.mu.Lock()
	until := c.blockedUntil[exp]
	c.mu.Unlock()
	if until.IsZero() || !until.After(time.Now()) {
		t.Fatalf("expected a backoff deadline after %d failed runs, got %v", tripAfter, until)
	}
	if c.tryStart(exp) {
		t.Fatal("a backing-off exploration must not start a new run")
	}
	// A successful run clears the backoff.
	c.mu.Lock()
	c.blockedUntil[exp] = time.Time{}
	c.running[exp] = false
	c.lastRun[exp] = time.Time{}
	c.mu.Unlock()
	if !c.tryStart(exp) {
		t.Fatal("should start once the backoff is over")
	}
	c.finish(exp, false)
	if !c.blockedUntil[exp].IsZero() {
		t.Fatal("success must clear the backoff")
	}
}

// compressTokensFor must stay inside its bounds for any block size.
func TestCompressTokensForBounds(t *testing.T) {
	cases := map[int]int{0: compressMinTokens, 1: compressMinTokens, 2: compressMinTokens, 64: 2048, 10000: compressMaxTokens}
	for members, want := range cases {
		if got := compressTokensFor(members); got != want {
			t.Fatalf("compressTokensFor(%d) = %d, want %d", members, got, want)
		}
	}
}
