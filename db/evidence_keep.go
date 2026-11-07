package db

import "context"

// ReferencedTrafficIDs returns every exchange id that a finding still depends on.
//
// Deleting captured traffic is only safe for exchanges nobody cited: once a
// finding binds a snapshot, or a reproduction recorded what it replayed, that id
// is the difference between "here is the proof" and "trust me".
func (d *DB) ReferencedTrafficIDs(ctx context.Context) (map[string]bool, error) {
	out := map[string]bool{}
	rows, err := d.QueryContext(ctx, `
		SELECT DISTINCT source_traffic_id FROM traffic_evidence_snapshots WHERE COALESCE(source_traffic_id,'') <> ''
		UNION
		SELECT jsonb_array_elements_text(traffic_ids)
		  FROM finding_reproductions
		 WHERE jsonb_typeof(traffic_ids) = 'array'`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return out, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
