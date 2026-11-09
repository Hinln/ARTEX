package db

import "context"

// HasPublicFoothold 回答「这个任务已经在公网资产上落地了吗」。
//
// 只要有一条结论挂在非保留地址上，就说明人已经进去了——这时内网就是战果，
// 该自动放行；一条都没有的时候，任何 RFC1918 目标都只是「我自己能连通」，
// 那正是把本机靶场当客户交出去的那次事故。
func (d *DB) HasPublicFoothold(ctx context.Context, expID int64) (bool, error) {
	if expID <= 0 {
		return false, nil
	}
	var ok bool
	err := d.QueryRowContext(ctx, `
SELECT EXISTS(
  SELECT 1
  FROM findings f
  JOIN exploration_nodes n ON n.id = f.node_id
  LEFT JOIN LATERAL jsonb_array_elements_text(COALESCE(f.asset_ids::jsonb, '[]'::jsonb)) AS t(e) ON true
  LEFT JOIN assets a ON a.id = (t.e)::bigint
  WHERE n.exploration_id = $1
    AND COALESCE(f.status, '') <> 'invalid_scope'
    AND (
      a.id IS NULL
      OR COALESCE(a.ip, '') = ''
      OR a.ip !~ '^(10\.|172\.(1[6-9]|2[0-9]|3[01])\.|192\.168\.|127\.|169\.254\.|100\.(6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7])\.|198\.18\.)'
    )
)`, expID).Scan(&ok)
	return ok, err
}

// AutoApprovePendingScope 把已落地任务里待审的私网范围转正。只在已有公网战果时调用。
func (d *DB) AutoApprovePendingScope(ctx context.Context, expID int64) (int64, error) {
	if expID <= 0 {
		return 0, nil
	}
	res, err := d.ExecContext(ctx, `
UPDATE task_scope ts SET source = 'agent'
FROM tasks t
WHERE t.id = ts.task_id
  AND t.exploration_id = $1
  AND COALESCE(ts.source, '') = 'pending'`, expID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
