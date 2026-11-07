package traffic

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// PruneBefore deletes captured exchanges older than cutoff, except the ids in
// keep — findings cite exchanges, and a cited exchange is evidence, not cache.
//
// Without this the store only grows: every scan, every fuzz batch and every
// polled page is kept forever, and the index plus blob buckets follow it.
func (t *Traffic) PruneBefore(ctx context.Context, cutoff time.Time, keep map[string]bool) (int64, error) {
	if t == nil || t.db == nil {
		return 0, nil
	}
	rows, err := t.db.QueryContext(ctx, `SELECT id FROM exchanges WHERE ts < ?`, cutoff.Unix())
	if err != nil {
		return 0, fmt.Errorf("挑选过期流量: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		if keep[id] {
			continue
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}

	var deleted int64
	const chunk = 300
	for start := 0; start < len(ids); start += chunk {
		end := start + chunk
		if end > len(ids) {
			end = len(ids)
		}
		part := ids[start:end]
		args := make([]any, len(part))
		marks := make([]string, len(part))
		for i, id := range part {
			args[i] = id
			marks[i] = "?"
		}
		tx, err := t.db.BeginTx(ctx, nil)
		if err != nil {
			return deleted, err
		}
		n, err := t.deleteWhere(tx, "id IN ("+strings.Join(marks, ",")+")", args...)
		if err != nil {
			tx.Rollback()
			return deleted, err
		}
		if err := tx.Commit(); err != nil {
			return deleted, err
		}
		deleted += n
	}
	// Bodies are content-addressed: drop the ones nobody references any more and
	// hand the freed pages back, otherwise the file never shrinks.
	_ = t.gcBlobs()
	t.reclaim()
	return deleted, nil
}
