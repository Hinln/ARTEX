package db

// Reproduce + confidence: the server-owned half of a finding's credibility.
//
// A model writes `severity` and `evidence`; those are claims. This file stores the
// one thing a model cannot grant itself — the result of the SYSTEM re-sending the
// finding's own proof request against the live target and comparing what came
// back. That is what makes "复现" mean something: a finding may only be shown as
// confirmed when a reproduced=true row exists with a matching evidence version.
//
// Deliberately separate from findings.status: status is the human workflow
// (pending / fixed / dismissed), while reproduce_state is machine evidence.
// Conflating them would let a reviewer's "I believe it" overwrite "the system
// could not reproduce it".

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Reproduce states. "" (absent) means never attempted; the API maps that to
// "unverified".
const (
	ReproducePassed  = "passed"
	ReproduceFailed  = "failed"
	ReproduceSkipped = "skipped" // destructive/write PoC or no replayable request
	ReproduceBlocked = "blocked" // target unreachable, auth missing, WAF, budget
)

// FindingConfidence is the credibility a finding may be displayed with.
//
// confirmed REQUIRES a passed reproduce row at the current evidence version:
// confidence is derived, never accepted from the model.
const (
	ConfidenceConfirmed = "confirmed"
	ConfidenceLikely    = "likely"
	ConfidenceUncertain = "uncertain"
)

// ReproduceState row is one attempt, kept append-only so a later failure does not
// erase an earlier success (reviewers need the history to explain flapping).
type ReproduceState struct {
	ID             int64     `json:"id"`
	FindingID      int64     `json:"finding_id"`
	ExplorationID  int64     `json:"exploration_id"`
	State          string    `json:"state"`
	Reason         string    `json:"reason"`
	StatusCode     int       `json:"status_code"`
	ResponseDigest string    `json:"response_digest"` // sha256 of the replayed body
	BaselineDigest string    `json:"baseline_digest"` // sha256 of the recorded body
	Matched        bool      `json:"matched"`         // response matched the baseline
	EvidenceVer    int64     `json:"evidence_version"`
	TrafficIDs     []string  `json:"traffic_ids"`
	DurationMs     int       `json:"duration_ms"`
	CreatedAt      time.Time `json:"created_at"`
}

const reproduceSchema = `
CREATE TABLE IF NOT EXISTS finding_reproductions (
    id              BIGSERIAL PRIMARY KEY,
    finding_id      BIGINT NOT NULL REFERENCES findings(id) ON DELETE CASCADE,
    exploration_id  BIGINT NOT NULL DEFAULT 0,
    state           TEXT NOT NULL,
    reason          TEXT NOT NULL DEFAULT '',
    status_code     INTEGER NOT NULL DEFAULT 0,
    response_digest TEXT NOT NULL DEFAULT '',
    baseline_digest TEXT NOT NULL DEFAULT '',
    matched         BOOLEAN NOT NULL DEFAULT FALSE,
    evidence_version BIGINT NOT NULL DEFAULT 0,
    traffic_ids     JSONB NOT NULL DEFAULT '[]'::jsonb,
    duration_ms     INTEGER NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_repro_finding ON finding_reproductions(finding_id, id DESC);
ALTER TABLE findings ADD COLUMN IF NOT EXISTS confidence TEXT NOT NULL DEFAULT '';
ALTER TABLE findings ADD COLUMN IF NOT EXISTS reproduced BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE findings ADD COLUMN IF NOT EXISTS reproduce_state TEXT NOT NULL DEFAULT '';
`

// EnsureReproduceSchema is idempotent and runs at startup with the other schema
// steps. The findings columns are additive, so existing rows read as
// unverified/unconfirmed — never as silently confirmed.
func (d *DB) EnsureReproduceSchema() error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := coordinateWithSchemaMigration(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(reproduceSchema); err != nil {
		return fmt.Errorf("reproduce schema: %w", err)
	}
	return tx.Commit()
}

// RecordReproduction appends one attempt and mirrors the latest state onto the
// finding row (atomically, so a reader never sees a stale mirror).
//
// confidence is recomputed here, not taken from the caller: only a PASSED run at
// the finding's current evidence_version may promote it to confirmed. Any other
// outcome caps it at likely, because an unverified claim is a claim.
func (d *DB) RecordReproduction(ctx context.Context, in ReproduceState) (int64, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck
	ids, _ := json.Marshal(in.TrafficIDs)
	var id int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO finding_reproductions
(finding_id, exploration_id, state, reason, status_code, response_digest, baseline_digest, matched, evidence_version, traffic_ids, duration_ms)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`,
		in.FindingID, in.ExplorationID, in.State, in.Reason, in.StatusCode,
		in.ResponseDigest, in.BaselineDigest, in.Matched, in.EvidenceVer, ids, in.DurationMs).Scan(&id); err != nil {
		return 0, err
	}
	conf := ConfidenceLikely
	if in.State == ReproducePassed {
		conf = ConfidenceConfirmed
	} else if in.State == ReproduceSkipped {
		conf = ConfidenceUncertain
	}
	if _, err := tx.ExecContext(ctx, `UPDATE findings
SET confidence=$2, reproduced=($3='passed'), reproduce_state=$3 WHERE id=$1`,
		in.FindingID, conf, in.State); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// LatestReproduction returns the most recent attempt for a finding. ok=false when
// none exists.
func (d *DB) LatestReproduction(ctx context.Context, findingID int64) (ReproduceState, bool, error) {
	var r ReproduceState
	var ids []byte
	err := d.QueryRowContext(ctx, `SELECT id, finding_id, exploration_id, state, reason, status_code,
response_digest, baseline_digest, matched, evidence_version, traffic_ids, duration_ms, created_at
FROM finding_reproductions WHERE finding_id=$1 ORDER BY id DESC LIMIT 1`, findingID).
		Scan(&r.ID, &r.FindingID, &r.ExplorationID, &r.State, &r.Reason, &r.StatusCode,
			&r.ResponseDigest, &r.BaselineDigest, &r.Matched, &r.EvidenceVer, &ids, &r.DurationMs, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ReproduceState{}, false, nil
	}
	if err != nil {
		return ReproduceState{}, false, err
	}
	_ = json.Unmarshal(ids, &r.TrafficIDs)
	return r, true, nil
}

// SyncFindingConfidence recomputes confidence from stored evidence alone. Called
// after an evidence-version bump (a new binding or a rewritten report) because a
// previously passed run no longer speaks for the new evidence.
func (d *DB) SyncFindingConfidence(ctx context.Context, findingID int64) error {
	var passed bool
	err := d.QueryRowContext(ctx, `SELECT EXISTS(
  SELECT 1 FROM finding_reproductions r
  JOIN findings f ON f.id=r.finding_id
  WHERE r.finding_id=$1 AND r.state='passed' AND r.evidence_version=f.evidence_version
)`, findingID).Scan(&passed)
	if err != nil {
		return err
	}
	state := ReproduceFailed
	if passed {
		state = ReproducePassed
	}
	conf := ConfidenceLikely
	if passed {
		conf = ConfidenceConfirmed
	}
	_, err = d.ExecContext(ctx, `UPDATE findings SET reproduced=$2, confidence=$3, reproduce_state=$4 WHERE id=$1`,
		findingID, passed, conf, state)
	return err
}
