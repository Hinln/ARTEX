package server

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

// 证据回收：抓包索引只增不减，长期跑的任务会把磁盘吃满。
//
// 规则只有一条——被漏洞引用的流量不动。判断依据是 PG 里的
// traffic_evidence_snapshots.source_traffic_id 与 finding_reproductions.traffic_ids，
// 所以「删掉之后报告还能复现」这件事始终成立。
func (s *Server) registerEvidenceMaintenance(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/evidence/prune", s.pruneEvidence)
	s.startEvidenceJanitor()
}

func (s *Server) pruneEvidence(w http.ResponseWriter, r *http.Request) {
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			days = n
		}
	}
	tr := s.m.Traffic()
	if tr == nil {
		writeErr(w, 409, "录制代理未启用，没有可回收的流量")
		return
	}
	keep, err := s.m.PG().ReferencedTrafficIDs(r.Context())
	if err != nil {
		writeErr(w, 500, "读取证据引用失败: "+err.Error())
		return
	}
	cutoff := time.Now().AddDate(0, 0, -days)
	deleted, err := tr.PruneBefore(r.Context(), cutoff, keep)
	if err != nil {
		writeErr(w, 500, "回收流量失败: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"deleted":   deleted,
		"kept":      len(keep),
		"older_than_days": days,
		"cutoff":    cutoff.Format(time.RFC3339),
	})
}

var evidenceJanitorOnce sync.Once

// startEvidenceJanitor 只在 ARTEX_EVIDENCE_TTL_DAYS>0 时启动（默认关，不擅自动别人的证据）。
func (s *Server) startEvidenceJanitor() {
	days, _ := strconv.Atoi(os.Getenv("ARTEX_EVIDENCE_TTL_DAYS"))
	if days <= 0 {
		return
	}
	evidenceJanitorOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(6 * time.Hour)
			defer ticker.Stop()
			for range ticker.C {
				tr := s.m.Traffic()
				if tr == nil {
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
				keep, err := s.m.PG().ReferencedTrafficIDs(ctx)
				if err == nil {
					_, _ = tr.PruneBefore(ctx, time.Now().AddDate(0, 0, -days), keep)
				}
				cancel()
			}
		}()
	})
}
