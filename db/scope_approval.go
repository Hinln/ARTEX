package db

import (
	"context"
	"net"
	"strings"
)

// ScopeAllowsHost 回答「这个私网地址在本任务已批准的范围里吗」。
// 只有 source=agent / manual 的 ip、cidr 行算数；pending 是等人批准，rejected 无效。
func (d *DB) ScopeAllowsHost(ctx context.Context, expID int64, host string) (bool, error) {
	host = strings.TrimSpace(host)
	if expID <= 0 || host == "" {
		return false, nil
	}
	if ip := net.ParseIP(stripHostPort(host)); ip == nil {
		return false, nil
	}
	var ok bool
	err := d.QueryRowContext(ctx, `
SELECT EXISTS(
  SELECT 1
  FROM task_scope ts
  JOIN tasks t ON t.id = ts.task_id
  WHERE t.exploration_id = $1
    AND COALESCE(ts.source, '') IN ('agent', 'manual')
    AND ts.kind IN ('ip', 'cidr')
    AND ts.net >>= $2::inet
)`, expID, host).Scan(&ok)
	return ok, err
}

// ApproveTaskScope 把一条待审的范围行变成生效范围（pending → agent）。
func (d *DB) ApproveTaskScope(ctx context.Context, taskID, scopeID int64) (int64, error) {
	res, err := d.ExecContext(ctx, `UPDATE task_scope SET source='agent'
		WHERE id=$1 AND task_id=$2 AND COALESCE(source,'')='pending'`, scopeID, taskID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RejectTaskScope 把一条待审的范围行作废：数据留痕，但不再生效。
func (d *DB) RejectTaskScope(ctx context.Context, taskID, scopeID int64) (int64, error) {
	res, err := d.ExecContext(ctx, `UPDATE task_scope SET source='rejected'
		WHERE id=$1 AND task_id=$2 AND COALESCE(source,'')='pending'`, scopeID, taskID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PendingTaskScope 列出等人批准的范围行。
func (d *DB) PendingTaskScope(ctx context.Context, taskID int64) ([]TaskScope, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, task_id, kind, net::text, COALESCE(domain,''),
		COALESCE(value,''), COALESCE(source,''), COALESCE(reason,'') FROM task_scope
		WHERE task_id=$1 AND COALESCE(source,'')='pending' ORDER BY id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskScope
	for rows.Next() {
		var ts TaskScope
		if err := rows.Scan(&ts.ID, &ts.TaskID, &ts.Kind, &ts.Net, &ts.Domain, &ts.Value, &ts.Source, &ts.Reason); err != nil {
			return nil, err
		}
		out = append(out, ts)
	}
	return out, rows.Err()
}
