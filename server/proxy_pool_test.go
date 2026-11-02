package server

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/traffic"
)

func TestParseProxyBatchValidationAndDedup(t *testing.T) {
	entries, errs := ParseProxyBatch("http://user:pass@127.0.0.1:8080|one\nhttp://user:pass@127.0.0.1:8080\nftp://bad:1\n# comment\n")
	if len(entries) != 1 || len(errs) != 2 {
		t.Fatalf("entries=%d errs=%d", len(entries), len(errs))
	}
	if entries[0].URL == "" || !strings.Contains(entries[0].URL, "pass") {
		t.Fatalf("parser lost stored credential")
	}
}

func TestProxyPoolSnapshotMasksCredentialsAndDisabledHealth(t *testing.T) {
	m := &Manager{proxyPool: &proxyPoolState{entries: []proxyPoolEntry{{ProxyPoolEntry: ProxyPoolEntry{ID: "1", Enabled: false, Health: "healthy"}, RawURL: "http://user:secret@host:8080"}}, logs: []ProxySwitchLog{{From: "http://user:secret@host:8080", To: "http://user:secret2@host:8081", Reason: "timed"}}}}
	entries, _, logs := m.ProxyPoolSnapshot()
	if strings.Contains(entries[0].URL, "secret") || entries[0].Health != "disabled" {
		t.Fatalf("entry leaked or wrong state: %+v", entries[0])
	}
	if strings.Contains(logs[0].From, "secret") || strings.Contains(logs[0].To, "secret2") {
		t.Fatalf("switch log leaked credentials: %+v", logs[0])
	}
}

func TestProxyPoolEmptyResponseUsesArrays(t *testing.T) {
	for _, tc := range []struct {
		name string
		pool *proxyPoolState
	}{
		{name: "uninitialized"},
		{name: "empty", pool: &proxyPoolState{}},
		{name: "member without switches", pool: &proxyPoolState{entries: []proxyPoolEntry{{ProxyPoolEntry: ProxyPoolEntry{ID: "a"}, RawURL: "http://a"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{m: &Manager{proxyPool: tc.pool}}
			w := httptest.NewRecorder()
			s.getProxyPool(w, httptest.NewRequest(http.MethodGet, "/api/settings/proxy-pool", nil))
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"entries", "switch_log"} {
				if raw := strings.TrimSpace(string(payload[field])); !strings.HasPrefix(raw, "[") {
					t.Errorf("%s must be a JSON array, got %s", field, raw)
				}
			}
		})
	}
}

func TestProxyPoolSequentialSkipsUnhealthyAndDisabled(t *testing.T) {
	m := &Manager{proxyPool: &proxyPoolState{active: "a", entries: []proxyPoolEntry{
		{ProxyPoolEntry: ProxyPoolEntry{ID: "a", Enabled: true, Health: "healthy"}, RawURL: "http://a"},
		{ProxyPoolEntry: ProxyPoolEntry{ID: "b", Enabled: false, Health: "healthy"}, RawURL: "http://b"},
		{ProxyPoolEntry: ProxyPoolEntry{ID: "c", Enabled: true, Health: "unhealthy-cooling"}, RawURL: "http://c"},
		{ProxyPoolEntry: ProxyPoolEntry{ID: "d", Enabled: true, Health: "healthy"}, RawURL: "http://d"},
	}}}
	if got := m.selectHealthyLocked("a"); got != "d" {
		t.Fatalf("selected %q", got)
	}
}

func TestProxyPoolIntervalLowerBound(t *testing.T) {
	if minProxyRotateInterval >= 5 {
		t.Fatalf("bound must allow the documented 5s setting")
	}
	if minProxyRotateInterval != 3 {
		t.Fatalf("bound=%d, rationale expects 3s", minProxyRotateInterval)
	}
}

func TestProxyPoolPersistenceKeepsRawURL(t *testing.T) {
	b, err := json.Marshal(proxyPoolPersist{Entries: []proxyPoolEntry{{ProxyPoolEntry: ProxyPoolEntry{ID: "1", URL: "http://***@host:1"}, RawURL: "http://user:pass@host:1"}}})
	if err != nil || !strings.Contains(string(b), "raw_url") || !strings.Contains(string(b), "pass") {
		t.Fatalf("bad persistence encoding: %s", b)
	}
}

func TestProxyPoolURLKeyNormalizesHostCase(t *testing.T) {
	a, _ := traffic.ValidateProxyURL("http://user:pass@HOST:8080")
	b, _ := traffic.ValidateProxyURL("http://user:pass@host:8080")
	if proxyURLKey(a) != proxyURLKey(b) {
		t.Fatalf("keys differ: %q %q", proxyURLKey(a), proxyURLKey(b))
	}
}

func TestProxyPoolHealthThresholdCooldownAndRecovery(t *testing.T) {
	m := &Manager{proxyPool: &proxyPoolState{entries: []proxyPoolEntry{{ProxyPoolEntry: ProxyPoolEntry{ID: "a", Enabled: true, Health: "healthy"}, RawURL: "http://a"}}}}
	fail := egressProbeResult{Error: "http://user:pass@proxy: connection refused"}
	for i := 0; i < defaultProxyHealthThreshold-1; i++ {
		m.markProxyProbe("a", fail)
		if got := m.proxyPool.entries[0].Health; got != "healthy" {
			t.Fatalf("failure %d moved to %s before threshold", i+1, got)
		}
	}
	m.markProxyProbe("a", fail)
	if got := m.proxyPool.entries[0]; got.Health != "unhealthy-cooling" || got.ConsecutiveFailures != defaultProxyHealthThreshold {
		t.Fatalf("threshold state=%+v", got)
	}
	if got := m.proxyPool.entries[0].LastError; strings.Contains(got, "pass") || !strings.Contains(got, "***@") {
		t.Fatalf("failure was not masked: %q", got)
	}
	m.markProxyProbe("a", egressProbeResult{OK: true, ExitIP: "203.0.113.9"})
	if got := m.proxyPool.entries[0]; got.Health != "healthy" || got.ConsecutiveFailures != 0 || got.EgressIP != "203.0.113.9" {
		t.Fatalf("recovery state=%+v", got)
	}
}

func TestProxyPoolUncheckedMemberDoesNotPauseBeforeFailureThreshold(t *testing.T) {
	m := proxyTestManager(t)
	task, err := m.CreateTask("unchecked proxy", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()
	m.globalProxy = "http://a"
	m.proxyPool = &proxyPoolState{active: "a", entries: []proxyPoolEntry{{
		ProxyPoolEntry: ProxyPoolEntry{ID: "a", Enabled: true, Health: "unknown"}, RawURL: "http://a",
	}}}
	s := &Server{m: m, ctx: context.Background(), proxyPoolProbeSem: make(chan struct{}, proxyPoolMaxProbeConcurrency)}
	s.proxyProbe = func(context.Context, string, string, []string, time.Duration) egressProbeResult {
		return egressProbeResult{Error: "proxy down"}
	}
	s.proxyPoolTickLocked(true)
	if st := task.lifecycleSnapshot(); st.Paused || st.PauseReason == "paused_by_proxy" {
		t.Fatalf("unchecked member paused the task: %+v", st)
	}
	if got := m.proxyPool.entries[0].Health; got != "unknown" {
		t.Fatalf("single failure changed unchecked member to %q", got)
	}
}

func TestProxyPoolCoolingWindowSkipsProbeUntilExpiry(t *testing.T) {
	m := proxyTestManager(t)
	m.globalProxy = "http://a"
	m.proxyPool = &proxyPoolState{active: "a", entries: []proxyPoolEntry{{
		ProxyPoolEntry: ProxyPoolEntry{ID: "a", Enabled: true, Health: "unhealthy-cooling", ConsecutiveFailures: 3, LastCheckedAt: time.Now()}, RawURL: "http://a",
	}}}
	s := &Server{m: m, ctx: context.Background(), proxyPoolProbeSem: make(chan struct{}, proxyPoolMaxProbeConcurrency)}
	calls := 0
	s.proxyProbe = func(context.Context, string, string, []string, time.Duration) egressProbeResult {
		calls++
		return egressProbeResult{OK: true, ExitIP: "203.0.113.5"}
	}
	s.proxyPoolTickLocked(false)
	if calls != 0 {
		t.Fatalf("cooling member was probed before cooldown expiry: calls=%d", calls)
	}
	m.proxyPool.entries[0].LastCheckedAt = time.Now().Add(-2 * time.Minute)
	s.proxyPoolTickLocked(false)
	if calls != 1 || m.proxyPool.entries[0].Health != "healthy" {
		t.Fatalf("expired cooling member did not recover: calls=%d entry=%+v", calls, m.proxyPool.entries[0])
	}
}

func TestProxyPoolTimedRotationUsesAvailableMembersAndRecoversTasks(t *testing.T) {
	m := proxyTestManager(t)
	if err := m.SetProxyPoolSettings(true, minProxyRotateInterval, true, "sequential", 3, 60, 30); err != nil {
		t.Fatal(err)
	}
	m.globalProxy = "http://a"
	m.proxyPool = &proxyPoolState{active: "a", lastTimed: time.Now().Add(-time.Minute), lastHealth: time.Now(), entries: []proxyPoolEntry{
		{ProxyPoolEntry: ProxyPoolEntry{ID: "a", Enabled: true, Health: "healthy"}, RawURL: "http://a"},
		{ProxyPoolEntry: ProxyPoolEntry{ID: "b", Enabled: true, Health: "healthy"}, RawURL: "http://b"},
		{ProxyPoolEntry: ProxyPoolEntry{ID: "c", Enabled: true, Health: "unhealthy-cooling"}, RawURL: "http://c"},
	}}
	s := &Server{m: m, ctx: context.Background()}
	s.proxyPoolTickLocked(false)
	if got := m.GlobalProxy(); got != "http://b" {
		t.Fatalf("timed rotation selected %q", got)
	}
	if len(m.proxyPool.logs) == 0 || m.proxyPool.logs[len(m.proxyPool.logs)-1].Reason != "timed" {
		t.Fatalf("timed rotation was not logged: %+v", m.proxyPool.logs)
	}
}

func TestProxyPoolFailureSwitchRecoversOnlyEligibleTasks(t *testing.T) {
	m := proxyTestManager(t)
	if err := m.SetProxyPoolSettings(false, minProxyRotateInterval, true, "sequential", 3, 60, 30); err != nil {
		t.Fatal(err)
	}
	m.globalProxy = "http://a"
	m.proxyPool = &proxyPoolState{active: "a", entries: []proxyPoolEntry{
		{ProxyPoolEntry: ProxyPoolEntry{ID: "a", Enabled: true, Health: "healthy"}, RawURL: "http://a"},
		{ProxyPoolEntry: ProxyPoolEntry{ID: "b", Enabled: true, Health: "healthy"}, RawURL: "http://b"},
	}}
	eligible, err := m.CreateTask("eligible", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	exhausted, err := m.CreateTask("exhausted", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = m.DeleteTask(eligible.ID, DeleteTaskOptions{})
		_, _ = m.DeleteTask(exhausted.ID, DeleteTaskOptions{})
	}()
	for _, task := range []*Task{eligible, exhausted} {
		if err := m.ApplyProxyPause(task.ID); err != nil {
			t.Fatal(err)
		}
		task.updateLifecycle(func(v *taskLifecycleState) { v.ReconnectGeneration = 7; v.ReconnectMax = 3 })
	}
	exhausted.updateLifecycle(func(v *taskLifecycleState) { v.ReconnectAttempt, v.ReconnectExhausted = 3, true })
	s := newAdmissionTestServer(m, nil)
	m.SetTaskControlMutex(&s.concMu)
	s.proxyPoolWake = make(chan struct{}, 1)
	s.proxyPoolProbeSem = make(chan struct{}, proxyPoolMaxProbeConcurrency)
	s.proxyProbe = func(context.Context, string, string, []string, time.Duration) egressProbeResult {
		return egressProbeResult{Error: "proxy down"}
	}
	s.engine.Pause(eligible.ID, agent.AbortPausedByProxy)
	s.engine.Pause(exhausted.ID, agent.AbortPausedByProxy)
	if !s.handleProxyPoolFailure(errors.New("proxyconnect tcp: connection refused")) {
		t.Fatal("confirmed failure did not switch to the available member")
	}
	if got := m.GlobalProxy(); got != "http://b" {
		t.Fatalf("failure switch selected %q", got)
	}
	if st := eligible.lifecycleSnapshot(); st.PauseReason != "" || st.Paused {
		t.Fatalf("eligible task did not recover: %+v", st)
	}
	if st := exhausted.lifecycleSnapshot(); st.PauseReason != "paused_by_proxy" || !st.ReconnectExhausted {
		t.Fatalf("exhausted task was automatically resumed: %+v", st)
	}
}

func TestProxyPoolReconnectKeepsAttemptsAndDoesNotAutoResumeExhausted(t *testing.T) {
	m := proxyTestManager(t)
	if err := m.pg.SetSetting(settingProxyReconnectMax, "2"); err != nil {
		t.Fatal(err)
	}
	if err := m.pg.SetSetting(settingProxyReconnectInterval, "1"); err != nil {
		t.Fatal(err)
	}
	m.globalProxy = "http://legacy"
	m.proxyPool = &proxyPoolState{active: "legacy", entries: []proxyPoolEntry{{
		ProxyPoolEntry: ProxyPoolEntry{ID: "legacy", Enabled: true, Health: "healthy"}, RawURL: "http://legacy",
	}}}
	task, err := m.CreateTask("legacy reconnect", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()
	if err := m.ApplyProxyPause(task.ID); err != nil {
		t.Fatal(err)
	}
	task.updateLifecycle(func(v *taskLifecycleState) { v.ReconnectGeneration = 3 })
	s := &Server{m: m, ctx: context.Background(), proxyPoolProbeSem: make(chan struct{}, proxyPoolMaxProbeConcurrency)}
	s.proxyProbe = func(context.Context, string, string, []string, time.Duration) egressProbeResult {
		return egressProbeResult{Error: "proxy down"}
	}
	s.proxyReconnectTick()
	first := task.lifecycleSnapshot()
	if first.ReconnectAttempt != 1 || first.ReconnectExhausted {
		t.Fatalf("first pool reconnect attempt=%+v", first)
	}
	task.updateLifecycle(func(v *taskLifecycleState) { v.ReconnectNextAt = 0 })
	s.proxyReconnectTick()
	second := task.lifecycleSnapshot()
	if second.ReconnectAttempt != 2 || !second.ReconnectExhausted || second.PauseReason != "paused_by_proxy" {
		t.Fatalf("second pool reconnect did not exhaust: %+v", second)
	}
	s.proxyReconnectTick()
	third := task.lifecycleSnapshot()
	if third.ReconnectAttempt != second.ReconnectAttempt || !third.ReconnectExhausted || third.PauseReason != "paused_by_proxy" {
		t.Fatalf("automatic tick revived exhausted task: %+v", third)
	}
	guards := s.snapshotProxyReconnectGuardsWithExhausted(false, true)
	s.proxyReconnectSuccessWithGuards(guards, nil)
	if got := task.lifecycleSnapshot(); got.PauseReason != "" {
		t.Fatalf("manual guarded resume did not release exhausted task: %+v", got)
	}
}

func TestProxyPoolDisableOrClearActiveRoutesAway(t *testing.T) {
	newServer := func(entries []proxyPoolEntry) (*Server, *Manager) {
		m := &Manager{globalProxy: "http://a", proxyPool: &proxyPoolState{active: "a", entries: entries}}
		return &Server{m: m}, m
	}
	t.Run("disable switches to healthy member", func(t *testing.T) {
		s, m := newServer([]proxyPoolEntry{
			{ProxyPoolEntry: ProxyPoolEntry{ID: "a", Enabled: true, Health: "healthy"}, RawURL: "http://a"},
			{ProxyPoolEntry: ProxyPoolEntry{ID: "b", Enabled: true, Health: "healthy"}, RawURL: "http://b"},
		})
		r := httptest.NewRequest(http.MethodPatch, "/api/settings/proxy-pool/a", strings.NewReader(`{"enabled":false}`))
		r.SetPathValue("id", "a")
		w := httptest.NewRecorder()
		s.updateProxyPool(w, r)
		if w.Code != http.StatusOK || m.GlobalProxy() != "http://b" || m.proxyPoolActiveID() != "b" {
			t.Fatalf("disable route status=%d proxy=%q active=%q body=%s", w.Code, m.GlobalProxy(), m.proxyPoolActiveID(), w.Body.String())
		}
	})
	t.Run("clear last member clears runtime", func(t *testing.T) {
		s, m := newServer([]proxyPoolEntry{{
			ProxyPoolEntry: ProxyPoolEntry{ID: "a", Enabled: true, Health: "healthy"}, RawURL: "http://a",
		}})
		r := httptest.NewRequest(http.MethodPatch, "/api/settings/proxy-pool/a", strings.NewReader(`{"url":""}`))
		r.SetPathValue("id", "a")
		w := httptest.NewRecorder()
		s.updateProxyPool(w, r)
		if w.Code != http.StatusOK || m.GlobalProxy() != "" || m.proxyPoolActiveID() != "" {
			t.Fatalf("clear route status=%d proxy=%q active=%q body=%s", w.Code, m.GlobalProxy(), m.proxyPoolActiveID(), w.Body.String())
		}
	})
}

func TestProxyPoolClearOrDeleteLastMemberRecoversOnlyEligibleProxyPauses(t *testing.T) {
	run := func(t *testing.T, deleteLast bool) {
		m := proxyTestManager(t)
		m.globalProxy = "http://a"
		m.proxyPool = &proxyPoolState{active: "a", entries: []proxyPoolEntry{{
			ProxyPoolEntry: ProxyPoolEntry{ID: "a", Enabled: true, Health: "healthy"}, RawURL: "http://a",
		}}}
		eligible, err := m.CreateTask("eligible", "goal", nil, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		exhausted, err := m.CreateTask("exhausted", "goal", nil, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		userPaused, err := m.CreateTask("user paused", "goal", nil, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			_, _ = m.DeleteTask(eligible.ID, DeleteTaskOptions{})
			_, _ = m.DeleteTask(exhausted.ID, DeleteTaskOptions{})
			_, _ = m.DeleteTask(userPaused.ID, DeleteTaskOptions{})
		}()
		for _, task := range []*Task{eligible, exhausted} {
			if err := m.ApplyProxyPause(task.ID); err != nil {
				t.Fatal(err)
			}
			task.updateLifecycle(func(v *taskLifecycleState) { v.ReconnectGeneration = 7 })
		}
		exhausted.updateLifecycle(func(v *taskLifecycleState) {
			v.ReconnectAttempt, v.ReconnectExhausted = 3, true
		})
		if err := m.ApplyTaskPauseOrigin(userPaused.ID, false); err != nil {
			t.Fatal(err)
		}
		s := newAdmissionTestServer(m, nil)
		for _, task := range []*Task{eligible, exhausted, userPaused} {
			s.engine.Pause(task.ID, agent.AbortPausedByProxy)
		}
		if deleteLast {
			r := httptest.NewRequest(http.MethodDelete, "/api/settings/proxy-pool/a", nil)
			r.SetPathValue("id", "a")
			w := httptest.NewRecorder()
			s.deleteProxyPool(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("delete status=%d body=%s", w.Code, w.Body.String())
			}
		} else {
			r := httptest.NewRequest(http.MethodPatch, "/api/settings/proxy-pool/a", strings.NewReader(`{"url":""}`))
			r.SetPathValue("id", "a")
			w := httptest.NewRecorder()
			s.updateProxyPool(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("clear status=%d body=%s", w.Code, w.Body.String())
			}
		}
		if got := eligible.lifecycleSnapshot(); got.PauseReason != "" || got.Paused {
			t.Fatalf("eligible proxy pause was not recovered: %+v", got)
		}
		if got := exhausted.lifecycleSnapshot(); got.PauseReason != "paused_by_proxy" || !got.ReconnectExhausted {
			t.Fatalf("exhausted proxy pause was recovered: %+v", got)
		}
		if got := userPaused.lifecycleSnapshot(); got.PauseReason != "paused_by_user" {
			t.Fatalf("user pause was recovered: %+v", got)
		}
	}
	t.Run("clear", func(t *testing.T) { run(t, false) })
	t.Run("delete", func(t *testing.T) { run(t, true) })
}

func TestProxyPoolCaptureOffUsesLocalForwarder(t *testing.T) {
	fp, err := newDynamicForwardProxy(func() string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	defer fp.Close()
	m := &Manager{forwardProxy: fp}
	m.proxyPoolConfigured.Store(true)
	got := m.ProxyAddr()
	if !strings.HasPrefix(got, "http://127.0.0.1:") {
		t.Fatalf("capture-off proxy address=%q, want local forwarder", got)
	}
}

func TestProxyPoolClearedRuntimeForwarderFallsBackToDirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "direct-ok")
	}))
	defer target.Close()
	m := proxyTestManager(t)
	// A configured pool can remain persisted after its last usable member is
	// disabled or cleared. The runtime route is still direct in that state.
	m.proxyPoolConfigured.Store(true)
	proxyURL, err := url.Parse(m.ProxyAddr())
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	resp, err := client.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "direct-ok" {
		t.Fatalf("cleared runtime route status=%d body=%q", resp.StatusCode, body)
	}
}

func TestProxyReconnectGuardsGateExhaustedAutomaticResume(t *testing.T) {
	task := &Task{ID: "exhausted", PauseReason: "paused_by_proxy", ReconnectGeneration: 4, ReconnectAttempt: 3, ReconnectExhausted: true}
	m := &Manager{tasks: map[string]*Task{task.ID: task}}
	s := &Server{m: m}
	if got := s.snapshotProxyReconnectGuards(false); len(got) != 0 {
		t.Fatalf("automatic guards included exhausted task: %+v", got)
	}
	if got := s.snapshotProxyReconnectGuardsWithExhausted(false, true); len(got) != 1 {
		t.Fatalf("manual guards did not include exhausted task: %+v", got)
	}
}

func TestProxyPoolSettingsRejectTooShortInterval(t *testing.T) {
	m := &Manager{}
	if err := m.SetProxyPoolSettings(true, minProxyRotateInterval-1, true, "sequential", 3, 60, 30); err == nil {
		t.Fatal("interval below lower bound was accepted")
	}
}

func TestProxyPoolRandomSelectionStillExcludesDisabledAndCooling(t *testing.T) {
	m := proxyTestManager(t)
	if err := m.pg.SetSetting(settingProxyRotateStrategy, "random"); err != nil {
		t.Fatal(err)
	}
	m.proxyPool = &proxyPoolState{entries: []proxyPoolEntry{
		{ProxyPoolEntry: ProxyPoolEntry{ID: "disabled", Enabled: false, Health: "healthy"}, RawURL: "http://disabled"},
		{ProxyPoolEntry: ProxyPoolEntry{ID: "cooling", Enabled: true, Health: "unhealthy-cooling"}, RawURL: "http://cooling"},
		{ProxyPoolEntry: ProxyPoolEntry{ID: "empty", Enabled: true, Health: "healthy"}},
		{ProxyPoolEntry: ProxyPoolEntry{ID: "good", Enabled: true, Health: "healthy"}, RawURL: "http://good"},
	}}
	for i := 0; i < 20; i++ {
		if got := m.selectHealthyLocked(""); got != "good" {
			t.Fatalf("random selected %q", got)
		}
	}
}

func TestProxyPoolProbeSkipsEnabledEmptyURL(t *testing.T) {
	m := proxyTestManager(t)
	m.globalProxy = "http://good"
	m.proxyPool = &proxyPoolState{active: "good", entries: []proxyPoolEntry{
		{ProxyPoolEntry: ProxyPoolEntry{ID: "empty", Enabled: true, Health: "healthy"}},
		{ProxyPoolEntry: ProxyPoolEntry{ID: "good", Enabled: true, Health: "healthy"}, RawURL: "http://good"},
	}}
	s := &Server{m: m, ctx: context.Background(), proxyPoolProbeSem: make(chan struct{}, proxyPoolMaxProbeConcurrency)}
	calls := 0
	s.proxyProbe = func(context.Context, string, string, []string, time.Duration) egressProbeResult {
		calls++
		return egressProbeResult{OK: true}
	}
	s.proxyPoolTickLocked(true)
	if calls != 1 {
		t.Fatalf("probe calls=%d, want only non-empty enabled member", calls)
	}
}

func TestProxyPoolFailureMarksOriginalMemberAfterConcurrentSwitch(t *testing.T) {
	m := &Manager{globalProxy: "http://a", proxyPool: &proxyPoolState{active: "a", entries: []proxyPoolEntry{
		{ProxyPoolEntry: ProxyPoolEntry{ID: "a", Enabled: true, Health: "healthy"}, RawURL: "http://a"},
		{ProxyPoolEntry: ProxyPoolEntry{ID: "b", Enabled: true, Health: "healthy"}, RawURL: "http://b"},
	}}}
	s := &Server{m: m, ctx: context.Background(), proxyPoolWake: make(chan struct{}, 1)}
	s.proxyProbe = func(context.Context, string, string, []string, time.Duration) egressProbeResult {
		m.mu.Lock()
		m.globalProxy = "http://b"
		m.mu.Unlock()
		return egressProbeResult{Error: "proxy connection refused"}
	}
	if !s.handleProxyPoolFailure(fmt.Errorf("proxyconnect tcp: connection refused")) {
		t.Fatal("failure after concurrent switch was not consumed")
	}
	if got := m.proxyPool.entries[0].ConsecutiveFailures; got != 1 {
		t.Fatalf("original member failure count=%d", got)
	}
}

func TestProxyPoolAllMembersUnhealthyKeepsProxyConfigured(t *testing.T) {
	m := &Manager{globalProxy: "http://a", proxyPool: &proxyPoolState{active: "a", entries: []proxyPoolEntry{
		{ProxyPoolEntry: ProxyPoolEntry{ID: "a", Enabled: true, Health: "unhealthy-cooling", ConsecutiveFailures: defaultProxyHealthThreshold, LastCheckedAt: time.Now().Add(-2 * time.Minute)}, RawURL: "http://a"},
		{ProxyPoolEntry: ProxyPoolEntry{ID: "b", Enabled: true, Health: "unhealthy-cooling", ConsecutiveFailures: defaultProxyHealthThreshold, LastCheckedAt: time.Now().Add(-2 * time.Minute)}, RawURL: "http://b"},
	}}}
	s := &Server{m: m, ctx: context.Background(), proxyPoolProbeSem: make(chan struct{}, proxyPoolMaxProbeConcurrency)}
	s.proxyProbe = func(context.Context, string, string, []string, time.Duration) egressProbeResult {
		return egressProbeResult{Error: "proxy down"}
	}
	s.proxyPoolTickLocked(true)
	if got := m.GlobalProxy(); got != "http://a" {
		t.Fatalf("all unhealthy members fell back to direct: %q", got)
	}
	if got := m.proxyPoolActiveID(); got != "a" {
		t.Fatalf("active member changed without a healthy replacement: %q", got)
	}
}

func TestProxyPoolMaskedEditHotCutsCoolingActiveMember(t *testing.T) {
	m := &Manager{globalProxy: "http://old:secret@old.example:8080", proxyPool: &proxyPoolState{active: "a", entries: []proxyPoolEntry{{
		ProxyPoolEntry: ProxyPoolEntry{ID: "a", Enabled: true, Health: "unhealthy-cooling", ConsecutiveFailures: 3},
		RawURL:         "http://old:secret@old.example:8080",
	}}}}
	s := &Server{m: m}
	r := httptest.NewRequest(http.MethodPatch, "/api/settings/proxy-pool/a", strings.NewReader(`{"url":"http://***@new.example:9090"}`))
	r.SetPathValue("id", "a")
	w := httptest.NewRecorder()
	s.updateProxyPool(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if got := m.GlobalProxy(); got != "http://old:secret@new.example:9090" {
		t.Fatalf("active proxy=%q", got)
	}
	if got := m.proxyPool.entries[0].Health; got != "healthy" {
		t.Fatalf("edited active member health=%q", got)
	}
}

func TestProxyPoolDeleteNeverRetainsDeletedRuntimeURL(t *testing.T) {
	m := &Manager{globalProxy: "http://a", proxyPool: &proxyPoolState{active: "a", entries: []proxyPoolEntry{
		{ProxyPoolEntry: ProxyPoolEntry{ID: "a", Enabled: true, Health: "healthy"}, RawURL: "http://a"},
		{ProxyPoolEntry: ProxyPoolEntry{ID: "b", Enabled: true, Health: "healthy"}, RawURL: "http://b"},
	}}}
	s := &Server{m: m}
	deleteEntry := func(id string) {
		r := httptest.NewRequest(http.MethodDelete, "/api/settings/proxy-pool/"+id, nil)
		r.SetPathValue("id", id)
		w := httptest.NewRecorder()
		s.deleteProxyPool(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("delete %s status=%d body=%s", id, w.Code, w.Body.String())
		}
	}
	deleteEntry("a")
	if got := m.GlobalProxy(); got != "http://b" {
		t.Fatalf("surviving pool member was not activated: %q", got)
	}
	deleteEntry("b")
	if got := m.GlobalProxy(); got != "" || m.proxyPool.active != "" {
		t.Fatalf("deleted last member left runtime proxy=%q active=%q", got, m.proxyPool.active)
	}
}

func TestSetGlobalProxyIsReadOnlyWhenPoolManaged(t *testing.T) {
	m := &Manager{globalProxy: "http://pool.example:8080", proxyPool: &proxyPoolState{entries: []proxyPoolEntry{{
		ProxyPoolEntry: ProxyPoolEntry{ID: "pool", Enabled: true, Health: "healthy"}, RawURL: "http://pool.example:8080",
	}}}}
	if err := m.SetGlobalProxy("http://other.example:8080"); err != nil {
		t.Fatal(err)
	}
	if got := m.GlobalProxy(); got != "http://pool.example:8080" {
		t.Fatalf("managed pool runtime changed through legacy setting: %q", got)
	}
}

func TestProxyPoolEmptyPersistenceDoesNotResurrectLegacyProxy(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir, "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	m.proxyPool = &proxyPoolState{active: "a", entries: []proxyPoolEntry{{
		ProxyPoolEntry: ProxyPoolEntry{ID: "a", Enabled: true, Health: "healthy"}, RawURL: "http://deleted.example:8080",
	}}}
	m.globalProxy = "http://deleted.example:8080"
	if err := m.persistProxyPoolLocked(); err != nil {
		_ = m.Close()
		t.Fatal(err)
	}
	s := &Server{m: m}
	r := httptest.NewRequest(http.MethodDelete, "/api/settings/proxy-pool/a", nil)
	r.SetPathValue("id", "a")
	w := httptest.NewRecorder()
	s.deleteProxyPool(w, r)
	if w.Code != http.StatusOK {
		_ = m.Close()
		t.Fatalf("delete status=%d body=%s", w.Code, w.Body.String())
	}
	if got := m.GlobalProxy(); got != "" {
		t.Fatalf("delete left runtime proxy=%q", got)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	m2, err := NewManager(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	if got := m2.GlobalProxy(); got != "" || len(m2.proxyPoolEntries()) != 0 {
		t.Fatalf("empty persisted pool resurrected proxy=%q entries=%d", got, len(m2.proxyPoolEntries()))
	}
}

func TestMergeMaskedProxyURLPreservesOnlyPassword(t *testing.T) {
	got, err := mergeMaskedProxyURL("http://***@NEW.example:9090", "http://old:secret@old.example:8080")
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://old:secret@NEW.example:9090" {
		t.Fatalf("got %q", got)
	}
}

func TestMergeMaskedProxyURLErrorIsMasked(t *testing.T) {
	_, err := mergeMaskedProxyURL("http://user:secret@", "")
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unmasked error: %v", err)
	}
}

func TestDynamicForwardProxyUsesCurrentUpstream(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer target.Close()
	up := ""
	fp, err := newDynamicForwardProxy(func() string { return up })
	if err != nil {
		t.Fatal(err)
	}
	defer fp.Close()
	pu, _ := url.Parse(fp.Addr())
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	resp, err := c.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestDynamicForwardProxyConnectAuthenticatedUpstreamAndBufferedBytes(t *testing.T) {
	const user, pass = "alice", "s3cret"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "connect required", http.StatusMethodNotAllowed)
			return
		}
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
		if r.Header.Get("Proxy-Authorization") != want {
			w.Header().Set("Proxy-Authenticate", `Basic realm="test"`)
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		h, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijack", 500)
			return
		}
		c, rw, err := h.Hijack()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\nupstream-buffer")
		_ = rw.Flush()
	}))
	defer upstream.Close()
	up, _ := url.Parse(upstream.URL)
	up.User = url.UserPassword(user, pass)
	fp, err := newDynamicForwardProxy(func() string { return up.String() })
	if err != nil {
		t.Fatal(err)
	}
	defer fp.Close()

	c, err := net.Dial("tcp", strings.TrimPrefix(fp.Addr(), "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = fmt.Fprintf(c, "CONNECT example.test:443 HTTP/1.1\r\nHost: example.test:443\r\n\r\n")
	rd := bufio.NewReader(c)
	resp, err := http.ReadResponse(rd, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("local CONNECT status %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(rd, int64(len("upstream-buffer"))))
	if err != nil || string(b) != "upstream-buffer" {
		t.Fatalf("buffered bytes=%q err=%v", b, err)
	}
}

func TestDynamicForwardProxyConnectForwardsHijackBufferedClientBytes(t *testing.T) {
	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer targetLn.Close()
	go func() {
		c, err := targetLn.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, len("hello"))
		if _, err := io.ReadFull(c, buf); err == nil {
			_, _ = c.Write([]byte("target"))
		}
	}()
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "connect required", 405)
			return
		}
		c, err := net.Dial("tcp", targetLn.Addr().String())
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		h, _ := w.(http.Hijacker)
		client, rw, err := h.Hijack()
		if err != nil {
			_ = c.Close()
			return
		}
		defer client.Close()
		defer c.Close()
		_, _ = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = rw.Flush()
		buf := make([]byte, len("hello"))
		if _, err := io.ReadFull(rw.Reader, buf); err != nil {
			return
		}
		_, _ = c.Write(buf)
		response := make([]byte, len("target"))
		if _, err := io.ReadFull(c, response); err == nil {
			_, _ = client.Write(response)
		}
	}))
	defer proxyServer.Close()
	fp, err := newDynamicForwardProxy(func() string { return proxyServer.URL })
	if err != nil {
		t.Fatal(err)
	}
	defer fp.Close()
	c, err := net.Dial("tcp", strings.TrimPrefix(fp.Addr(), "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\nhello", targetLn.Addr().String(), targetLn.Addr().String())
	rd := bufio.NewReader(c)
	resp, err := http.ReadResponse(rd, &http.Request{Method: http.MethodConnect})
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT response=%v status=%v", err, resp)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	b, err := io.ReadAll(io.LimitReader(rd, int64(len("target"))))
	if err != nil || string(b) != "target" {
		t.Fatalf("target response=%q err=%v", b, err)
	}
}

func TestDialHTTPConnectBlackholeHonorsContext(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			defer c.Close()
			_, _ = io.Copy(io.Discard, c)
		}
	}()
	u, _ := url.Parse("http://" + ln.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = dialHTTPConnect(ctx, u, "example.test:443")
	if err == nil {
		t.Fatal("blackholed CONNECT unexpectedly succeeded")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("context cancellation took %s", elapsed)
	}
}
