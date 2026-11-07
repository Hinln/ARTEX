package agent

// cold-digest §4/§5/§7: the Compactor ties the pure algorithms (coldgraph.go)
// to the store (db/digest.go) and the LLM. It runs in two modes:
//
//	maintain — cheap, synchronous, once per planner round: bump round_no,
//	           recompute hot/cold, stamp/clear cold_since_round (§2.3). This is
//	           the bookkeeping the planner does anyway; it never calls the LLM.
//	minor/major — background, off the planner hot path (§7): group cold nodes
//	           and compress each ≥2 block into a digest via the LLM. minor folds
//	           only the not-yet-covered cold set (tiered append); major re-derives
//	           the whole grouping from source and merges fragments (§5.1/§5.2),
//	           reusing bodies whose signature is unchanged (§5.3).
//
// Concurrency: one compaction per task at a time (mutex), ≥cooldown between runs,
// and a commit-time liveness recheck drops any member that revived while the body
// was being generated so a digest never covers a hot node.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"
)

// Compactor performs background cold-node compaction for many explorations.
type Compactor struct {
	prov     llm.Provider
	model    string
	params   coldParams
	n, m     int           // minor / major thresholds (§7 N=20, M=8)
	cooldown time.Duration // min gap between compactions per task (§7 60s)
	maxDur   time.Duration // hard cap on one background compaction

	mu      sync.Mutex
	running map[int64]bool
	lastRun map[int64]time.Time
	// failures counts consecutive failed compaction runs per exploration. A
	// backend that is down (quota, 502, timeout) used to be re-probed ~60s after
	// every planner round, forever: one production task logged 8,986 compress
	// calls for 20 digests. The counter drives an exponential backoff; a single
	// successful run resets it.
	failures map[int64]int
	// blockedUntil is the per-exploration backoff deadline. While it is in the
	// future, OnPlannerRound returns without loading the graph at all.
	blockedUntil map[int64]time.Time
	// blockFailures counts consecutive failures per block signature, so a single
	// block the model cannot summarize (oversized, repeatedly 502'ing) stops
	// costing a call every cooldown while its neighbours keep folding.
	blockFailures map[string]int
	// maxBlocks caps how many blocks one background run folds. A task with
	// thousands of eligible cold nodes otherwise fires thousands of LLM calls in
	// a single un-cancellable burst that outlives several planner rounds.
	maxBlocks int
}

// Compaction backoff: after `tripAfter` consecutive failed runs the exploration
// is skipped for a cooling-off window that doubles each further failure, capped
// at maxCompactBackoff. A block whose own signature failed `blockTripAfter`
// times in a row is skipped until its signature changes (a member's content
// version bumped), because retrying an identical input has no chance of working
// and the model is charged each attempt.
const (
	tripAfter         = 3
	blockTripAfter    = 2
	baseBackoff       = time.Minute
	maxCompactBackoff = 30 * time.Minute
	maxBlocksPerRun   = 8
)

// NewCompactor builds a compactor. prov/model are used for the §4 body LLM call
// (same model the agent runs on, per §4). A nil Compactor is a safe no-op.
func NewCompactor(prov llm.Provider, model string) *Compactor {
	return &Compactor{
		prov:          prov,
		model:         model,
		params:        defaultColdParams(),
		n:             20,
		m:             8,
		cooldown:      60 * time.Second,
		maxDur:        5 * time.Minute,
		running:       map[int64]bool{},
		lastRun:       map[int64]time.Time{},
		failures:      map[int64]int{},
		blockedUntil:  map[int64]time.Time{},
		blockFailures: map[string]int{},
		maxBlocks:     maxBlocksPerRun,
	}
}

// OnPlannerRound is the single entry the planner calls each wake-up. It bumps the
// round, maintains the cold stamps synchronously, then (if a threshold is hit and
// no compaction is running / cooling down) launches a background compaction that
// outlives this planner round.
func (c *Compactor) OnPlannerRound(ctx context.Context, ts *db.ExplorationStore) {
	if c == nil || c.prov == nil || ts == nil {
		return
	}
	round, uncompressed, activeDigests, err := c.maintain(ts)
	if err != nil {
		log.Printf("[compaction] maintain exp=%d: %v", ts.ID(), err)
		return
	}
	needMinor := uncompressed >= c.n
	needMajor := activeDigests >= c.m
	if !needMinor && !needMajor {
		return
	}
	if !c.tryStart(ts.ID()) {
		return // already running, or within cooldown —派生态最终一致，下轮再压
	}
	go func() {
		attempted, ok := 0, 0
		defer func() { c.finish(ts.ID(), attempted > 0 && ok == 0) }()
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.maxDur)
		defer cancel()
		// 压缩是裸 provider 调用（compress 里直接 prov.Complete），不经过 agentcore
		// 的会话循环，所以 ctx 上没有 session id；按 session-id 头做提示缓存/粘性
		// 路由的网关（opencode zen 缺 x-opencode-session 直接 400）就收不到该头。
		// 这里补一个按探索稳定的 id：同一探索的所有压缩请求共享它，既能带上头，
		// 也让 llmrec 能把这次调用的 token 归因回该探索（此前记不到）。
		bg = transcript.WithSessionID(bg, fmt.Sprintf("exp%d-compactor", ts.ID()))
		if needMajor {
			attempted, ok = c.major(bg, ts)
		} else {
			attempted, ok = c.minor(bg, ts)
		}
	}()
	_ = round
}

// maintain bumps round_no, recomputes hot/cold over the whole graph, and applies
// the cold_since_round stamp/clear ops (§2.3). Returns the new round plus the
// counts that drive the trigger: how many eligible-cold nodes are not yet covered
// (minor) and how many active digests exist (major).
func (c *Compactor) maintain(ts *db.ExplorationStore) (round int64, uncompressed, activeDigests int, err error) {
	round, err = ts.BumpRound()
	if err != nil {
		return
	}
	g, _, err := loadColdGraph(ts)
	if err != nil {
		return
	}
	stamps, err := ts.ColdStamps()
	if err != nil {
		return
	}
	hot := g.hotSet()
	structCold := g.structuralCold(hot)
	ops := computeStampOps(structCold, stamps, round)
	if err = ts.ApplyStampOps(toDBStampOps(ops)); err != nil {
		return
	}
	applyStampsInPlace(stamps, ops)
	elig := g.eligibleCold(structCold, stamps, round, c.params)
	covered, err := ts.CoveredMembers()
	if err != nil {
		return
	}
	for id := range elig {
		if _, ok := covered[id]; !ok {
			uncompressed++
		}
	}
	ad, err := ts.ActiveDigests()
	if err != nil {
		return
	}
	activeDigests = len(ad)
	return
}

// minor folds the not-yet-covered eligible-cold set into new digest segments
// (tiered append, §5). Existing digests are untouched. Returns how many blocks
// were attempted and how many succeeded (drives the run-level backoff).
// foldableBatch picks the blocks one background run may attempt: at most limit,
// skipping any whose signature the block breaker has already given up on, and
// stopping early once the run's context is done. It is the single place both
// minor and major apply those three rules, so the bounding can be reasoned about
// (and tested) without a database or a model.
func foldableBatch(ctx context.Context, blocks []block, cvers map[int64]int, limit int, open func(string) bool) []block {
	if limit <= 0 {
		limit = len(blocks)
	}
	out := make([]block, 0, min(limit, len(blocks)))
	for _, b := range blocks {
		if len(out) >= limit || ctx.Err() != nil {
			break
		}
		if open != nil && !open(blockSignature(b, cvers)) {
			continue // identical input already failed — don't pay for another call
		}
		out = append(out, b)
	}
	return out
}

func (c *Compactor) minor(ctx context.Context, ts *db.ExplorationStore) (attempted, ok int) {
	round, err := ts.RoundNo()
	if err != nil {
		return 0, 0
	}
	g, nodeByID, err := loadColdGraph(ts)
	if err != nil {
		return 0, 0
	}
	stamps, err := ts.ColdStamps()
	if err != nil {
		return 0, 0
	}
	cvers, err := ts.ContentVersions()
	if err != nil {
		return 0, 0
	}
	covered, err := ts.CoveredMembers()
	if err != nil {
		return 0, 0
	}
	hot := g.hotSet()
	elig := g.eligibleCold(g.structuralCold(hot), stamps, round, c.params)
	uncompressed := map[int64]bool{}
	for id := range elig {
		if _, ok := covered[id]; !ok {
			uncompressed[id] = true
		}
	}
	blocks := g.group(uncompressed, c.params)
	if len(blocks) == 0 {
		return 0, 0 // this batch has no ≥2 connected/shared-parent block — nothing to fold (§7)
	}
	for _, b := range foldableBatch(ctx, blocks, cvers, c.maxBlocks, c.blockOpen) {
		attempted++
		if c.foldBlock(ctx, ts, g, b, nodeByID, cvers, c.generationFor(b, nil)) {
			ok++
		}
	}
	// A minor may have pushed the segment count over M → merge in the same run.
	if ad, e := ts.ActiveDigests(); e == nil && len(ad) >= c.m {
		a2, o2 := c.major(ctx, ts)
		attempted += a2
		ok += o2
	}
	return attempted, ok
}

// major re-derives the whole grouping from source over ALL eligible-cold nodes
// (§5.1 回源重压), then reconciles against the active digests by signature:
// unchanged blocks keep their digest (no LLM), stale digests are superseded, and
// new/changed blocks are compressed afresh. This is where tiered fragments of one
// direction merge and where "later became connected" blocks unify (§5.2).
func (c *Compactor) major(ctx context.Context, ts *db.ExplorationStore) (attempted, ok int) {
	round, err := ts.RoundNo()
	if err != nil {
		return 0, 0
	}
	g, nodeByID, err := loadColdGraph(ts)
	if err != nil {
		return 0, 0
	}
	stamps, err := ts.ColdStamps()
	if err != nil {
		return 0, 0
	}
	cvers, err := ts.ContentVersions()
	if err != nil {
		return 0, 0
	}
	active, err := ts.ActiveDigests()
	if err != nil {
		return 0, 0
	}
	hot := g.hotSet()
	elig := g.eligibleCold(g.structuralCold(hot), stamps, round, c.params)
	blocks := g.group(elig, c.params)

	bySig := map[string]*db.Node{}
	digestSig := map[int64]string{} // digest id → signature
	for _, d := range active {
		sig, _ := digestSigGen(d)
		bySig[sig] = d
		digestSig[d.ID] = sig
	}
	desired := map[string]bool{}
	var toCreate []block
	for _, b := range blocks {
		sig := blockSignature(b, cvers)
		desired[sig] = true
		if _, ok := bySig[sig]; ok {
			continue // unchanged → reuse the existing digest, skip LLM (§5.3)
		}
		toCreate = append(toCreate, b)
	}
	// Replace stale digests only where the NEW grouping reproduces their members.
	//
	// Members of a retired digest are exactly the eligible-cold nodes it covers, so
	// the new grouping covers them too unless the grouping changed shape (an
	// ineligible member, a revived branch, or a different membership cap). Deciding
	// supersede from that structural fact — instead of "whoever has a freshly
	// created digest" — is what keeps a lowered MaxMembers from silently dropping
	// the tail of a legacy oversized digest: those members stay covered by the old
	// digest until the new grouping actually includes them.
	//
	// A digest is replaced only when the run attempted every block whose signature
	// is absent from the active set (i.e. nothing was skipped by the block breaker
	// or the per-run cap); otherwise it is kept so its covers edges remain the
	// source of truth until a later run finishes the job.
	attempted, ok = 0, 0
	replaced := map[int64]bool{} // members now covered by a freshly created digest
	incomplete := len(toCreate) > len(foldableBatch(ctx, toCreate, cvers, c.maxBlocks, c.blockOpen))
	for _, b := range foldableBatch(ctx, toCreate, cvers, c.maxBlocks, c.blockOpen) {
		attempted++
		if c.foldBlock(ctx, ts, g, b, nodeByID, cvers, c.generationFor(b, active)) {
			ok++
			for _, m := range b.Members {
				replaced[m] = true
			}
		}
	}
	if incomplete {
		log.Printf("[compaction] exp=%d 本轮未覆盖全部待压块（上限/熔断），保留旧 digest 直到后续轮次补齐", ts.ID())
	}
	var stale []int64
	for _, d := range active {
		if desired[digestSig[d.ID]] {
			continue // this digest's block survived unchanged
		}
		members := digestMemberIDs(d)
		if len(members) == 0 {
			stale = append(stale, d.ID)
			continue
		}
		if incomplete {
			continue // never retire a digest while its replacement set is partial
		}
		allReplaced := true
		for _, m := range members {
			if !replaced[m] {
				allReplaced = false
				break
			}
		}
		if allReplaced {
			stale = append(stale, d.ID)
		}
	}
	if err := ts.SupersedeDigests(stale); err != nil {
		log.Printf("[compaction] supersede exp=%d: %v", ts.ID(), err)
	}
	return attempted, ok
}

// foldBlock compresses one block and writes its digest — with a commit-time
// liveness recheck (§ concurrency): between grouping and write the graph may have
// changed, so any member that has since gone hot (revived) is dropped from the
// covers set. If the block dissolves below K it is skipped.
//
// Returns whether the model call itself succeeded. That (not "a digest was
// written") is what the run-level backoff keys off: a block that revived out
// from under us is a healthy backend, while a compress error means the endpoint
// is failing and the run should not keep spending calls.
func (c *Compactor) foldBlock(ctx context.Context, ts *db.ExplorationStore, g *coldGraph, b block, nodeByID map[int64]*db.Node, cvers map[int64]int, generation int) bool {
	sig := blockSignature(b, cvers)
	body, err := c.compress(ctx, g, b, nodeByID)
	if err != nil {
		// A cancellation is the run winding down (deadline), not a content
		// problem: don't poison the block's counter with it.
		if ctx.Err() == nil {
			c.recordBlockResult(sig, false)
		}
		log.Printf("[compaction] compress exp=%d block=%v: %v", ts.ID(), b.Members, err)
		return false
	}
	c.recordBlockResult(sig, true)
	// Re-read fresh state and drop any member that revived while we compressed.
	fresh, _, err := loadColdGraph(ts)
	if err != nil {
		return true
	}
	freshHot := fresh.hotSet()
	members := make([]int64, 0, len(b.Members))
	for _, mID := range b.Members {
		if !freshHot[mID] {
			members = append(members, mID)
		}
	}
	if len(members) < c.params.K {
		return true // block revived out from under us — leave those nodes hot, don't fold
	}
	final := block{Members: members, Anchors: b.Anchors}
	payload := digestPayload(body, final, nodeByID, generation, blockSignature(final, cvers))
	if _, err := ts.AddDigest(payload, members); err != nil {
		log.Printf("[compaction] add digest exp=%d: %v", ts.ID(), err)
		return false
	}
	return true
}

// generationFor computes a digest's重摘代次 (§1): 1 for a fresh fold; for a major
// merge, max(generation) over the active digests that overlap this block's
// members, +1.
func (c *Compactor) generationFor(b block, active []*db.Node) int {
	if len(active) == 0 {
		return 1
	}
	memberSet := make(map[int64]bool, len(b.Members))
	for _, m := range b.Members {
		memberSet[m] = true
	}
	best := 0
	for _, d := range active {
		_, gen := digestSigGen(d)
		for _, m := range digestMemberIDs(d) {
			if memberSet[m] {
				if gen > best {
					best = gen
				}
				break
			}
		}
	}
	return best + 1
}

// tryStart acquires the per-task compaction lock, honoring the cooldown and the
// failure backoff. While a task is backing off, this returns before the caller
// loads the graph, so a broken backend costs nothing at all.
func (c *Compactor) tryStart(expID int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running[expID] {
		return false
	}
	if until, ok := c.blockedUntil[expID]; ok && time.Now().Before(until) {
		return false
	}
	if t, ok := c.lastRun[expID]; ok && time.Since(t) < c.cooldown {
		return false
	}
	c.running[expID] = true
	return true
}

// finish releases the per-task lock and records whether the run folded anything
// successfully. A run in which every attempted block failed (backend down, quota
// exhausted, timeouts) starts the exponential backoff so the exploration stops
// burning a model call per planner round; any single success clears it.
func (c *Compactor) finish(expID int64, failed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.running[expID] = false
	c.lastRun[expID] = time.Now()
	if !failed {
		c.failures[expID] = 0
		delete(c.blockedUntil, expID)
		return
	}
	c.failures[expID]++
	if n := c.failures[expID]; n >= tripAfter {
		wait := baseBackoff << uint(n-tripAfter) // 1m, 2m, 4m, ... capped below
		if wait > maxCompactBackoff || wait <= 0 {
			wait = maxCompactBackoff
		}
		c.blockedUntil[expID] = time.Now().Add(wait)
		log.Printf("[compaction] exp=%d 连续 %d 次压缩失败，退避 %s（此前每轮都在重试）", expID, n, wait)
	}
}

// blockOpen reports whether a block signature may still be attempted, and
// recordBlockResult records the outcome. A block whose identical input failed
// blockTripAfter times is not retried until its signature changes (a member's
// content version bumped), because a deterministic failure cannot heal by being
// re-sent — but the model is still charged for every attempt.
func (c *Compactor) blockOpen(sig string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.blockFailures[sig] < blockTripAfter
}

func (c *Compactor) recordBlockResult(sig string, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ok {
		delete(c.blockFailures, sig)
		return
	}
	c.blockFailures[sig]++
}

// --- helpers: db ↔ coldgraph ---

// loadColdGraph reads the exploration's nodes + edges and builds the cold-graph
// view plus an id→node index (for summaries/payload during compression).
func loadColdGraph(ts *db.ExplorationStore) (*coldGraph, map[int64]*db.Node, error) {
	// Compaction must see the WHOLE graph, not the default row caps — pass a very
	// high limit so the LIMIT clause is effectively unbounded for real task sizes.
	const allRows = 1 << 30
	nodes, err := ts.Nodes(allRows)
	if err != nil {
		return nil, nil, err
	}
	edges, err := ts.Edges(allRows)
	if err != nil {
		return nil, nil, err
	}
	cgNodes := make([]cgNode, 0, len(nodes))
	byID := make(map[int64]*db.Node, len(nodes))
	for _, n := range nodes {
		cgNodes = append(cgNodes, cgNode{ID: n.ID, Kind: n.Kind, State: n.State})
		byID[n.ID] = n
	}
	cgEdges := make([]cgEdge, 0, len(edges))
	for _, e := range edges {
		cgEdges = append(cgEdges, cgEdge{From: e.From, Rel: e.Rel, To: e.To})
	}
	return newColdGraph(cgNodes, cgEdges), byID, nil
}

// loadColdShape builds the cold-graph view WITHOUT any node payload. This is the
// read the planner overview needs every round (hot/cold reachability over the
// whole edge set); the full loadColdGraph reads every payload column and is only
// justified when the caller actually renders summaries. On a production task
// with ~4.3k nodes the payload column alone was ~13 MB per call, paid twice per
// planner round before this split.
func loadColdShape(ts *db.ExplorationStore) (*coldGraph, error) {
	nodes, err := ts.LiteNodes()
	if err != nil {
		return nil, err
	}
	// Edges must be unbounded here: the previous overview path used Edges(5000),
	// which silently dropped lineage once a task passed 5,000 edges (several
	// production tasks were at 5,072–6,383), so parents/yields/from_intent came
	// back incomplete. Edges(0) means "default 5000", so pass an explicit cap.
	const allEdges = 1 << 30
	edges, err := ts.Edges(allEdges)
	if err != nil {
		return nil, err
	}
	cgNodes := make([]cgNode, 0, len(nodes))
	for _, n := range nodes {
		cgNodes = append(cgNodes, cgNode{ID: n.ID, Kind: n.Kind, State: n.State})
	}
	cgEdges := make([]cgEdge, 0, len(edges))
	for _, e := range edges {
		cgEdges = append(cgEdges, cgEdge{From: e.From, Rel: e.Rel, To: e.To})
	}
	return newColdGraph(cgNodes, cgEdges), nil
}

func toDBStampOps(ops []stampOp) []db.StampOp {
	out := make([]db.StampOp, len(ops))
	for i, o := range ops {
		out[i] = db.StampOp{ID: o.ID, Set: o.Set, Round: o.Round}
	}
	return out
}

// applyStampsInPlace folds the just-applied ops into the in-memory stamp map so
// eligibility can be computed immediately without a re-read.
func applyStampsInPlace(stamps map[int64]*int64, ops []stampOp) {
	for _, o := range ops {
		if o.Set {
			r := o.Round
			stamps[o.ID] = &r
		} else {
			stamps[o.ID] = nil
		}
	}
}

// --- helpers: digest payload ---

// digestPayload builds the digest node payload (cold-digest §1): the body, the
// member ids split by kind (restore cache; source of truth is the covers edges),
// the anchor ids, the generation, and the change-detection signature.
func digestPayload(body string, b block, nodeByID map[int64]*db.Node, generation int, signature string) map[string]any {
	var facts, intents []int64
	for _, m := range b.Members {
		if n := nodeByID[m]; n != nil && n.Kind == db.KindIntent {
			intents = append(intents, m)
		} else {
			facts = append(facts, m)
		}
	}
	return map[string]any{
		"body":       body,
		"member_ids": map[string]any{"facts": facts, "intents": intents},
		"anchor_ids": b.Anchors,
		"generation": generation,
		"signature":  signature,
	}
}

func digestSigGen(n *db.Node) (string, int) {
	var p struct {
		Signature  string `json:"signature"`
		Generation int    `json:"generation"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	return p.Signature, p.Generation
}

func digestMemberIDs(n *db.Node) []int64 {
	var p struct {
		MemberIDs struct {
			Facts   []int64 `json:"facts"`
			Intents []int64 `json:"intents"`
		} `json:"member_ids"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	return append(append([]int64{}, p.MemberIDs.Facts...), p.MemberIDs.Intents...)
}

// --- helpers: compression input + LLM (§4) ---

func nodeSummary(n *db.Node) string {
	if n == nil {
		return ""
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if s, ok := p["summary"].(string); ok {
			return s
		}
		if t, ok := p["text"].(string); ok {
			return t
		}
	}
	return ""
}

func nodeConfidence(n *db.Node) string {
	if n == nil {
		return ""
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if c, ok := p["confidence"].(string); ok {
			return c
		}
	}
	return ""
}

// buildCompressionInput renders the connected sub-graph for the §4 prompt:
// member nodes (summary + id + kind + state + confidence), the internal blood
// edges among members, and — for a §3.1 shared-parent group — the anchor parents
// as context ("共同父 #p"), which are NOT members.
func buildCompressionInput(g *coldGraph, b block, nodeByID map[int64]*db.Node) string {
	memberSet := make(map[int64]bool, len(b.Members))
	for _, m := range b.Members {
		memberSet[m] = true
	}
	var sb strings.Builder
	sb.WriteString("【成员节点（要压缩的）】：\n")
	for _, m := range b.Members {
		n := nodeByID[m]
		kind := "fact"
		if n != nil && n.Kind == db.KindIntent {
			kind = "intent"
		}
		state := ""
		if n != nil {
			state = n.State
		}
		line := fmt.Sprintf("- #%d [%s/%s] %s", m, kind, state, nodeSummary(n))
		if conf := nodeConfidence(n); conf != "" {
			line += fmt.Sprintf(" (confidence=%s)", conf)
		}
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	// internal edges among members
	var edgeLines []string
	for _, m := range b.Members {
		for _, to := range g.children[m] {
			if memberSet[to] {
				edgeLines = append(edgeLines, fmt.Sprintf("- #%d 产出/派生→ #%d", m, to))
			}
		}
	}
	if len(edgeLines) > 0 {
		sb.WriteString("\n【成员之间的血缘边（父→子）】：\n")
		sort.Strings(edgeLines)
		sb.WriteString(strings.Join(edgeLines, "\n"))
		sb.WriteByte('\n')
	}
	if len(b.Anchors) > 0 {
		sb.WriteString("\n【共同父 / 上下文锚（不是成员，只用于理解这些结果从哪个意图探出）】：\n")
		for _, a := range b.Anchors {
			n := nodeByID[a]
			state := ""
			if n != nil {
				state = n.State
			}
			fmt.Fprintf(&sb, "- #%d [%s] %s\n", a, state, nodeSummary(n))
		}
	}
	return sb.String()
}

// compress runs the §4 body LLM call on one block. Uses the same model the agent
// runs on; thinking disabled (a pure summarization step).
//
// The output budget scales with the block. A fixed 1500-token cap was hit on
// 99% of successful calls (measured: 176,773 / 178,591 responses stopped at
// exactly 1500) and the answer was stored anyway, so most digests ended
// mid-sentence — and a truncated digest is worse than no digest, because the
// planner trusts it. The stop reason is therefore checked: an answer cut off at
// the cap is treated as a failure so the block is retried with a bigger budget
// (up to compressMaxTokens) or, failing that, left unfolded.
func (c *Compactor) compress(ctx context.Context, g *coldGraph, b block, nodeByID map[int64]*db.Node) (string, error) {
	input := buildCompressionInput(g, b, nodeByID)
	maxTok := compressTokensFor(len(b.Members))
	req := llm.CompletionRequest{
		System:    []string{compressionSystemPrompt},
		Messages:  []llm.Message{llm.UserText(input)},
		MaxTokens: maxTok,
		Thinking:  "disabled",
	}
	msg, stop, _, err := c.prov.Complete(ctx, req)
	if err != nil {
		return "", err
	}
	body := strings.TrimSpace(msg.Text())
	if body == "" {
		return "", fmt.Errorf("empty body from model")
	}
	// "max_tokens"/"length" = the model ran out of budget mid-sentence. Storing
	// that as a digest silently loses the tail of the block, so reject it and let
	// the block-level backoff stop the retry once it is clear the budget is not
	// the problem.
	if isTruncatedStop(stop) {
		return "", fmt.Errorf("body truncated at max_tokens=%d (%d members)", maxTok, len(b.Members))
	}
	return body, nil
}

// compression output budget. Roughly one body token per two member summaries,
// bounded so a single call never dominates the context: a 64-member block gets
// 2048 tokens, a 2-member block the 768 floor.
const (
	compressMinTokens     = 768
	compressMaxTokens     = 4096
	compressTokensPerNode = 32
)

func compressTokensFor(members int) int {
	return min(compressMaxTokens, max(compressMinTokens, members*compressTokensPerNode))
}

// isTruncatedStop reports whether a provider stop reason means the output was
// cut off by the token cap. Norma normalizes OpenAI's "length" to "max_tokens".
func isTruncatedStop(stop string) bool {
	switch strings.ToLower(strings.TrimSpace(stop)) {
	case "max_tokens", "length", "max_output_tokens":
		return true
	}
	return false
}

// compressionSystemPrompt is the §4 body prompt.
const compressionSystemPrompt = `你在压缩一组【彼此关联】的探索节点，产出一段综合结论(body)，供规划者快速掌握"这一片已经探明了什么"。

输入是一个连通子图：
- 节点：每条是一个意图或事实的 summary（一句话），带 id、类型(intent/fact)、state、confidence(若有)。
- 关系：节点之间的血缘边（A 派生自 B / A 产出 B），说明它们如何串联。
- 若节点间没有直接血缘边、但同属一个上游意图（会另给出该上游意图作为"共同父 #p"），则按"这个意图（#p）探到了什么"来综合它们——共同父只是上下文锚、不是要压缩的成员。

据此写一段 body：
1. 综合、不罗列：顺着关系把因果串起来（哪个事实催生哪个意图、哪条意图产出了哪个结论），讲成"这一片探索得出了什么"，不要把每条 summary 抄一遍。
2. 保留区分度：彼此不同的结论分别说清，别揉成一句笼统的话。
3. 保留证据强度：带 confidence 的结论标出 observed / inferred；inferred 的否定/存疑结论要点明它只是推断、可复核，别写成定论。
4. 带上 id：每条结论后标注来源节点 id（如"…（#12,#28）"），让规划者能按 id 还原原节点。
5. 正向陈述、只写输入里有的：不脑补、不引入输入中没有的判断。
6. 长度随内容自适应：结论少就短，多且互不相同就写够——但整体显著短于所有输入 summary 的总和。

只输出 body 正文本身。`
