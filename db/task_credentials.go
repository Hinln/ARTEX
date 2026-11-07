package db

import (
	"context"
	"sort"
	"strings"
)

// TaskCredential is one credential recovered for a task, flattened out of the
// intelligence records so a tool can use it without knowing that storage shape.
type TaskCredential struct {
	Label    string `json:"label"`
	Username string `json:"username"`
	Password string `json:"password"`
	Token    string `json:"token"`
	Cookie   string `json:"cookie"`
	Endpoint string `json:"endpoint"`
	Protocol string `json:"protocol"`
	SourceID int64  `json:"source_id"`
}

// TaskCredentials returns every credential analysed for this exploration.
// Deduplicated on the credential itself: the same account is often extracted
// from a login request and again from a config dump.
func (s *ExplorationStore) TaskCredentials(ctx context.Context, limit int) ([]TaskCredential, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	var taskID int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM tasks WHERE exploration_id=$1 ORDER BY id DESC LIMIT 1`, s.expID).Scan(&taskID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	page, err := s.db.ListIntelligence(ctx, IntelligenceFilter{TaskID: taskID, Limit: 100})
	if err != nil {
		return nil, err
	}
	out := make([]TaskCredential, 0, limit)
	seen := map[string]bool{}
	for _, item := range page.Items {
		if item == nil || item.Credentials == nil {
			continue
		}
		for _, a := range item.Credentials.Accounts {
			key := strings.ToLower(strings.Join([]string{a.Username, a.Password, a.Token, a.Cookie, a.Endpoint}, "\x00"))
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, TaskCredential{
				Label: a.Label, Username: a.Username, Password: a.Password,
				Token: a.Token, Cookie: a.Cookie, Endpoint: a.Endpoint,
				Protocol: a.Protocol, SourceID: item.ID,
			})
			if len(out) >= limit {
				return out, nil
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Endpoint < out[j].Endpoint })
	return out, nil
}
