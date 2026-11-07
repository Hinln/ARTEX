package server

// The reproduce decorator sits between the agent's report_finding tool and the
// evidence store: it records the finding exactly as before, then (only for
// severities that require it) re-sends the finding's own proof traffic and
// writes the machine verdict. The agent's tool result carries that verdict, so a
// model that claimed a high-severity finding sees "系统复现：未通过" in the same
// turn instead of a silent success.
//
// Ordering matters: the finding row must exist before a reproduction row can
// reference it (FK), and the evidence version must be final before a pass can be
// tied to it. Both hold once Record returns.

import (
	"context"
	"log"

	"github.com/Autumn-27/artex/db"
)

// ReproduceRecorder decorates a FindingRecorder with post-record reproduction.
type ReproduceRecorder struct {
	inner FindingRecorder
	repro *Reproducer
	pg    *db.DB
}

// FindingRecorder is the interface the agents call (agent.FindingRecorder is
// structurally identical; keeping this local avoids importing agent here).
type FindingRecorder interface {
	Record(context.Context, db.RecordFindingInput, []db.TrafficRef) (*db.RecordedFinding, error)
}

func NewReproduceRecorder(inner FindingRecorder, repro *Reproducer, pg *db.DB) *ReproduceRecorder {
	return &ReproduceRecorder{inner: inner, repro: repro, pg: pg}
}

func (r *ReproduceRecorder) Record(ctx context.Context, in db.RecordFindingInput, refs []db.TrafficRef) (*db.RecordedFinding, error) {
	out, err := r.inner.Record(ctx, in, refs)
	if err != nil || out == nil {
		return out, err
	}
	if r.repro == nil || r.pg == nil || !r.repro.NeededFor(in.VulnClass, in.Severity) {
		return out, nil
	}
	// Reproduction is best-effort by design: a failure to verify must never roll
	// back a finding the worker did record. It lowers the finding's confidence
	// instead, which is the honest outcome.
	res, err := r.repro.Reproduce(ctx, out.FindingID)
	if err != nil {
		log.Printf("[reproduce] finding %d: 复现失败: %v", out.FindingID, err)
		r.syncConfidence(ctx, out.FindingID)
		return out, nil
	}
	ids := make([]string, 0, len(res.TrafficIDs))
	ids = append(ids, res.TrafficIDs...)
	_, recErr := r.pg.RecordReproduction(ctx, db.ReproduceState{
		FindingID:      out.FindingID,
		ExplorationID:  in.ExplorationID,
		State:          res.State,
		Reason:         res.Reason,
		StatusCode:     res.StatusCode,
		ResponseDigest: res.ResponseDigest,
		BaselineDigest: res.BaselineDigest,
		Matched:        res.Matched,
		EvidenceVer:    out.Traffic.Version,
		TrafficIDs:     ids,
		DurationMs:     int(res.Duration.Milliseconds()),
	})
	if recErr != nil {
		log.Printf("[reproduce] finding %d: 记录复现结论失败: %v", out.FindingID, recErr)
		r.syncConfidence(ctx, out.FindingID)
		return out, nil
	}
	log.Printf("[reproduce] finding %d severity=%s → %s (%s)", out.FindingID, in.Severity, res.State, res.Reason)
	return out, nil
}

// syncConfidence mirrors a completed verification attempt into the finding's
// confidence. Without it a finding whose replay failed stayed "confirmed"
// because the model had asserted it; confidence is a property of the evidence.
func (r *ReproduceRecorder) syncConfidence(ctx context.Context, findingID int64) {
	if r == nil || r.pg == nil || findingID <= 0 {
		return
	}
	if err := r.pg.SyncFindingConfidence(ctx, findingID); err != nil {
		log.Printf("[reproduce] finding %d: 复现结论同步失败: %v", findingID, err)
	}
}