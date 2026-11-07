package guard

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// nativeNetTools are agent tools that name a network target in their arguments.
// They never carry a shell command, so the command rules cannot see them — but
// the per-host budget must, otherwise the native path is a free bypass of the
// rate limit that Bash traffic obeys.
var nativeNetTools = map[string]bool{
	"http_request": true,
	"dns_lookup":   true,
	"tls_info":     true,
	"port_probe":   true,
	"fingerprint":  true,
	"favicon_hash": true,
	"robots_harvest": true,
	// icp_lookup only touches the engagement host in footer mode; the other
	// directions read a third-party index and are deliberately unbudgeted.
	"icp_lookup": true,
	"http_fuzz":  true,
	"authz_diff": true,
}

// nativeNetHosts pulls target hosts out of a native tool's JSON arguments.
func nativeNetHosts(tool string, raw json.RawMessage) []string {
	if !nativeNetTools[tool] {
		return nil
	}
	var in struct {
		Host   string `json:"host"`
		URL    string `json:"url"`
		Target string `json:"target"`
		Query  string `json:"query"`
		Mode   string `json:"mode"`
	}
	_ = json.Unmarshal(raw, &in)
	if tool == "icp_lookup" {
		if strings.ToLower(strings.TrimSpace(in.Mode)) != "footer" {
			return nil
		}
		return targetHosts(in.Query)
	}
	blob := strings.Join([]string{in.Host, in.URL, in.Target}, " ")
	return targetHosts(blob)
}

// checkHosts enforces the breaker and the per-host budget for a native call.
func (g *cmdGate) checkHosts(session string, hosts []string) verdict {
	if g == nil || !g.cfg.Enabled || len(hosts) == 0 {
		return verdict{}
	}
	g.mu.Lock()
	if until, ok := g.openTill[session]; ok && time.Now().Before(until) {
		left := time.Until(until).Round(time.Second)
		reason := g.lastDeny[session]
		g.mu.Unlock()
		return verdict{Deny: true, Rule: "breaker-open",
			Reason: "本任务连续触发安全闸门，已暂停执行（剩余 " + left.String() + "）。最后一次拦截：" + reason +
				"。请停止当前打法，改用合规方式，或把发现记录下来结束本轮。"}
	}
	g.mu.Unlock()

	now := time.Now()
	for _, h := range hosts {
		g.mu.Lock()
		cutoff := now.Add(-time.Minute)
		w := g.windows[h]
		keep := w[:0]
		for _, ts := range w {
			if ts.After(cutoff) {
				keep = append(keep, ts)
			}
		}
		g.windows[h] = keep
		exceeded := len(keep) >= g.cfg.RateRPM
		inflight := g.inflight[h] >= g.cfg.Concurrency
		if !exceeded && !inflight {
			g.windows[h] = append(g.windows[h], now)
		}
		g.mu.Unlock()
		if exceeded {
			g.deny(session, "rate-limit", h)
			return verdict{Deny: true, Rule: "rate-limit",
				Reason: "目标 " + h + " 已达频率上限（" + strconv.Itoa(g.cfg.RateRPM) + " 次/分钟）。等一分钟后继续，或减少重复探测。"}
		}
		if inflight {
			g.deny(session, "concurrency", h)
			return verdict{Deny: true, Rule: "concurrency",
				Reason: "同时对 " + h + " 的在途请求已达 " + strconv.Itoa(g.cfg.Concurrency) + " 个上限。串行执行。"}
		}
	}
	return verdict{}
}

func (g *cmdGate) acquireHosts(hosts []string) {
	if g == nil || !g.cfg.Enabled {
		return
	}
	g.mu.Lock()
	for _, h := range hosts {
		g.inflight[h]++
	}
	g.mu.Unlock()
}

func (g *cmdGate) doneHosts(hosts []string) {
	if g == nil || !g.cfg.Enabled {
		return
	}
	g.mu.Lock()
	for _, h := range hosts {
		if g.inflight[h] > 0 {
			g.inflight[h]--
		}
	}
	g.mu.Unlock()
}
