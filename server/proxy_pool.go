package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/traffic"
)

const (
	settingProxyPool             = "proxy_pool_entries"
	settingProxyPoolActive       = "proxy_pool_active_id"
	settingProxyRotateTimed      = "proxy_rotate_timed_enabled"
	settingProxyRotateInterval   = "proxy_rotate_interval_seconds"
	settingProxyRotateFailure    = "proxy_rotate_on_failure"
	settingProxyRotateStrategy   = "proxy_rotate_strategy"
	settingProxyHealthThreshold  = "proxy_health_fail_threshold"
	settingProxyHealthCooldown   = "proxy_health_cooldown_seconds"
	settingProxyHealthInterval   = "proxy_health_check_interval_seconds"
	defaultProxyRotateInterval   = 30
	minProxyRotateInterval       = 3
	defaultProxyHealthThreshold  = 3
	defaultProxyHealthCooldown   = 60
	defaultProxyHealthInterval   = 30
	proxyPoolProbeTimeout        = 10 * time.Second
	proxyPoolMaxProbeConcurrency = 4
)

// ProxyPoolEntry is the public shape. URL is always masked in API responses.
type ProxyPoolEntry struct {
	ID                  string    `json:"id"`
	URL                 string    `json:"url"`
	Name                string    `json:"name,omitempty"`
	Remark              string    `json:"remark,omitempty"`
	Enabled             bool      `json:"enabled"`
	EgressIP            string    `json:"egress_ip,omitempty"`
	LatencyMS           int64     `json:"latency_ms,omitempty"`
	LastError           string    `json:"last_error,omitempty"`
	LastCheckedAt       time.Time `json:"last_checked_at,omitempty"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	Health              string    `json:"health"`
}

type proxyPoolEntry struct {
	ProxyPoolEntry
	RawURL string `json:"raw_url"`
}

type ProxySwitchLog struct {
	At     time.Time `json:"at"`
	From   string    `json:"from"`
	To     string    `json:"to"`
	Reason string    `json:"reason"`
}

type proxyPoolPersist struct {
	Entries []proxyPoolEntry `json:"entries"`
	Active  string           `json:"active"`
}

type ProxyImportError struct {
	Line  int    `json:"line"`
	Input string `json:"input"`
	Error string `json:"error"`
}

// ParseProxyBatch accepts one proxy URL per line. Optional name and remark may
// follow as URL|name|remark. Duplicates are reported once and skipped.
func ParseProxyBatch(raw string) ([]ProxyPoolEntry, []ProxyImportError) {
	var out []ProxyPoolEntry
	var errs []ProxyImportError
	seen := map[string]bool{}
	for n, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		rawURL := strings.TrimSpace(parts[0])
		u, err := traffic.ValidateProxyURL(rawURL)
		if err != nil {
			errs = append(errs, ProxyImportError{Line: n + 1, Input: maskSecrets(line), Error: maskSecrets(err.Error())})
			continue
		}
		key := proxyURLKey(u)
		if seen[key] {
			errs = append(errs, ProxyImportError{Line: n + 1, Input: maskSecrets(line), Error: "重复代理，已跳过"})
			continue
		}
		seen[key] = true
		name, remark := "", ""
		if len(parts) > 1 {
			name = strings.TrimSpace(parts[1])
		}
		if len(parts) > 2 {
			remark = strings.TrimSpace(parts[2])
		}
		// New entries have not failed yet.  Keep them available until a probe
		// establishes a consecutive-failure streak.
		out = append(out, ProxyPoolEntry{ID: fmt.Sprintf("proxy-%d-%d", time.Now().UnixNano(), n+1), URL: rawURL, Name: name, Remark: remark, Enabled: true, Health: "unknown"})
	}
	return out, errs
}

func proxyURLKey(u *url.URL) string {
	if u == nil {
		return ""
	}
	v := *u
	v.Host = strings.ToLower(v.Host)
	v.Scheme = strings.ToLower(v.Scheme)
	return v.String()
}

func proxyURLKeyRaw(raw string) string {
	u, err := traffic.ValidateProxyURL(raw)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(raw))
	}
	return proxyURLKey(u)
}

type proxyPoolState struct {
	mu         sync.Mutex
	entries    []proxyPoolEntry
	active     string
	logs       []ProxySwitchLog
	lastTimed  time.Time
	lastHealth time.Time
}

type proxyPoolTickOutcome struct {
	confirmed bool
	switched  bool
}

func (m *Manager) initProxyPool() {
	m.proxyPool = &proxyPoolState{}
	persistedPool, poolSettingPresent, _ := m.pg.GetSetting(settingProxyPool)
	if poolSettingPresent && strings.TrimSpace(persistedPool) != "" {
		var p proxyPoolPersist
		if json.Unmarshal([]byte(persistedPool), &p) == nil {
			m.proxyPool.entries, m.proxyPool.active = p.Entries, p.Active
		}
	}
	if m.proxyPool.active == "" {
		if v, ok, _ := m.pg.GetSetting(settingProxyPoolActive); ok {
			m.proxyPool.active = strings.TrimSpace(v)
		}
	}
	if len(m.proxyPool.entries) == 0 && !poolSettingPresent && strings.TrimSpace(m.globalProxy) != "" {
		m.proxyPool.entries = []proxyPoolEntry{{ProxyPoolEntry: ProxyPoolEntry{ID: "proxy-legacy", URL: maskSecrets(m.globalProxy), Enabled: true, Health: "healthy"}, RawURL: m.globalProxy}}
		m.proxyPool.active = "proxy-legacy"
		_ = m.persistProxyPoolLocked()
	}
	if len(m.proxyPool.entries) == 0 && poolSettingPresent {
		m.globalProxy = ""
		m.proxyPool.active = ""
		_ = m.pg.SetSetting(settingGlobalProxy, "")
		_ = m.pg.SetSetting(settingProxyPoolActive, "")
	} else if len(m.proxyPool.entries) > 0 {
		for i := range m.proxyPool.entries {
			if strings.TrimSpace(m.proxyPool.entries[i].RawURL) != "" && m.proxyPool.entries[i].Health == "" {
				m.proxyPool.entries[i].Health = "unknown"
			}
		}
		chosen := ""
		for _, e := range m.proxyPool.entries {
			if e.ID == m.proxyPool.active && e.Enabled {
				chosen = e.ID
				break
			}
		}
		if chosen == "" {
			for _, e := range m.proxyPool.entries {
				if e.Enabled && e.RawURL != "" && e.Health != "unhealthy-cooling" {
					chosen = e.ID
					break
				}
			}
		}
		if chosen == "" {
			for _, e := range m.proxyPool.entries {
				if e.Enabled && e.RawURL != "" {
					chosen = e.ID
					break
				}
			}
		}
		if chosen == "" {
			m.globalProxy, m.proxyPool.active = "", ""
			_ = m.pg.SetSetting(settingGlobalProxy, "")
			_ = m.pg.SetSetting(settingProxyPoolActive, "")
		} else {
			m.proxyPool.active = chosen
			for _, e := range m.proxyPool.entries {
				if e.ID == chosen {
					m.globalProxy = e.RawURL
					break
				}
			}
			_ = m.pg.SetSetting(settingProxyPoolActive, chosen)
			_ = m.pg.SetSetting(settingGlobalProxy, m.globalProxy)
		}
	}
	m.proxyPoolConfigured.Store(len(m.proxyPool.entries) > 0)
}

func (m *Manager) persistProxyPoolLocked() error {
	if m.pg == nil {
		return nil
	}
	b, err := json.Marshal(proxyPoolPersist{Entries: m.proxyPool.entries, Active: m.proxyPool.active})
	if err != nil {
		return err
	}
	return m.pg.SetSetting(settingProxyPool, string(b))
}

func (m *Manager) ProxyPoolSnapshot() ([]ProxyPoolEntry, string, []ProxySwitchLog) {
	if m.proxyPool == nil {
		return []ProxyPoolEntry{}, "", []ProxySwitchLog{}
	}
	m.proxyPool.mu.Lock()
	defer m.proxyPool.mu.Unlock()
	entries := make([]ProxyPoolEntry, 0, len(m.proxyPool.entries))
	for _, e := range m.proxyPool.entries {
		p := e.ProxyPoolEntry
		p.URL = maskSecrets(e.RawURL)
		p.LastError = maskSecrets(p.LastError)
		if !p.Enabled {
			p.Health = "disabled"
		}
		entries = append(entries, p)
	}
	logs := append([]ProxySwitchLog{}, m.proxyPool.logs...)
	for i := range logs {
		logs[i].From, logs[i].To = maskSecrets(logs[i].From), maskSecrets(logs[i].To)
	}
	return entries, m.proxyPool.active, logs
}

func (m *Manager) ActiveProxy() string { return m.GlobalProxy() }

func (m *Manager) proxyPoolEntries() []proxyPoolEntry {
	if m.proxyPool == nil {
		return nil
	}
	m.proxyPool.mu.Lock()
	defer m.proxyPool.mu.Unlock()
	return append([]proxyPoolEntry(nil), m.proxyPool.entries...)
}

func (m *Manager) setProxyPoolEntries(entries []proxyPoolEntry, active string) error {
	if m.proxyPool == nil {
		m.proxyPool = &proxyPoolState{}
	}
	m.proxyPool.mu.Lock()
	defer m.proxyPool.mu.Unlock()
	m.proxyPool.entries, m.proxyPool.active = entries, active
	m.proxyPoolConfigured.Store(len(entries) > 0)
	if err := m.persistProxyPoolLocked(); err != nil {
		return err
	}
	if active == "" {
		for _, e := range entries {
			if e.Enabled && e.RawURL != "" && e.Health != "unhealthy-cooling" {
				return m.applyActiveProxyLocked(e.ID, "reconnect")
			}
		}
		for _, e := range entries {
			if e.Enabled && e.RawURL != "" {
				return m.applyActiveProxyLockedWithHealth(e.ID, "reconnect", true)
			}
		}
		if strings.TrimSpace(m.GlobalProxy()) != "" {
			return m.applyActiveProxyLocked("", "reconnect")
		}
		return nil
	}
	for _, e := range entries {
		if e.ID == active && e.Enabled && e.Health == "healthy" {
			return m.applyActiveProxyLocked(active, "manual")
		}
	}
	return nil
}

func (m *Manager) applyActiveProxyLocked(id, reason string) error {
	return m.applyActiveProxyLockedWithHealth(id, reason, false)
}

func (m *Manager) applyActiveProxyLockedWithHealth(id, reason string, allowUnhealthy bool) error {
	var next string
	for _, e := range m.proxyPool.entries {
		if e.ID == id && e.Enabled && e.RawURL != "" && (allowUnhealthy || e.Health != "unhealthy-cooling") {
			next = e.RawURL
			break
		}
	}
	if id == "" {
		next = ""
	}
	if id != "" && next == "" {
		return errors.New("代理未启用或不健康")
	}
	m.mu.RLock()
	from := m.globalProxy
	m.mu.RUnlock()
	if m.traffic != nil {
		if err := m.traffic.SetUpstreamProxy(next); err != nil {
			return err
		}
	}
	m.mu.Lock()
	m.globalProxy = next
	m.mu.Unlock()
	m.proxyPool.active = id
	if m.pg != nil {
		_ = m.pg.SetSetting(settingGlobalProxy, next)
		_ = m.pg.SetSetting(settingProxyPoolActive, id)
	}
	if from != next {
		m.proxyPool.logs = append(m.proxyPool.logs, ProxySwitchLog{At: time.Now(), From: maskSecrets(from), To: maskSecrets(next), Reason: reason})
		if len(m.proxyPool.logs) > 20 {
			m.proxyPool.logs = m.proxyPool.logs[len(m.proxyPool.logs)-20:]
		}
	}
	// Browser MCP reads the current capture/upstream route on each reconciliation.
	if m.pg != nil {
		m.syncBrowserMCPProxy()
	}
	return m.persistProxyPoolLocked()
}

func (m *Manager) selectHealthyLocked(exclude string) string {
	strategy := "sequential"
	if m.pg != nil {
		if v, ok, _ := m.pg.GetSetting(settingProxyRotateStrategy); ok && v == "random" {
			strategy = v
		}
	}
	pool := make([]string, 0)
	for _, e := range m.proxyPool.entries {
		if e.Enabled && e.RawURL != "" && e.Health != "unhealthy-cooling" && e.ID != exclude {
			pool = append(pool, e.ID)
		}
	}
	if len(pool) == 0 && exclude != "" {
		for _, e := range m.proxyPool.entries {
			if e.Enabled && e.RawURL != "" && e.Health != "unhealthy-cooling" {
				pool = append(pool, e.ID)
			}
		}
	}
	if len(pool) == 0 {
		return ""
	}
	if strategy == "random" {
		return pool[rand.Intn(len(pool))]
	}
	start := 0
	for i, e := range m.proxyPool.entries {
		if e.ID == exclude {
			start = i + 1
			break
		}
	}
	for n := 0; n < len(m.proxyPool.entries); n++ {
		e := m.proxyPool.entries[(start+n)%len(m.proxyPool.entries)]
		if e.Enabled && e.RawURL != "" && e.Health != "unhealthy-cooling" && e.ID != exclude {
			return e.ID
		}
	}
	return pool[0]
}

func (m *Manager) rotateProxy(reason string) bool {
	if m.proxyPool == nil {
		return false
	}
	m.proxyPool.mu.Lock()
	defer m.proxyPool.mu.Unlock()
	id := m.selectHealthyLocked(m.proxyPool.active)
	if id == "" || id == m.proxyPool.active {
		return false
	}
	return m.applyActiveProxyLocked(id, reason) == nil
}

func (m *Manager) markProxyProbe(id string, res egressProbeResult) {
	if m.proxyPool == nil {
		return
	}
	m.proxyPool.mu.Lock()
	defer m.proxyPool.mu.Unlock()
	m.markProxyProbeLocked(id, res)
}

func (m *Manager) markProxyProbeLocked(id string, res egressProbeResult) {
	for i := range m.proxyPool.entries {
		e := &m.proxyPool.entries[i]
		if e.ID != id {
			continue
		}
		e.LastCheckedAt, e.LatencyMS, e.EgressIP = time.Now(), res.LatencyMS, res.ExitIP
		if res.OK {
			e.ConsecutiveFailures, e.LastError, e.Health = 0, "", "healthy"
		} else {
			e.ConsecutiveFailures++
			e.LastError = maskSecrets(res.Error)
			threshold := defaultProxyHealthThreshold
			if m.pg != nil {
				threshold = proxyIntSetting(m.pg, settingProxyHealthThreshold, defaultProxyHealthThreshold)
			}
			if e.ConsecutiveFailures >= threshold {
				e.Health = "unhealthy-cooling"
			}
		}
		_ = m.persistProxyPoolLocked()
		return
	}
}

func proxyIntSetting(pg interface {
	GetSetting(string) (string, bool, error)
}, key string, def int) int {
	if pg == nil {
		return def
	}
	if v, ok, _ := pg.GetSetting(key); ok {
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func (m *Manager) proxyHealthConfig() (threshold int, cooldown, interval time.Duration) {
	if m.pg == nil {
		return defaultProxyHealthThreshold, defaultProxyHealthCooldown * time.Second, defaultProxyHealthInterval * time.Second
	}
	return proxyIntSetting(m.pg, settingProxyHealthThreshold, defaultProxyHealthThreshold), time.Duration(proxyIntSetting(m.pg, settingProxyHealthCooldown, defaultProxyHealthCooldown)) * time.Second, time.Duration(proxyIntSetting(m.pg, settingProxyHealthInterval, defaultProxyHealthInterval)) * time.Second
}

func (m *Manager) SetProxyPoolSettings(timed bool, interval int, onFailure bool, strategy string, threshold, cooldown, healthInterval int) error {
	if interval < minProxyRotateInterval {
		return fmt.Errorf("定时轮换间隔不能小于 %d 秒", minProxyRotateInterval)
	}
	if strategy != "sequential" && strategy != "random" {
		return errors.New("proxy_rotate_strategy 必须是 sequential 或 random")
	}
	if threshold < 1 || cooldown < 1 || healthInterval < 1 {
		return errors.New("健康检查参数必须大于 0")
	}
	return m.pg.SetSettingsAtomic(context.Background(), map[string]string{settingProxyRotateTimed: fmt.Sprint(timed), settingProxyRotateInterval: fmt.Sprint(interval), settingProxyRotateFailure: fmt.Sprint(onFailure), settingProxyRotateStrategy: strategy, settingProxyHealthThreshold: fmt.Sprint(threshold), settingProxyHealthCooldown: fmt.Sprint(cooldown), settingProxyHealthInterval: fmt.Sprint(healthInterval)})
}

func (m *Manager) proxyRotateConfig() (bool, time.Duration, bool, string) {
	if m.pg == nil {
		return false, defaultProxyRotateInterval * time.Second, false, "sequential"
	}
	return m.pg.GetBool(settingProxyRotateTimed, false), time.Duration(proxyIntSetting(m.pg, settingProxyRotateInterval, defaultProxyRotateInterval)) * time.Second, m.pg.GetBool(settingProxyRotateFailure, false), func() string {
		v, ok, _ := m.pg.GetSetting(settingProxyRotateStrategy)
		if ok && v == "random" {
			return v
		}
		return "sequential"
	}()
}

func (s *Server) runProxyPoolSupervisor(ctx context.Context) {
	for {
		_, rotateInterval, _, _ := s.m.proxyRotateConfig()
		_, _, healthInterval := s.m.proxyHealthConfig()
		if rotateInterval <= 0 {
			rotateInterval = defaultProxyRotateInterval * time.Second
		}
		if healthInterval <= 0 {
			healthInterval = defaultProxyHealthInterval * time.Second
		}
		d := healthInterval
		if timed, _, _, _ := s.m.proxyRotateConfig(); timed && rotateInterval < d {
			d = rotateInterval
		}
		if d <= 0 {
			d = time.Second
		}
		t := time.NewTimer(d)
		select {
		case <-ctx.Done():
			if !t.Stop() {
				select {
				case <-t.C:
				default:
				}
			}
			return
		case <-t.C:
			s.proxyPoolTick()
		case <-s.proxyPoolWake:
			if !t.Stop() {
				select {
				case <-t.C:
				default:
				}
			}
			s.proxyPoolTick()
		}
	}
}

func (s *Server) proxyPoolTick() proxyPoolTickOutcome {
	if s == nil {
		return proxyPoolTickOutcome{}
	}
	s.proxyPoolTickMu.Lock()
	defer s.proxyPoolTickMu.Unlock()
	return s.proxyPoolTickLocked(false)
}

func (s *Server) proxyPoolTickLocked(forceHealth bool) proxyPoolTickOutcome {
	var outcome proxyPoolTickOutcome
	if s.m == nil || s.m.proxyPool == nil {
		return outcome
	}
	now := time.Now()
	threshold, cooldown, healthInterval := s.m.proxyHealthConfig()
	s.m.proxyPool.mu.Lock()
	healthDue := s.m.proxyPool.lastHealth.IsZero() || now.Sub(s.m.proxyPool.lastHealth) >= healthInterval
	if !healthDue {
		// A cooling member becomes probe-eligible at its own cooldown deadline;
		// do not make it wait for the next full health-check interval.
		for _, e := range s.m.proxyPool.entries {
			if e.Enabled && strings.TrimSpace(e.RawURL) != "" && e.Health == "unhealthy-cooling" && (e.LastCheckedAt.IsZero() || now.Sub(e.LastCheckedAt) >= cooldown) {
				healthDue = true
				break
			}
		}
	}
	if healthDue {
		s.m.proxyPool.lastHealth = now
	}
	s.m.proxyPool.mu.Unlock()
	if healthDue || forceHealth {
		entries := s.m.proxyPoolEntries()
		sem := s.proxyPoolProbeSem
		if sem == nil {
			sem = make(chan struct{}, proxyPoolMaxProbeConcurrency)
		}
		activeAtProbe := s.m.proxyPoolActiveID()
		probeResults := make(map[string]bool, len(entries))
		var probeMu sync.Mutex
		var wg sync.WaitGroup
		for _, e := range entries {
			if !e.Enabled || strings.TrimSpace(e.RawURL) == "" {
				continue
			}
			if !forceHealth && e.Health == "unhealthy-cooling" && e.ConsecutiveFailures >= threshold && !e.LastCheckedAt.IsZero() && now.Sub(e.LastCheckedAt) < cooldown {
				continue
			}
			e := e
			wg.Add(1)
			go func() {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-serverContext(s).Done():
					return
				}
				defer func() { <-sem }()
				res := s.runProxyProbe(serverContext(s), e.RawURL, "", s.m.egressProbes(), proxyPoolProbeTimeout)
				s.m.markProxyProbe(e.ID, res)
				probeMu.Lock()
				probeResults[e.ID] = res.OK
				probeMu.Unlock()
			}()
		}
		wg.Wait()
		probeMu.Lock()
		confirmed := probeResults[activeAtProbe]
		probeMu.Unlock()
		if confirmed {
			outcome.confirmed = true
		}
		if s.reconcileProxyPoolAfterProbe(confirmed) {
			outcome.switched = true
		}
	}

	// Health reconciliation runs before timed rotation. This matters when an
	// import starts a fresh probe round: tasks are not paused or rerouted until
	// the new members have been checked. A successful recovery switch already
	// selected the best available member for this round, so avoid immediately
	// rotating away from it.
	timed, rotateInterval, _, _ := s.m.proxyRotateConfig()
	if timed && !outcome.switched {
		s.m.proxyPool.mu.Lock()
		due := s.m.proxyPool.lastTimed.IsZero() || now.Sub(s.m.proxyPool.lastTimed) >= rotateInterval
		if due {
			s.m.proxyPool.lastTimed = now
		}
		s.m.proxyPool.mu.Unlock()
		if due {
			before := s.m.proxyPoolActiveID()
			if s.m.rotateProxy("timed") && before != s.m.proxyPoolActiveID() {
				outcome.switched = true
				s.proxyReconnectSuccess()
			}
		}
	}
	return outcome
}

func serverContext(s *Server) context.Context {
	if s != nil && s.ctx != nil {
		return s.ctx
	}
	return context.Background()
}

func (s *Server) reconcileProxyPoolAfterProbe(activeConfirmed bool) bool {
	entries := s.m.proxyPoolEntries()
	activeID := s.m.proxyPoolActiveID()
	activeAvailable := false
	for _, e := range entries {
		if e.ID == activeID && e.Enabled && e.RawURL != "" && e.Health != "unhealthy-cooling" {
			activeAvailable = true
			break
		}
	}
	if activeAvailable && activeConfirmed {
		s.proxyReconnectSuccess()
		return false
	}
	if activeAvailable {
		return false
	}
	s.m.proxyPool.mu.Lock()
	id := s.m.selectHealthyLocked(activeID)
	var err error
	if id != "" {
		err = s.m.applyActiveProxyLocked(id, "reconnect")
	}
	s.m.proxyPool.mu.Unlock()
	if err == nil && id != "" {
		s.proxyReconnectSuccess()
		return true
	}
	allCooling := false
	enabled := 0
	for _, e := range entries {
		if e.Enabled && e.RawURL != "" {
			enabled++
			if e.Health == "unhealthy-cooling" {
				allCooling = true
			} else {
				allCooling = false
				break
			}
		}
	}
	if id == "" && enabled > 0 && allCooling && (activeID != "" || strings.TrimSpace(s.m.GlobalProxy()) != "") {
		s.pauseAllProxyTasks(errors.New("代理池暂无健康成员"))
	}
	return false
}

func (m *Manager) activateProxyID(id, reason string) error {
	if m.proxyPool == nil {
		return errors.New("代理池未初始化")
	}
	m.proxyPool.mu.Lock()
	defer m.proxyPool.mu.Unlock()
	if err := m.applyActiveProxyLocked(id, reason); err != nil {
		return err
	}
	return nil
}

func (s *Server) handleProxyPoolFailure(cause error) bool {
	if s == nil || s.m == nil || s.m.proxyPool == nil || !isGlobalProxyDialFailure(cause) {
		return false
	}
	cur := strings.TrimSpace(s.m.GlobalProxy())
	if cur == "" {
		return false
	}
	res := s.runProxyProbe(serverContext(s), cur, "", s.m.egressProbes(), proxyPoolProbeTimeout)
	if res.OK {
		return false
	}
	_, _, onFailure, _ := s.m.proxyRotateConfig()
	s.proxyPoolTickMu.Lock()
	s.m.proxyPool.mu.Lock()
	active := s.m.proxyPool.active
	currentChanged := s.m.GlobalProxy() != cur
	for _, e := range s.m.proxyPool.entries {
		if e.RawURL == cur {
			s.m.markProxyProbeLocked(e.ID, res)
			break
		}
	}
	if currentChanged {
		s.m.proxyPool.mu.Unlock()
		s.proxyPoolTickMu.Unlock()
		return true
	}
	if onFailure {
		if id := s.m.selectHealthyLocked(active); id != "" && id != active {
			if err := s.m.applyActiveProxyLocked(id, "failure"); err == nil {
				s.m.proxyPool.mu.Unlock()
				s.proxyPoolTickMu.Unlock()
				s.proxyReconnectSuccess()
				return true
			}
		}
	}
	select {
	case s.proxyPoolWake <- struct{}{}:
	default:
	}
	s.m.proxyPool.mu.Unlock()
	s.proxyPoolTickMu.Unlock()
	return false
}

func (s *Server) pauseAllProxyTasks(cause error) {
	for _, t := range s.m.List() {
		st := t.lifecycleSnapshot()
		if isTerminalStatus(st.Status) || st.Paused || (s.engine != nil && s.engine.IsDeleting(t.ID)) {
			continue
		}
		s.pauseProxyTask(t.ID, cause)
	}
}

func (s *Server) getProxyPool(w http.ResponseWriter, r *http.Request) {
	entries, active, logs := s.m.ProxyPoolSnapshot()
	timed, interval, onFailure, strategy := s.m.proxyRotateConfig()
	threshold, cooldown, healthInterval := s.m.proxyHealthConfig()
	writeJSON(w, 200, map[string]any{"entries": entries, "active_id": active, "active_proxy": maskSecrets(s.m.GlobalProxy()), "switch_log": logs, "timed_enabled": timed, "interval_seconds": int(interval / time.Second), "on_failure": onFailure, "strategy": strategy, "health_fail_threshold": threshold, "health_cooldown_seconds": int(cooldown / time.Second), "health_check_interval_seconds": int(healthInterval / time.Second)})
}

func (s *Server) importProxyPool(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text           string  `json:"text"`
		TimedEnabled   *bool   `json:"timed_enabled"`
		Interval       *int    `json:"interval_seconds"`
		OnFailure      *bool   `json:"on_failure"`
		Strategy       *string `json:"strategy"`
		Threshold      *int    `json:"health_fail_threshold"`
		Cooldown       *int    `json:"health_cooldown_seconds"`
		HealthInterval *int    `json:"health_check_interval_seconds"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	parsed, errs := ParseProxyBatch(req.Text)
	entries := s.m.proxyPoolEntries()
	seen := map[string]bool{}
	for _, e := range entries {
		seen[proxyURLKeyRaw(e.RawURL)] = true
	}
	for _, p := range parsed {
		key := proxyURLKeyRaw(p.URL)
		if seen[key] {
			line := 0
			if i := strings.LastIndex(p.ID, "-"); i >= 0 {
				_, _ = fmt.Sscanf(p.ID[i+1:], "%d", &line)
			}
			errs = append(errs, ProxyImportError{Line: line, Input: maskSecrets(p.URL), Error: "重复代理，已跳过"})
			continue
		}
		u, _ := traffic.ValidateProxyURL(p.URL)
		entries = append(entries, proxyPoolEntry{ProxyPoolEntry: p, RawURL: u.String()})
		seen[proxyURLKey(u)] = true
	}
	active := s.m.proxyPoolActiveID()
	if active != "" {
		foundActive := false
		for _, e := range entries {
			if e.ID == active {
				foundActive = true
				break
			}
		}
		if !foundActive {
			active = ""
		}
	}
	if err := s.m.setProxyPoolEntries(entries, active); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if req.Interval != nil || req.TimedEnabled != nil || req.OnFailure != nil || req.Strategy != nil || req.Threshold != nil || req.Cooldown != nil || req.HealthInterval != nil {
		timed, interval, onFailure, strategy := s.m.proxyRotateConfig()
		threshold, cooldown, healthInterval := s.m.proxyHealthConfig()
		if req.TimedEnabled != nil {
			timed = *req.TimedEnabled
		}
		if req.Interval != nil {
			interval = time.Duration(*req.Interval) * time.Second
		}
		if req.OnFailure != nil {
			onFailure = *req.OnFailure
		}
		if req.Strategy != nil {
			strategy = *req.Strategy
		}
		if req.Threshold != nil {
			threshold = *req.Threshold
		}
		if req.Cooldown != nil {
			cooldown = time.Duration(*req.Cooldown) * time.Second
		}
		if req.HealthInterval != nil {
			healthInterval = time.Duration(*req.HealthInterval) * time.Second
		}
		if err := s.m.SetProxyPoolSettings(timed, int(interval/time.Second), onFailure, strategy, threshold, int(cooldown/time.Second), int(healthInterval/time.Second)); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	}
	// Probe imported/unchecked entries before any pool-wide pause decision. The
	// task state is untouched until this round has completed.
	s.proxyPoolTick()
	entriesOut, activeOut, logs := s.m.ProxyPoolSnapshot()
	writeJSON(w, 200, map[string]any{"entries": entriesOut, "active_id": activeOut, "switch_log": logs, "errors": errs})
}

func (m *Manager) proxyPoolActiveID() string {
	if m.proxyPool == nil {
		return ""
	}
	m.proxyPool.mu.Lock()
	defer m.proxyPool.mu.Unlock()
	return m.proxyPool.active
}

func mergeMaskedProxyURL(raw, old string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	nu, err := traffic.ValidateProxyURL(raw)
	if err != nil {
		return "", fmt.Errorf("%s", maskSecrets(err.Error()))
	}
	if ou, _ := traffic.ValidateProxyURL(old); ou != nil && nu.User != nil && ou.User != nil {
		newUser := nu.User.Username()
		newPW, hasNewPW := nu.User.Password()
		maskedUser := newUser == "***" || newUser == "******"
		maskedPW := hasNewPW && (strings.Contains(newPW, "***") || strings.Contains(newPW, "******"))
		if maskedUser {
			oldPW, hasOldPW := ou.User.Password()
			if hasOldPW {
				nu.User = url.UserPassword(ou.User.Username(), oldPW)
			} else {
				nu.User = url.User(ou.User.Username())
			}
		} else if maskedPW {
			if oldPW, hasOldPW := ou.User.Password(); hasOldPW {
				nu.User = url.UserPassword(newUser, oldPW)
			} else {
				nu.User = url.User(newUser)
			}
		}
	}
	return nu.String(), nil
}

func (s *Server) updateProxyPool(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		URL     *string `json:"url"`
		Name    *string `json:"name"`
		Remark  *string `json:"remark"`
		Enabled *bool   `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if s.m.proxyPool == nil {
		writeErr(w, 404, "代理池为空")
		return
	}
	needPause := false
	switched := false
	routed := false
	recoverPaused := false
	proxyGuards := s.snapshotProxyReconnectGuards(false)
	s.m.proxyPool.mu.Lock()
	for i := range s.m.proxyPool.entries {
		e := &s.m.proxyPool.entries[i]
		if e.ID != id {
			continue
		}
		oldEntry := *e
		oldRaw := e.RawURL
		if req.URL != nil {
			raw, err := mergeMaskedProxyURL(*req.URL, oldRaw)
			if err != nil {
				s.m.proxyPool.mu.Unlock()
				writeErr(w, 400, err.Error())
				return
			}
			e.RawURL, e.URL = raw, maskSecrets(raw)
			if e.ID == s.m.proxyPool.active && e.Enabled && raw != "" {
				// A validated edit replaces the endpoint and starts a fresh health round,
				// including when the old endpoint was cooling down.
				e.Health, e.ConsecutiveFailures, e.LastError = "healthy", 0, ""
			}
		}
		if req.Name != nil {
			e.Name = strings.TrimSpace(*req.Name)
		}
		if req.Remark != nil {
			e.Remark = strings.TrimSpace(*req.Remark)
		}
		if req.Enabled != nil {
			e.Enabled = *req.Enabled
			if !e.Enabled {
				e.Health = "disabled"
			} else if e.Health == "disabled" {
				e.Health = "healthy"
			}
		}
		if s.m.proxyPool.active == id {
			if req.URL != nil || req.Enabled != nil {
				if e.Enabled && e.RawURL != "" {
					if err := s.m.applyActiveProxyLockedWithHealth(id, "manual", req.URL != nil); err != nil {
						*e = oldEntry
						s.m.proxyPool.mu.Unlock()
						writeErr(w, 400, maskSecrets(err.Error()))
						return
					}
					routed = true
				} else if next := s.m.selectHealthyLocked(id); next != "" {
					for _, candidate := range s.m.proxyPool.entries {
						if candidate.ID == next && candidate.RawURL == "" {
							next = ""
							break
						}
					}
					if next == "" {
						// Fall through to the enabled-member fallback below.
					} else {
						if err := s.m.applyActiveProxyLocked(next, "manual"); err != nil {
							s.m.proxyPool.mu.Unlock()
							writeErr(w, 400, maskSecrets(err.Error()))
							return
						}
						switched = true
						routed = true
					}
				}
				if !routed {
					// A disabled or cleared active member must never remain the
					// runtime route. Keep traffic on any surviving enabled member,
					// including a cooling one, until the normal reconnect loop can
					// establish a healthy route.
					next := ""
					for _, candidate := range s.m.proxyPool.entries {
						if candidate.ID != id && candidate.Enabled && candidate.RawURL != "" {
							next = candidate.ID
							break
						}
					}
					if next != "" {
						if err := s.m.applyActiveProxyLockedWithHealth(next, "manual", true); err != nil {
							s.m.proxyPool.mu.Unlock()
							writeErr(w, 400, maskSecrets(err.Error()))
							return
						}
						needPause = true
					} else {
						if err := s.m.applyActiveProxyLocked("", "manual"); err != nil {
							s.m.proxyPool.mu.Unlock()
							writeErr(w, 400, maskSecrets(err.Error()))
							return
						}
						needPause = false
						recoverPaused = true
					}
				}
			}
		}
		if err := s.m.persistProxyPoolLocked(); err != nil {
			s.m.proxyPool.mu.Unlock()
			writeErr(w, 500, err.Error())
			return
		}
		out := e.ProxyPoolEntry
		out.URL = maskSecrets(e.RawURL)
		if !out.Enabled {
			out.Health = "disabled"
		}
		s.m.proxyPool.mu.Unlock()
		if needPause {
			s.pauseAllProxyTasks(errors.New("代理池暂无健康成员"))
		}
		if switched {
			s.proxyReconnectSuccess()
		}
		if recoverPaused {
			s.proxyReconnectSuccessWithGuards(proxyGuards, nil)
		}
		writeJSON(w, 200, map[string]any{"entry": out})
		return
	}
	s.m.proxyPool.mu.Unlock()
	writeErr(w, 404, "代理不存在")
}

func (s *Server) deleteProxyPool(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.m.proxyPool == nil {
		writeErr(w, 404, "代理不存在")
		return
	}
	needPause := false
	switched := false
	recoverPaused := false
	proxyGuards := s.snapshotProxyReconnectGuards(false)
	s.m.proxyPool.mu.Lock()
	found := false
	out := s.m.proxyPool.entries[:0]
	for _, e := range s.m.proxyPool.entries {
		if e.ID == id {
			found = true
			continue
		}
		out = append(out, e)
	}
	if !found {
		s.m.proxyPool.mu.Unlock()
		writeErr(w, 404, "代理不存在")
		return
	}
	s.m.proxyPool.entries = out
	s.m.proxyPoolConfigured.Store(len(out) > 0)
	if s.m.proxyPool.active == id {
		if next := s.m.selectHealthyLocked(id); next != "" {
			if err := s.m.applyActiveProxyLocked(next, "manual"); err != nil {
				s.m.proxyPool.mu.Unlock()
				writeErr(w, 500, maskSecrets(err.Error()))
				return
			}
			switched = true
		} else {
			// Keep runtime traffic attached to a surviving pool member even when
			// every remaining member is cooling down; never retain the deleted URL.
			next := ""
			for _, e := range s.m.proxyPool.entries {
				if e.Enabled && e.RawURL != "" {
					next = e.ID
					break
				}
			}
			if next != "" {
				if err := s.m.applyActiveProxyLockedWithHealth(next, "manual", true); err != nil {
					s.m.proxyPool.mu.Unlock()
					writeErr(w, 500, maskSecrets(err.Error()))
					return
				}
				needPause = true
			} else {
				if err := s.m.applyActiveProxyLocked("", "manual"); err != nil {
					s.m.proxyPool.mu.Unlock()
					writeErr(w, 500, maskSecrets(err.Error()))
					return
				}
				// Clearing the last usable route leaves no proxy to recover.
				// Only an enabled member that is actually cooling warrants the
				// pool-wide proxy pause barrier.
				for _, candidate := range s.m.proxyPool.entries {
					if candidate.Enabled && candidate.RawURL != "" && candidate.Health == "unhealthy-cooling" {
						needPause = true
						break
					}
				}
				if !needPause {
					recoverPaused = true
				}
			}
		}
	}
	if err := s.m.persistProxyPoolLocked(); err != nil {
		s.m.proxyPool.mu.Unlock()
		writeErr(w, 500, err.Error())
		return
	}
	s.m.proxyPool.mu.Unlock()
	if needPause {
		s.pauseAllProxyTasks(errors.New("代理池暂无健康成员"))
	}
	if switched {
		s.proxyReconnectSuccess()
	}
	if recoverPaused {
		s.proxyReconnectSuccessWithGuards(proxyGuards, nil)
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) activateProxyPool(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	before := s.m.proxyPoolActiveID()
	if err := s.m.activateProxyID(id, "manual"); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if before != id {
		s.proxyReconnectSuccess()
	}
	writeJSON(w, 200, map[string]any{"ok": true, "active_id": id, "active_proxy": maskSecrets(s.m.GlobalProxy())})
}

func (s *Server) testProxyPoolEntry(w http.ResponseWriter, r *http.Request) {
	s.proxyPoolTickMu.Lock()
	defer s.proxyPoolTickMu.Unlock()
	id := r.PathValue("id")
	for _, e := range s.m.proxyPoolEntries() {
		if e.ID == id && e.Enabled && strings.TrimSpace(e.RawURL) != "" {
			res := s.runProxyProbe(r.Context(), e.RawURL, "", s.m.egressProbes(), proxyPoolProbeTimeout)
			s.m.markProxyProbe(id, res)
			if res.OK {
				s.reconcileProxyPoolAfterProbe(id == s.m.proxyPoolActiveID())
			}
			writeJSON(w, 200, map[string]any{"ok": res.OK, "result": res})
			return
		}
	}
	writeErr(w, 404, "代理不存在")
}

func (s *Server) testAllProxyPool(w http.ResponseWriter, r *http.Request) {
	s.proxyPoolTickMu.Lock()
	if s.m != nil && s.m.proxyPool != nil {
		s.m.proxyPool.mu.Lock()
		s.m.proxyPool.lastHealth = time.Time{}
		s.m.proxyPool.mu.Unlock()
	}
	s.proxyPoolTickLocked(true)
	s.proxyPoolTickMu.Unlock()
	entries, active, logs := s.m.ProxyPoolSnapshot()
	writeJSON(w, 200, map[string]any{"entries": entries, "active_id": active, "switch_log": logs})
}
