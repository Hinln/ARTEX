package db

import "context"

// FindingState is what the report needs to know about a finding beyond the
// graph payload: whether the replay actually passed, or the model merely
// described a bug it thinks is there.
type FindingState string

const (
	ReportVerified FindingState = "verified" // 复现器重放证据并比对通过
	ReportFailed   FindingState = "failed"   // 复现跑了但没对上
	ReportPending  FindingState = "pending"  // 还没有复现结论
)

// FindingStates maps node_id -> state for the findings of this exploration.
func (s *ExplorationStore) FindingStates(ctx context.Context) (map[int64]FindingState, error) {
	out := map[int64]FindingState{}
	if s == nil || s.db == nil {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT f.node_id, COALESCE(f.reproduced,false), COALESCE(f.reproduce_state,''), COALESCE(f.confidence,'')
		FROM findings f JOIN exploration_nodes n ON n.id = f.node_id
		WHERE n.exploration_id = $1 AND f.node_id IS NOT NULL`, s.expID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var nodeID int64
		var reproduced bool
		var state, confidence string
		if err := rows.Scan(&nodeID, &reproduced, &state, &confidence); err != nil {
			return out, err
		}
		switch {
		case reproduced && state == "passed":
			out[nodeID] = ReportVerified
		case state == "failed" || state == "blocked":
			out[nodeID] = ReportFailed
		default:
			out[nodeID] = ReportPending
		}
	}
	return out, rows.Err()
}
