package server

// HTTP surface for reproduction: read the latest verdict, and re-run it on demand.
//
// Re-running matters because a finding's verdict ages: the target may have been
// fixed, or the first attempt may have hit a WAF. A reviewer needs to be able to
// say "verify this again now" without editing a single field.

import (
	"net/http"

	"github.com/Autumn-27/artex/db"
)

func (s *Server) registerFindingReproduce(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/exploration/findings/{id}/reproduce", s.getFindingReproduce)
	mux.HandleFunc("POST /api/exploration/findings/{id}/reproduce", s.runFindingReproduce)
}

// getFindingReproduce returns the latest attempt (or a "never attempted" shape).
func (s *Server) getFindingReproduce(w http.ResponseWriter, r *http.Request) {
	id := int64(atoiDefault(r.PathValue("id"), 0))
	if id <= 0 {
		writeErr(w, 400, "bad finding id")
		return
	}
	state, ok, err := s.m.pg.LatestReproduction(r.Context(), id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if !ok {
		writeJSON(w, 200, map[string]any{"finding_id": id, "attempted": false})
		return
	}
	writeJSON(w, 200, map[string]any{"finding_id": id, "attempted": true, "reproduce": state})
}

// runFindingReproduce re-sends the finding's proof traffic and records the result.
func (s *Server) runFindingReproduce(w http.ResponseWriter, r *http.Request) {
	id := int64(atoiDefault(r.PathValue("id"), 0))
	if id <= 0 {
		writeErr(w, 400, "bad finding id")
		return
	}
	f, err := s.m.pg.GetFinding(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if f == nil {
		writeErr(w, 404, "finding not found")
		return
	}
	repro := NewReproducer(s.evidenceStore(), s.m.pg, s.m.ProxyAddr(), s.m.ProxyCACert())
	res, err := repro.Reproduce(r.Context(), id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	_, recErr := s.m.pg.RecordReproduction(r.Context(), db.ReproduceState{
		FindingID:      id,
		State:          res.State,
		Reason:         res.Reason,
		StatusCode:     res.StatusCode,
		ResponseDigest: res.ResponseDigest,
		BaselineDigest: res.BaselineDigest,
		Matched:        res.Matched,
		EvidenceVer:    f.EvidenceVersion,
		TrafficIDs:     res.TrafficIDs,
		DurationMs:     int(res.Duration.Milliseconds()),
	})
	if recErr != nil {
		writeErr(w, 500, recErr.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"finding_id":  id,
		"state":       res.State,
		"reason":      res.Reason,
		"matched":     res.Matched,
		"status_code": res.StatusCode,
		"duration_ms": int(res.Duration.Milliseconds()),
	})
}
