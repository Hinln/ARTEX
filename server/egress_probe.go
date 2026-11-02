package server

// egress_probe.go —— 全局出口代理的连通性探测。
//
// settings.global_proxy 保存时只做格式校验、一个字节都不发，所以死代理也能保存成功。
// 这里真发探测请求：① 直连的出口 IP，② 经这个代理的出口 IP。
// ② 发往哪个地址由 egressTarget 决定——那是本文件唯一需要判断的地方。

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/Autumn-27/artex/traffic"
)

// defaultEgressProbes 按顺序尝试，第一个成功即停；probe_url 可覆盖，供隔离网使用。
var defaultEgressProbes = []string{
	"https://api.ipify.org?format=json", // 返回 {"ip":"1.2.3.4"}
	"https://ifconfig.me/ip",            // 返回纯文本 1.2.3.4
}

// validateProbeURL 只放行指向"非本机、非链路本地"的绝对 http(s) 地址。
//
// 这个接口要登录，但仍要挡：agent 可能被目标页面诱导去调它，而"本机"在容器里就是 ARTEX
// 自己（管理端口、云元数据 169.254.169.254）。**私网地址是刻意放行的**——自定义探测服务
// 的主用途就是内网回显，挡掉 192.168/10/172.16 等于废掉这个功能。
func validateProbeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("probe_url 不是合法 URL")
	}
	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return "", errors.New("probe_url 只支持 http / https")
	}
	if u.Host == "" || u.Hostname() == "" {
		return "", errors.New("probe_url 缺少主机名")
	}
	if err := probeCheckURL(u); err != nil {
		return "", err
	}
	return raw, nil
}

// probeCheckRedirect 逐跳校验，挡住"公网地址 302 到 127.0.0.1"这种绕法。
func probeCheckRedirect(next *http.Request, via []*http.Request) error {
	if len(via) >= 3 {
		return errors.New("重定向次数过多")
	}
	if len(via) > 0 && next.URL.Host != via[0].URL.Host {
		return fmt.Errorf("拒绝跨主机重定向（%s → %s）", via[0].URL.Host, next.URL.Host)
	}
	return probeCheckURL(next.URL)
}

// probeCheckURL rejects only loopback, link-local, unspecified, and multicast
// destinations. RFC1918 addresses remain valid for intranet echo services.
func probeCheckURL(u *url.URL) error {
	host := strings.TrimSuffix(strings.TrimSpace(u.Hostname()), ".")
	if zone := strings.IndexByte(host, '%'); zone >= 0 {
		host = host[:zone]
	}
	ip := net.ParseIP(host)
	if ip == nil && repeaterLooksNumericHost(host) {
		var ok bool
		ip, ok = repeaterParseLegacyIPv4(host)
		if !ok {
			return fmt.Errorf("拒绝无法解析的数字主机地址 %q", host)
		}
	}
	if strings.EqualFold(host, "localhost") || (ip != nil && probeIsBlockedDialIP(ip)) {
		return errors.New("拒绝访问本机/链路本地地址")
	}
	return nil
}

func probeIsBlockedDialIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// probeBlockSensitiveDial is always enabled for direct probes. Unlike the
// repeater, probe_url has no local-target escape hatch.
func probeBlockSensitiveDial(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("无法解析目标地址 %q", host)
	}
	if probeIsBlockedDialIP(ip) {
		return fmt.Errorf("拒绝访问本机/链路本地地址 %s", ip)
	}
	return nil
}

// userinfoRe 匹配 URL 里的 user:pass@。
var userinfoRe = regexp.MustCompile(`://[^/@\s]+@`)

// maskSecrets 去掉 URL 里的用户名密码。它们会进 Configured / Path / probes[].error，
// 而这三处都会被界面渲染或写进日志。
func maskSecrets(s string) string { return userinfoRe.ReplaceAllString(s, "://***@") }

// egressAttempt 保留每个地址的失败原因，才能区分"连不上代理"和"代理通但出不了网"。
type egressAttempt struct {
	URL       string `json:"url"`
	OK        bool   `json:"ok"`
	IP        string `json:"ip,omitempty"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

// egressProbeResult 是一个链路形态的探测汇总。
type egressProbeResult struct {
	OK        bool            `json:"ok"`
	ExitIP    string          `json:"exit_ip,omitempty"` // 外界看到的出口 IP
	Via       string          `json:"via,omitempty"`     // direct / http / https / socks5
	ViaLabel  string          `json:"via_label,omitempty"`
	LatencyMS int64           `json:"latency_ms"`
	Error     string          `json:"error,omitempty"`
	Probes    []egressAttempt `json:"probes"` // 每个地址的明细，便于排查
}

// egressCheck 是报告里的一行，对应一个链路形态。
type egressCheck struct {
	Key        string            `json:"key"`   // direct | global
	Label      string            `json:"label"` // 中文说明，前端直接显示
	Path       string            `json:"path"`  // 实际使用的代理地址（空 = 直连）
	Skipped    bool              `json:"skipped,omitempty"`
	SkipReason string            `json:"skip_reason,omitempty"`
	Result     egressProbeResult `json:"result"`
}

// egressReport 是 POST /api/settings/proxy/test 的返回体。
type egressReport struct {
	OK         bool          `json:"ok"`          // 以"② 经全局代理"为准；未配代理时看"① 直连"
	Capture    bool          `json:"capture"`     // 当前是否开启流量捕获
	Configured string        `json:"configured"`  // 表单传上来的 global_proxy
	AgentProxy string        `json:"agent_proxy"` // ProxyAddr() 的实际返回值
	Checks     []egressCheck `json:"checks"`
	Warnings   []string      `json:"warnings,omitempty"`
}

// probeEgress 不碰数据库、不碰 Server，所以测试里可以直接调、完全离线。
func probeEgress(ctx context.Context, proxyRaw, caPEMPath string, probes []string, timeout time.Duration) egressProbeResult {
	start := time.Now()

	// 复用 traffic 包的校验，保证"什么算合法代理"全项目只有一处定义。
	var proxyURL *url.URL
	if strings.TrimSpace(proxyRaw) != "" {
		u, err := traffic.ValidateProxyURL(proxyRaw)
		if err != nil {
			return egressProbeResult{
				OK:        false,
				Error:     maskSecrets(err.Error()),
				LatencyMS: time.Since(start).Milliseconds(),
				// 空数组不是 nil：nil 会序列化成 JSON null，前端按数组处理会炸。
				Probes: []egressAttempt{},
			}
		}
		proxyURL = u
	}

	tr, caErr := buildProbeTransport(proxyURL, caPEMPath)

	client := &http.Client{
		Transport: tr,
		// 比 ctx 宽 2 秒，让 ctx 先到期，错误里才是 "context deadline exceeded"。
		Timeout:       timeout + 2*time.Second,
		CheckRedirect: probeCheckRedirect,
	}

	attempts := make([]egressAttempt, 0, len(probes))
	for _, target := range probes {
		reqCtx, cancel := context.WithTimeout(ctx, timeout)
		t0 := time.Now()
		ip, err := probeOne(reqCtx, client, target)
		cancel()

		a := egressAttempt{URL: maskSecrets(target), LatencyMS: time.Since(t0).Milliseconds()}
		if err != nil {
			a.Error = maskSecrets(err.Error())
			// 只在证书类错误上补一句，否则连不上代理也会挂上不相干的 CA 说明。
			// 必须写进 probes[].error——界面渲染的是它，不是 result.error。
			if caErr != nil && strings.Contains(a.Error, "certificate") {
				a.Error = "流量捕获的 CA 不可用（" + caErr.Error() + "）：证书错误可能由此而来。" + a.Error
			}
		} else {
			a.OK, a.IP = true, ip
		}
		attempts = append(attempts, a)

		if a.OK {
			// via 说的是代理协议，不是出口的网络类型。
			via, viaLabel := "direct", "直连"
			if proxyURL != nil {
				via, viaLabel = proxyURL.Scheme, schemeLabel(proxyURL.Scheme)
			}
			return egressProbeResult{
				OK:        true,
				ExitIP:    ip,
				Via:       via,
				ViaLabel:  viaLabel,
				LatencyMS: time.Since(start).Milliseconds(),
				Probes:    attempts,
			}
		}
	}

	return egressProbeResult{
		OK:        false,
		Error:     "全部探测地址均失败",
		LatencyMS: time.Since(start).Milliseconds(),
		Probes:    attempts,
	}
}

// schemeLabel 把协议名翻译成人话（"http" → "HTTP 代理"）。
func schemeLabel(scheme string) string {
	switch strings.ToLower(scheme) {
	case "http":
		return "HTTP 代理"
	case "https":
		return "HTTPS 代理"
	case "socks5", "socks5h":
		return "SOCKS5 代理"
	default:
		return strings.ToUpper(scheme) + " 代理"
	}
}

// buildProbeTransport 不能用 http.DefaultTransport：它的 Proxy 会读容器里的 HTTP_PROXY，
// 那样"测直连"其实走了代理。这里 Proxy 留 nil 才是真直连。
func buildProbeTransport(proxyURL *url.URL, caPEMPath string) (*http.Transport, error) {
	tr := &http.Transport{
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: 8 * time.Second,
	}
	if proxyURL != nil {
		tr.Proxy = http.ProxyURL(proxyURL)
	} else {
		// 直连时在拨号阶段再查一次：挡住"域名解析后才变成 127.0.0.1"（DNS rebinding）。
		// 走代理时不装——那时拨的是代理地址，用户配 127.0.0.1 的本地代理是允许的。
		tr.DialContext = (&net.Dialer{Timeout: 10 * time.Second, Control: probeBlockSensitiveDial}).DialContext
	}
	// CA 不可用不 panic，把原因带回去拼进失败原因——否则只会看到 x509，看不出真实原因。
	if path := strings.TrimSpace(caPEMPath); path != "" {
		pemBytes, err := os.ReadFile(path)
		if err != nil {
			return tr, fmt.Errorf("读不到 %s", path)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pemBytes) {
			return tr, fmt.Errorf("%s 不是有效的 PEM", path)
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool}
	}
	return tr, nil
}

// probeOne 支持 JSON（{"ip":"..."}）和纯文本；认不出就报错，绝不猜一个 IP。
func probeOne(ctx context.Context, client *http.Client, target string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "artex-egress-probe/1.0")

	resp, err := client.Do(req)
	if err != nil {
		// 原样返回：dial refused / deadline exceeded / x509 各自对应不同故障。
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var v struct {
		IP string `json:"ip"`
	}
	if json.Unmarshal(body, &v) == nil && v.IP != "" {
		if net.ParseIP(strings.TrimSpace(v.IP)) == nil {
			return "", errors.New("响应里的 ip 字段不是合法 IP")
		}
		return strings.TrimSpace(v.IP), nil
	}

	// net.ParseIP 顺便挡掉 HTML（反爬页、代理错误页）。
	if s := strings.TrimSpace(string(body)); net.ParseIP(s) != nil {
		return s, nil
	}
	return "", fmt.Errorf("无法从响应中解析出 IP（收到 %d 字节，可能是反爬页或代理错误页）", len(body))
}

// egressTarget 决定 ② 把请求发到哪：
//
//	捕获关 → 表单值；捕获开+已保存 → MITM；捕获开+未保存 → 表单值（跳过 MITM）
//
// 第三种不能走 MITM：MITM 的上游只由 SetGlobalProxy 更新，读不到请求体里的新值，
// 走它只会测出旧代理。viaMITM 供调用方放宽超时。
func egressTarget(cfg string, capture, pending bool, agentProxy, agentCA string) (addr, ca string, viaMITM bool) {
	if cfg == "" {
		return "", "", false
	}
	if capture && !pending && agentProxy != "" {
		return agentProxy, agentCA, true
	}
	return cfg, "", false
}

// testEgressProxy 的语义对齐 testWebSearch：地址取自请求体（未保存也能测）、
// 空 = 测直连、永远返回 200 用 ok 表达结果。
func (s *Server) testEgressProxy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GlobalProxy string `json:"global_proxy"`
		ProbeURL    string `json:"probe_url"` // 可选：覆盖默认探测地址
	}
	// 请求体只有一个地址，给个上限，免得被塞大 payload。
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	// 空 body 等价于测直连。
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, 400, err.Error())
		return
	}

	probes := defaultEgressProbes
	if u := strings.TrimSpace(req.ProbeURL); u != "" {
		checked, err := validateProbeURL(u)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		probes = []string{checked}
	}

	// 这三个调用决定 agent 到底会走哪条路。
	capture := s.m.TrafficEnabled() // 流量捕获开关
	agentProxy := s.m.ProxyAddr()   // agent 实际使用的代理地址
	agentCA := s.m.ProxyCACert()    // 捕获开时是 MITM 的 CA 路径；关时为空
	savedProxy := s.m.GlobalProxy() // 已保存的 global_proxy

	// 表单值 ≠ 已保存值时，② 仍按表单值测，但 agent 走的是已保存值——必须提示。
	// 归一化再比：尾斜杠不算"未保存"。
	pending := normalizeProxy(req.GlobalProxy) != normalizeProxy(savedProxy)

	// 整体上限 40s：单次 Timeout 只兜一次，不兜总量；前端 fetch 没有超时。
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	proxyGuards := s.snapshotProxyReconnectGuards(false)

	configuredProxy := strings.TrimSpace(req.GlobalProxy)
	rep := egressReport{
		Capture:    capture,
		Configured: maskSecrets(configuredProxy),
		AgentProxy: maskSecrets(agentProxy),
	}

	rep.Checks = append(rep.Checks, egressCheck{
		Key:    "direct",
		Label:  "① 直连 IP（不经过代理）",
		Result: probeEgress(ctx, "", "", probes, 10*time.Second),
	})

	if configuredProxy != "" {
		probeAddr, probeCA, viaMITM := egressTarget(configuredProxy, capture, pending, agentProxy, agentCA)
		timeout := 10 * time.Second
		if viaMITM {
			timeout = 15 * time.Second // 多了一跳 TLS 解密 + 重签
		}
		rep.Checks = append(rep.Checks, egressCheck{
			Key:   "global",
			Label: "② 经全局代理的出口 IP",
			// Path 是实际探测地址（经 MITM 时是 8788），排查时看这个。
			Path:   maskSecrets(probeAddr),
			Result: probeEgress(ctx, probeAddr, probeCA, probes, timeout),
		})
	} else {
		skipReason := "输入框为空，未配置代理"
		if savedProxy != "" {
			// 说"未配置代理"会和 W1（"Agent 仍使用 xxx"）互相否定。
			skipReason = "输入框为空；已保存的代理仍在生效"
		}
		rep.Checks = append(rep.Checks, egressCheck{
			Key:        "global",
			Label:      "② 经全局代理的出口 IP",
			Skipped:    true,
			SkipReason: skipReason,
			Result:     egressProbeResult{Probes: []egressAttempt{}},
		})
	}

	rep.OK = judgeEgress(rep)
	rep.Warnings = egressWarnings(rep, pending, savedProxy)
	if rep.OK && !pending {
		// The settings page uses this endpoint for its manual "测试代理" action.
		// A successful probe is the same signal as an automatic reconnect success,
		// but it must never consume an automatic attempt.
		s.proxyReconnectSuccessWithGuards(proxyGuards, nil)
	}
	writeJSON(w, 200, rep)
}

// normalizeProxy 只归一写法，不做校验；空串和解析失败原样返回。
func normalizeProxy(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

// judgeEgress 以 ② 为准；② 被跳过才看 ①。不能"任一行成功就算成功"。
func judgeEgress(rep egressReport) bool {
	for _, c := range rep.Checks {
		if c.Key == "global" && !c.Skipped {
			return c.Result.OK
		}
	}
	for _, c := range rep.Checks {
		if c.Key == "direct" {
			return c.Result.OK
		}
	}
	return false
}

// egressWarnings 生成"看数字容易忽略"的提醒。
func egressWarnings(rep egressReport, pending bool, savedProxy string) []string {
	var out []string
	safeSavedProxy := maskSecrets(savedProxy)

	// W1 agent 走的是已保存配置，不说清会被当成"测通了=配好了"。
	if pending {
		switch {
		case rep.Configured == "":
			out = append(out, "此地址尚未保存，Agent 当前仍使用 "+safeSavedProxy+"。保存后即改为直连。")
		case savedProxy == "":
			out = append(out, "此地址尚未保存，Agent 当前仍为直连。"+
				"上方结果对应输入框中的值，保存后生效。")
		default:
			out = append(out, "此地址尚未保存，Agent 当前仍使用 "+safeSavedProxy+
				"。上方结果对应输入框中的值，保存后生效。")
		}
	}

	var directIP, proxyIP string
	proxyOK := false
	for _, c := range rep.Checks {
		switch c.Key {
		case "direct":
			directIP = c.Result.ExitIP
		case "global":
			if !c.Skipped {
				proxyIP = c.Result.ExitIP
				proxyOK = c.Result.OK
			}
		}
	}

	// W2 看已保存值；只在 ② 通过时提示——没通的话"这里通不代表"本身就不成立。
	if !rep.Capture && proxyOK && strings.HasPrefix(strings.ToLower(savedProxy), "socks5") {
		out = append(out, "当前是「关闭捕获 + socks5」：本测试用的是 Go 客户端（一定遵守代理），"+
			"但 agent 的工具不一定——curl 能走，python urllib 会直接报错。"+
			"所以这里通不代表工具都能走；要稳定生效请开启流量捕获（由 MITM 亲自拨号）。")
	}

	// W3 两行相同 → 代理可能没生效，或对端只是透明转发。
	if directIP != "" && proxyIP != "" && directIP == proxyIP {
		out = append(out, fmt.Sprintf("直连与代理的出口 IP 相同（%s）：可能代理未生效、只是透明转发，"+
			"也可能跳板机与本机在同一 NAT 后面（出口本来就相同）。"+
			"要区分：在「自定义 IP 探测服务」里填一台内网回显服务，看它看到的源 IP 是否变成了跳板机。", directIP))
	}

	// W4 同样看已保存值，否则会和 W1 自相矛盾。
	if rep.Capture && savedProxy == "" {
		out = append(out, "已开启流量捕获，但全局代理为空：捕获只负责记录，出口仍是直连。")
	}

	// W5 容器里的 127.0.0.1 指容器自己，连不到宿主机上的代理。
	if isLoopbackProxy(rep.Configured) {
		out = append(out, "代理地址是回环地址（127.0.0.1 / localhost）。"+
			"如果 ARTEX 跑在容器里，这里的 127.0.0.1 指容器自身，连不到宿主机上的代理；"+
			"请改用宿主机的可达 IP（如 http://192.168.x.x:端口）。")
	}

	return out
}

func isLoopbackProxy(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
