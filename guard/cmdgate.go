// Package guard: hard command gate.
//
// The intercept rules and the rule-pack deny rows are PROMPT text: they tell the
// model what not to do and nothing enforces it. Measured on 2026-11-07: a task
// installed the edusrc pack, whose deny list forbids full-port sweeps
// (agent/rulepacks.go "禁止全端口宽扫"), and the worker ran
//
//	nmap -n -Pn -sS -sV -sC -p- --min-rate 800
//
// on the first attempt, 34 Bash calls, zero errors. This file is the judgment
// layer that text was supposed to be: it runs before the command executes, and
// its answer is final.
//
// Three things live here:
//  1. CmdGate      — hard deny for scan/brute/exfil shapes.
//  2. RateLimiter  — per-target request rate and concurrency ceiling (default
//     300 rpm / 4 concurrent), because "widely" is not only a
//     command shape, it is also a number.
//  3. Breaker      — a run that keeps hitting the gate gets stopped instead of
//     burning the rest of its budget arguing with a wall.
package guard

import (
	"context"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// cmdRule is one hard denial. Hint is returned to the model; a denial without a
// reachable alternative just teaches it to paraphrase.
type cmdRule struct {
	Name string
	Re   *regexp.Regexp
	Hint string
}

// defaultCmdRules are deliberately about *shape*, not about words like "scan":
// the model can always rephrase, but it cannot make `-p-` stop being a full
// sweep.
var defaultCmdRules = []cmdRule{
	{
		Name: "full-port-sweep",
		Re:   regexp.MustCompile(`(?i)\bnmap\b[^\n;&|]*(?:-p[\s]*[-*]|--allports|-p[\s]*1-65535|-p[\s]*0-65535)`),
		Hint: "禁止全端口扫描。改用 nmap --top-ports 100 或明确列出目标端口（如 -p 22,80,443,8080）。",
	},
	{
		Name: "high-rate-sweep",
		Re:   regexp.MustCompile(`(?i)\b(?:masscan|zmap)\b`),
		Hint: "禁止 masscan/zmap 类高速扫描；单目标用 nmap 指定端口即可。",
	},
	{
		Name: "dir-bruteforce",
		Re:   regexp.MustCompile(`(?i)\b(?:ffuf|gobuster|dirsearch|feroxbuster|wfuzz|dirb)\b`),
		Hint: "禁止目录爆破。改用针对性探测：已知路径、robots.txt、sitemap.xml、页面里出现过的链接。",
	},
	{
		Name: "credential-bruteforce",
		Re:   regexp.MustCompile(`(?i)\b(?:hydra|medusa|patator|ncrack|sqlmap[^\n]*--(?:dump|os-shell|os-pwn|file-write|file-read))\b`),
		Hint: "禁止口令爆破与整库导出。用已获得的凭据验证单点登录；登录接口只做少量默认口令试探并记录。",
	},
	{
		Name: "sms-email-bomb",
		Re:   regexp.MustCompile(`(?i)(?:for|while|seq)[^\n]*(?:sendcode|sms|smscode|verify_code|sendmail)`),
		Hint: "禁止短信/邮件轰炸，也不得为取证连发。只发 1 次并记录响应。",
	},
	{
		Name: "credential-exfil",
		Re:   regexp.MustCompile(`(?i)\b(?:curl|wget|nc|ncat|socat)\b[^\n;&|]*(?:/etc/(?:passwd|shadow)|id_rsa|\.ssh/|\.aws/credentials|\.env\b|\.git-credentials)`),
		Hint: "禁止把本机凭据文件打出去。读取可以，外发不行。",
	},
	{
		Name: "reverse-shell-to-external",
		Re:   regexp.MustCompile(`(?i)\b(?:bash|sh|nc|ncat|socat)\b[^\n]{0,60}?(?:/dev/(?:tcp|udp)/|bash\s+-i|sh\s+-i|mkfifo)`),
		Hint: "反弹 shell 只能在已授权且已确认的资产上建立，且必须先记录到 finding 证据再执行；不允许直连外部主机。",
	},
}

// gateConfig holds the tunables. Env-driven so a deployment can adjust without a
// rebuild, defaults chosen for a single-target engagement.
type gateConfig struct {
	Enabled     bool
	RateRPM     int // per target host, network commands per minute
	Concurrency int // per target host, in-flight network commands
	BreakerN    int // consecutive denials before the run is stopped
	BreakerCool time.Duration
}

func envInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func loadGateConfig() gateConfig {
	c := gateConfig{
		Enabled:     strings.ToLower(os.Getenv("ARTEX_CMD_GATE")) != "off",
		RateRPM:     envInt("ARTEX_RATE_RPM", 300),
		Concurrency: envInt("ARTEX_RATE_CONCURRENCY", 4),
		BreakerN:    envInt("ARTEX_BREAKER_DENY_N", 5),
		BreakerCool: time.Duration(envInt("ARTEX_BREAKER_COOLDOWN_SEC", 300)) * time.Second,
	}
	return c
}

// cmdGate is the stateful half of the gate: rate windows and breakers.
type cmdGate struct {
	cfg gateConfig

	mu       sync.Mutex
	windows  map[string][]time.Time // host -> request timestamps within the last minute
	inflight map[string]int         // host -> in-flight count
	streak   map[string]int         // session -> consecutive denials
	openTill map[string]time.Time   // session -> breaker open until
	lastDeny map[string]string      // session -> last denial reason (for the report)
}

// privateTargetRe 抓「命令行里出现私网/保留段字面量」。能连通不等于在范围内：
// 上一版把本机教学靶场(172.26.0.0/16)当成客户内网，产出一批不可提交的结论。
// 127.0.0.1 故意不在列表里——本机自测不是"打别人"。
var privateTargetRe = regexp.MustCompile(`(?i)(?:\b(?:curl|wget|nc|ncat|socat|nmap|masscan|rustscan|sqlmap|hydra|python3?|node|nuclei)\b[^\n]{0,200}?|\bdocker\s+(?:exec|run)\b[^\n]{0,300}?)(?:10\.\d{1,3}\.\d{1,3}\.\d{1,3}|172\.(?:1[6-9]|2\d|3[01])\.\d{1,3}\.\d{1,3}|192\.168\.\d{1,3}\.\d{1,3}|169\.254\.\d{1,3}\.\d{1,3}|100\.(?:6[4-9]|[7-9]\d|1[01]\d|12[0-7])\.\d{1,3}\.\d{1,3}|198\.18\.\d{1,3}\.\d{1,3})`)

func init() {
	// 自家基础设施的别名：这些不是 IP，正则匹配不到，单列一条。
	defaultCmdRules = append(defaultCmdRules, cmdRule{
		Name: "self-target",
		Re:   regexp.MustCompile(`(?i)\b(?:host\.docker\.internal|gateway\.docker\.internal|metadata\.google\.internal)\b`),
		Hint: "这是 ARTEX 自身/宿主机的别名，打它等于打自己。要验证本机服务用 127.0.0.1 或容器名。",
	})
	defaultCmdRules = append(defaultCmdRules, cmdRule{
		Name: "private-target",
		Re:   privateTargetRe,
		Hint: "该地址属于 RFC1918/保留网段，不是客户的外部资产。确实拿到授权就先在任务范围里显式登记这个 IP（add_task_scope），否则不要碰；本机自测用 localhost。",
	})
}

func newCmdGate(cfg gateConfig) *cmdGate {
	g := &cmdGate{cfg: cfg, windows: map[string][]time.Time{}, inflight: map[string]int{}, streak: map[string]int{}, openTill: map[string]time.Time{}, lastDeny: map[string]string{}}
	activeGate.Store(g)
	return g
}

// verdict is what the hook acts on.
type verdict struct {
	Deny   bool
	Rule   string
	Reason string
}

var (
	reNetworkCmd = regexp.MustCompile(`(?i)\b(?:curl|wget|nc|ncat|nmap|sqlmap|hydra|python3?|node)\b`)
	reHost       = regexp.MustCompile(`(?i)(?:https?://)?\b([a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)+)\b`)
	// Hosts that never leave the machine and should not consume the budget.
	reLocalHost = regexp.MustCompile(`(?i)^(localhost|127\.|0\.0\.0\.0|::1)`)
)

// check runs the whole gate for one command. session keys the breaker.
func (g *cmdGate) check(session, cmd string) verdict {
	if g == nil || !g.cfg.Enabled || strings.TrimSpace(cmd) == "" {
		return verdict{}
	}

	// 1. Breaker first: an open breaker means nothing runs, whatever the command.
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

	// 2. Hard command shapes.
	for _, r := range defaultCmdRules {
		if !r.Re.MatchString(cmd) {
			continue
		}
		// private-target 不是一刀切：打进去以后的内网是正常战果，只有「自家基础
		// 设施」和「没登记过的私网」才拦。判定见 pvtlaw.go。
		if r.Name == "private-target" {
			if v, blocked := g.judgePrivate(session, cmd); blocked {
				g.deny(session, v.Rule, v.Reason)
				return v
			}
			continue
		}
		g.deny(session, r.Name, r.Hint)
		return verdict{Deny: true, Rule: r.Name, Reason: r.Hint}
	}

	// 3. Rate + concurrency, only for commands that actually hit the network.
	if !reNetworkCmd.MatchString(cmd) {
		return verdict{}
	}
	hosts := targetHosts(cmd)
	if len(hosts) == 0 {
		return verdict{}
	}
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, h := range hosts {
		cutoff := now.Add(-time.Minute)
		w := g.windows[h]
		keep := w[:0]
		for _, ts := range w {
			if ts.After(cutoff) {
				keep = append(keep, ts)
			}
		}
		g.windows[h] = keep
		if len(keep) >= g.cfg.RateRPM {
			g.mu.Unlock()
			g.deny(session, "rate-limit", h)
			g.mu.Lock()
			return verdict{Deny: true, Rule: "rate-limit",
				Reason: "目标 " + h + " 已达频率上限（" + strconv.Itoa(g.cfg.RateRPM) + " 次/分钟）。等一分钟后继续，或减少重复探测。"}
		}
		if g.inflight[h] >= g.cfg.Concurrency {
			g.mu.Unlock()
			g.deny(session, "concurrency", h)
			g.mu.Lock()
			return verdict{Deny: true, Rule: "concurrency",
				Reason: "同时对 " + h + " 的在途请求已达 " + strconv.Itoa(g.cfg.Concurrency) + " 个上限。串行执行。"}
		}
		g.windows[h] = append(g.windows[h], now)
	}
	return verdict{}
}

// done releases an in-flight slot after the command returns. Kept trivial and
// best-effort: the budget matters more than exact accounting.
func (g *cmdGate) done(cmd string) {
	if g == nil || !g.cfg.Enabled || !reNetworkCmd.MatchString(cmd) {
		return
	}
	for _, h := range targetHosts(cmd) {
		g.mu.Lock()
		if g.inflight[h] > 0 {
			g.inflight[h]--
		}
		g.mu.Unlock()
	}
}

// acquire counts an in-flight slot. Called right after check() approves.
func (g *cmdGate) acquire(cmd string) {
	if g == nil || !g.cfg.Enabled || !reNetworkCmd.MatchString(cmd) {
		return
	}
	for _, h := range targetHosts(cmd) {
		g.mu.Lock()
		g.inflight[h]++
		g.mu.Unlock()
	}
}

// deny records a denial and trips the breaker when the streak is long enough.
func (g *cmdGate) deny(session, rule, reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.streak[session]++
	g.lastDeny[session] = rule + ": " + reason
	if g.cfg.BreakerN > 0 && g.streak[session] >= g.cfg.BreakerN {
		g.openTill[session] = time.Now().Add(g.cfg.BreakerCool)
		g.streak[session] = 0
	}
}

// allow resets the denial streak: a run that corrects itself should not be
// punished by stale counts.
func (g *cmdGate) allow(session string) {
	g.mu.Lock()
	if g.streak[session] > 0 {
		g.streak[session] = 0
	}
	g.mu.Unlock()
}

// targetHosts extracts the hosts a command would touch.
func targetHosts(cmd string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, m := range reHost.FindAllStringSubmatch(cmd, -1) {
		h := strings.ToLower(strings.TrimSuffix(m[1], "."))
		if reLocalHost.MatchString(h) || seen[h] {
			continue
		}
		// Skip things that are obviously file paths or versions.
		if strings.HasSuffix(h, ".txt") || strings.HasSuffix(h, ".json") || strings.HasSuffix(h, ".log") {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	return out
}

// hostOfURL is a small helper kept for callers that already have a URL.
func hostOfURL(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

var _ = context.Background // keep context imported for future judge wiring

// activeGate 指向当前进程里的闸门实例，供 /api/stats 读实时计数。
var activeGate atomic.Pointer[cmdGate]

// GateStats 汇报闸门配置与当前负载：谁的预算快用完了，哪个会话被熔断。
func GateStats() map[string]any {
	g := activeGate.Load()
	if g == nil {
		return map[string]any{"enabled": false}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	open := make([]string, 0, len(g.openTill))
	for sess, until := range g.openTill {
		if now.Before(until) {
			open = append(open, sess+" ("+until.Sub(now).Round(time.Second).String()+")")
		}
	}
	return map[string]any{
		"enabled":              g.cfg.Enabled,
		"rate_rpm":             g.cfg.RateRPM,
		"concurrency":          g.cfg.Concurrency,
		"breaker_deny_n":       g.cfg.BreakerN,
		"breaker_cooldown_sec": int(g.cfg.BreakerCool.Seconds()),
		"tracked_hosts":        len(g.windows),
		"inflight_hosts":       len(g.inflight),
		"open_sessions":        open,
	}
}

// judgePrivate 逐个检查命令里出现的私网目标：
//   - 自身基础设施（回环/元数据/Docker 默认桥/host.docker.internal）→ 永远拦；
//   - 已在任务范围内（add_task_scope 且经人工批准）→ 放行；
//   - 其余私网 → 拦，并告诉跑的人该怎么把它变成合法目标。
func (g *cmdGate) judgePrivate(session, cmd string) (verdict, bool) {
	hosts := privateHosts(cmd)
	for _, h := range hosts {
		if why := SelfInfraReason(h); why != "" {
			return verdict{Deny: true, Rule: "self-target",
				Reason: why + "。要验证自己的服务请访问 127.0.0.1 或容器名，不要把它当客户资产。"}, true
		}
	}
	if len(hosts) == 0 {
		return verdict{}, false
	}
	for _, h := range hosts {
		if scopeAllowsPrivate(session, h) {
			continue
		}
		return verdict{Deny: true, Rule: "private-target",
			Reason: "目标 " + h + " 是私网/保留地址，且不在本任务已批准的范围里。" +
				"内网是正常战果，但要走流程：先 add_task_scope 登记这个地址（说明你是从哪台机器、哪条路发现它的），" +
				"人工在任务范围里批准之后才能打。自家靶场/宿主网关不会被批准，别浪费时间。"}, true
	}
	return verdict{}, false
}

var privateLiteralRe = regexp.MustCompile(`(?:10\.\d{1,3}\.\d{1,3}\.\d{1,3}|172\.(?:1[6-9]|2\d|3[01])\.\d{1,3}\.\d{1,3}|192\.168\.\d{1,3}\.\d{1,3}|169\.254\.\d{1,3}\.\d{1,3}|100\.(?:6[4-9]|[7-9]\d|1[01]\d|12[0-7])\.\d{1,3}\.\d{1,3}|198\.18\.\d{1,3}\.\d{1,3})`)

// privateHosts 取命令里出现的私网字面量（去重、去掉 :port）。
func privateHosts(cmd string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range privateLiteralRe.FindAllString(cmd, -1) {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}
