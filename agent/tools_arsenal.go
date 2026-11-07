package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

// The recon arsenal. Everything here is bounded on purpose: a worker that can
// ask for 65,536 ports or 200MB of body will do exactly that, and the gate that
// stops it in Bash does not see a native call. Caps live in the tool, the rate
// window lives in the gate's host budget (guard.nativeNetHosts).

const (
	portProbeMaxPorts = 64
	portProbeConcur   = 8
	portProbeTimeout  = 1500 * time.Millisecond
	dnsMaxAnswers     = 32
	endpointMaxOut    = 200
	fingerprintMaxHit = 24
)

// dnsLookup answers the records a task actually needs when it is deciding
// whether a host is worth a request: where it points, whether it is hosted, and
// whether it advertises a third-party service it should not.
func (t *ToolSet) dnsLookup() actool.CoreTool {
	return readTool("dns_lookup", "查询域名的 DNS 记录（A/AAAA/CNAME/MX/NS/TXT）。用于确认目标解析、识别托管商与第三方服务，不发起任何到目标的连接。",
		obj(map[string]any{
			"host":  str("域名，例如 example.com"),
			"types": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "记录类型子集，默认 A,AAAA,CNAME,MX,NS,TXT"},
		}, "host"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Host  string   `json:"host"`
				Types []string `json:"types"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(fmt.Sprintf("参数解析失败: %v", err)), nil
			}
			host := strings.TrimSpace(a.Host)
			if host == "" {
				return actool.Errorf("dns_lookup 需要 host"), nil
			}
			host = strings.TrimSuffix(host, ".")
			types := a.Types
			if len(types) == 0 {
				types = []string{"A", "AAAA", "CNAME", "MX", "NS", "TXT"}
			}
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			r := net.Resolver{}
			var b strings.Builder
			fmt.Fprintf(&b, "host: %s\n", host)
			for _, tp := range types {
				switch strings.ToUpper(strings.TrimSpace(tp)) {
				case "A", "AAAA":
					ips, err := r.LookupIP(ctx, map[string]string{"A": "ip4", "AAAA": "ip6"}[strings.ToUpper(tp)], host)
					if err != nil {
						fmt.Fprintf(&b, "%s: 无（%v）\n", strings.ToUpper(tp), err)
						continue
					}
					fmt.Fprintf(&b, "%s: %s\n", strings.ToUpper(tp), joinCap(ipStrings(ips), dnsMaxAnswers))
				case "CNAME":
					c, err := r.LookupCNAME(ctx, host)
					if err != nil {
						fmt.Fprintf(&b, "CNAME: 无（%v）\n", err)
						continue
					}
					fmt.Fprintf(&b, "CNAME: %s\n", c)
				case "MX":
					mxs, err := r.LookupMX(ctx, host)
					if err != nil {
						fmt.Fprintf(&b, "MX: 无（%v）\n", err)
						continue
					}
					rows := make([]string, 0, len(mxs))
					for _, mx := range mxs {
						rows = append(rows, fmt.Sprintf("%d %s", mx.Pref, mx.Host))
					}
					fmt.Fprintf(&b, "MX: %s\n", joinCap(rows, dnsMaxAnswers))
				case "NS":
					nss, err := r.LookupNS(ctx, host)
					if err != nil {
						fmt.Fprintf(&b, "NS: 无（%v）\n", err)
						continue
					}
					rows := make([]string, 0, len(nss))
					for _, ns := range nss {
						rows = append(rows, ns.Host)
					}
					fmt.Fprintf(&b, "NS: %s\n", joinCap(rows, dnsMaxAnswers))
				case "TXT":
					txts, err := r.LookupTXT(ctx, host)
					if err != nil {
						fmt.Fprintf(&b, "TXT: 无（%v）\n", err)
						continue
					}
					fmt.Fprintf(&b, "TXT: %s\n", joinCap(txts, dnsMaxAnswers))
				default:
					fmt.Fprintf(&b, "%s: 不支持的记录类型\n", tp)
				}
			}
			return actool.Text(b.String()), nil
		})
}

// tlsInfo reads the certificate a host presents without validating it — the
// point is what the target says about itself (SAN list, issuer, expiry), not
// whether a browser would trust it.
func (t *ToolSet) tlsInfo() actool.CoreTool {
	return readTool("tls_info", "读取目标 TLS 证书：主体、颁发者、SAN 列表、有效期与协商版本。SAN 常泄露内部域名，过期/自签常暴露运维问题。不校验证书链。",
		obj(map[string]any{
			"host": str("主机名或 IP"),
			"port": map[string]any{"type": "integer", "description": "端口，默认 443"},
		}, "host"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Host string `json:"host"`
				Port int    `json:"port"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(fmt.Sprintf("参数解析失败: %v", err)), nil
			}
			host := strings.TrimSpace(a.Host)
			if host == "" {
				return actool.Errorf("tls_info 需要 host"), nil
			}
			port := a.Port
			if port <= 0 || port > 65535 {
				port = 443
			}
			addr := net.JoinHostPort(host, strconv.Itoa(port))
			d := &net.Dialer{Timeout: 6 * time.Second}
			conn, err := tls.DialWithDialer(d, "tcp", addr, &tls.Config{InsecureSkipVerify: true, ServerName: host})
			if err != nil {
				return actool.Errorf(fmt.Sprintf("TLS 握手失败 %s: %v", addr, err)), nil
			}
			defer conn.Close()
			state := conn.ConnectionState()
			var b strings.Builder
			fmt.Fprintf(&b, "addr: %s\nversion: %s\ncipher: %s\n", addr, tlsVersionName(state.Version), tls.CipherSuiteName(state.CipherSuite))
			fmt.Fprintf(&b, "alpn: %s\n", orDash(state.NegotiatedProtocol))
			for i, cert := range state.PeerCertificates {
				if i > 2 {
					break
				}
				fmt.Fprintf(&b, "cert[%d] subject: %s\n", i, cert.Subject.String())
				fmt.Fprintf(&b, "cert[%d] issuer: %s\n", i, cert.Issuer.String())
				fmt.Fprintf(&b, "cert[%d] not_before: %s\n", i, cert.NotBefore.Format(time.RFC3339))
				fmt.Fprintf(&b, "cert[%d] not_after: %s\n", i, cert.NotAfter.Format(time.RFC3339))
				fmt.Fprintf(&b, "cert[%d] dns_names: %s\n", i, joinCap(cert.DNSNames, 40))
				if len(cert.IPAddresses) > 0 {
					ips := make([]string, 0, len(cert.IPAddresses))
					for _, ip := range cert.IPAddresses {
						ips = append(ips, ip.String())
					}
					fmt.Fprintf(&b, "cert[%d] ip_names: %s\n", i, joinCap(ips, 20))
				}
			}
			return actool.Text(b.String()), nil
		})
}

// portProbe opens TCP connections to an explicit, short list of ports. It is
// deliberately not a port scanner: no SYN, no ranges wider than portProbeMaxPorts,
// and callers must name the ports they care about.
func (t *ToolSet) portProbe() actool.CoreTool {
	return readTool("port_probe", "按明确列出的端口做 TCP 连通性探测（上限 64 个端口，可写 \"22,80,443\" 或 \"8000-8010\"）。不做全端口扫描、不做服务指纹爆破；需要更大范围时先确认资产归属再逐个端口推进。",
		obj(map[string]any{
			"host":       str("主机名或 IP"),
			"ports":      str("端口列表，例如 \"22,80,443\" 或 \"8000-8010\"（总跨度不超过 64）"),
			"timeout_ms": map[string]any{"type": "integer", "description": "单端口超时毫秒，默认 1500，上限 5000"},
		}, "host", "ports"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Host      string `json:"host"`
				Ports     string `json:"ports"`
				TimeoutMs int    `json:"timeout_ms"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(fmt.Sprintf("参数解析失败: %v", err)), nil
			}
			host := strings.TrimSpace(a.Host)
			if host == "" {
				return actool.Errorf("port_probe 需要 host"), nil
			}
			ports, err := parsePortSpec(a.Ports)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			timeout := time.Duration(a.TimeoutMs) * time.Millisecond
			if timeout <= 0 {
				timeout = portProbeTimeout
			}
			if timeout > 5*time.Second {
				timeout = 5 * time.Second
			}
			type row struct {
				port int
				open bool
			}
			sem := make(chan struct{}, portProbeConcur)
			out := make(chan row, len(ports))
			for _, p := range ports {
				go func(port int) {
					sem <- struct{}{}
					defer func() { <-sem }()
					conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), timeout)
					if err != nil {
						out <- row{port: port}
						return
					}
					_ = conn.Close()
					out <- row{port: port, open: true}
				}(p)
			}
			got := make([]row, 0, len(ports))
			for range ports {
				select {
				case r := <-out:
					got = append(got, r)
				case <-ctx.Done():
					return actool.Errorf("port_probe 被取消"), nil
				}
			}
			sort.Slice(got, func(i, j int) bool { return got[i].port < got[j].port })
			open := make([]string, 0, len(got))
			for _, r := range got {
				if r.open {
					open = append(open, strconv.Itoa(r.port))
				}
			}
			var b strings.Builder
			fmt.Fprintf(&b, "host: %s\nprobed: %d\nopen: %s\n", host, len(ports), orDash(strings.Join(open, ",")))
			if len(open) == 0 {
				b.WriteString("说明：全部未开放。不要据此扩大端口范围重扫，先确认目标是否存活、是否有防火墙。\n")
			}
			return actool.Text(b.String()), nil
		})
}

// parseResponse extracts the parts of a raw HTTP response a worker would
// otherwise re-derive by hand: status, interesting headers, title, forms,
// parameters and the technology hints those imply.
func (t *ToolSet) parseResponse() actool.CoreTool {
	return readTool("parse_response", "解析一段原始 HTTP 响应（状态行 + 头 + 正文）：提取状态码、关键响应头、页面标题、表单及参数名、跳转位置、技术栈线索。贴 curl -i 的输出即可。",
		obj(map[string]any{
			"text": str("原始响应文本，含状态行与响应头"),
		}, "text"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(fmt.Sprintf("参数解析失败: %v", err)), nil
			}
			if strings.TrimSpace(a.Text) == "" {
				return actool.Errorf("parse_response 需要 text"), nil
			}
			status, headers, body := splitHTTP(a.Text)
			var b strings.Builder
			fmt.Fprintf(&b, "status: %s\n", orDash(status))
			for _, h := range []string{"Server", "X-Powered-By", "Location", "Content-Type", "Set-Cookie", "Via", "X-AspNet-Version", "X-Generator"} {
				if v, ok := headers[strings.ToLower(h)]; ok && strings.TrimSpace(v) != "" {
					fmt.Fprintf(&b, "%s: %s\n", h, clip(v, 200))
				}
			}
			if title := htmlTitle(body); title != "" {
				fmt.Fprintf(&b, "title: %s\n", clip(title, 160))
			}
			if forms := htmlForms(body); len(forms) > 0 {
				fmt.Fprintf(&b, "forms: %s\n", joinCap(forms, 12))
			}
			if params := htmlParamNames(body); len(params) > 0 {
				fmt.Fprintf(&b, "params: %s\n", joinCap(params, 40))
			}
			if hits := techHits(headers, body); len(hits) > 0 {
				fmt.Fprintf(&b, "tech: %s\n", joinCap(hits, fingerprintMaxHit))
			}
			fmt.Fprintf(&b, "body_bytes: %d\n", len(body))
			return actool.Text(b.String()), nil
		})
}

// extractEndpoints turns a page or a JS bundle into the path list a worker
// should try next. This is what replaces fuzzing: the application tells you its
// own routes, if you read them.
func (t *ToolSet) extractEndpoints() actool.CoreTool {
	return readTool("extract_endpoints", "从 HTML/JS/JSON 文本中提取接口与路径：链接、表单 action、fetch/axios 调用、绝对 URL、形如 /a/b 的路径与查询参数名。用于在不爆破目录的前提下拿到真实路由。",
		obj(map[string]any{
			"text":     str("HTML / JS / JSON 文本"),
			"base_url": str("可选，用于把相对路径补成绝对 URL"),
		}, "text"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Text    string `json:"text"`
				BaseURL string `json:"base_url"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(fmt.Sprintf("参数解析失败: %v", err)), nil
			}
			if strings.TrimSpace(a.Text) == "" {
				return actool.Errorf("extract_endpoints 需要 text"), nil
			}
			found := collectEndpoints(a.Text, a.BaseURL)
			if len(found) == 0 {
				return actool.Text("未提取到路径。若这是压缩/混淆后的 bundle，先剔除体积再解析，或直接看其中的字符串常量。"), nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "endpoints: %d\n", len(found))
			for _, e := range found {
				b.WriteString(e)
				b.WriteByte('\n')
			}
			return actool.Text(b.String()), nil
		})
}

// fingerprint identifies what the target runs from headers, cookies and body
// markers, and says which marker fired — a claim without evidence is not usable
// in a report.
func (t *ToolSet) fingerprintTool() actool.CoreTool {
	return readTool("fingerprint", "识别目标技术栈：可从 URL 直接取（经录制代理），也可只给响应头/正文文本。输出每个命中的组件与证据字符串，便于写报告时引用。",
		obj(map[string]any{
			"url":   str("可选，目标 URL；给了就发起一次 GET 并分析"),
			"text":  str("可选，已有响应原文（含头）；与 url 二选一"),
			"title": str("可选，已知页面标题，参与匹配"),
		}), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				URL   string `json:"url"`
				Text  string `json:"text"`
				Title string `json:"title"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(fmt.Sprintf("参数解析失败: %v", err)), nil
			}
			var headers map[string]string
			body := a.Text
			if strings.TrimSpace(a.URL) != "" {
				client, err := t.httpClient(httpDefaultTimeout, false)
				if err != nil {
					return actool.Errorf(fmt.Sprintf("HTTP 客户端不可用: %v", err)), nil
				}
				reqCtx, cancel := context.WithTimeout(ctx, httpDefaultTimeout*time.Second)
				defer cancel()
				req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, a.URL, nil)
				if err != nil {
					return actool.Errorf(fmt.Sprintf("URL 无效: %v", err)), nil
				}
				req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; ARTEX)")
				resp, err := client.Do(req)
				if err != nil {
					return actool.Errorf(fmt.Sprintf("请求失败: %v", err)), nil
				}
				defer resp.Body.Close()
				raw, _ := io.ReadAll(io.LimitReader(resp.Body, httpMaxBody))
				headers = headerMap(resp.Header)
				body = string(raw)
				headers["status"] = resp.Status
			} else if strings.TrimSpace(a.Text) != "" {
				_, h, b := splitHTTP(a.Text)
				headers = h
				body = b
			} else {
				return actool.Errorf("fingerprint 需要 url 或 text"), nil
			}
			if t := strings.TrimSpace(a.Title); t != "" {
				body = t + "\n" + body
			}
			hits := techHits(headers, body)
			var b strings.Builder
			if st := headers["status"]; st != "" {
				fmt.Fprintf(&b, "status: %s\n", st)
			}
			if len(hits) == 0 {
				b.WriteString("未命中已知指纹。把响应头与 title 记录下来，人工判断后再报，不要凭猜测写组件名。\n")
			} else {
				fmt.Fprintf(&b, "tech (%d):\n", len(hits))
				for _, h := range hits {
					fmt.Fprintf(&b, "- %s\n", h)
				}
			}
			return actool.Text(b.String()), nil
		})
}

// ---------------- implementation details ----------------

func ipStrings(ips []net.IP) []string {
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		out = append(out, ip.String())
	}
	return out
}

func joinCap(rows []string, capN int) string {
	filtered := make([]string, 0, len(rows))
	for _, r := range rows {
		if strings.TrimSpace(r) != "" {
			filtered = append(filtered, r)
		}
	}
	if len(filtered) > capN {
		filtered = append(filtered[:capN], fmt.Sprintf("…(共 %d 条)", len(rows)))
	}
	return orDash(strings.Join(filtered, ", "))
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func tlsVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS10:
		return "TLS1.0"
	case tls.VersionTLS11:
		return "TLS1.1"
	case tls.VersionTLS12:
		return "TLS1.2"
	case tls.VersionTLS13:
		return "TLS1.3"
	}
	return fmt.Sprintf("0x%04x", v)
}

// parsePortSpec accepts "22,80,443" and "8000-8010" but refuses anything wider
// than the probe budget: widening the range is how "just checking 8080" turns
// into a port sweep.
func parsePortSpec(spec string) ([]int, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, fmt.Errorf("port_probe 需要 ports，例如 \"22,80,443\"")
	}
	seen := map[int]bool{}
	var out []int
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			lo, err1 := strconv.Atoi(strings.TrimSpace(bounds[0]))
			hi, err2 := strconv.Atoi(strings.TrimSpace(bounds[1]))
			if err1 != nil || err2 != nil || lo <= 0 || hi <= 0 || hi < lo {
				return nil, fmt.Errorf("端口区间无效: %s", part)
			}
			if hi-lo+1 > portProbeMaxPorts {
				return nil, fmt.Errorf("端口跨度 %d 超过上限 %d；不允许做端口扫描，请列出确切端口", hi-lo+1, portProbeMaxPorts)
			}
			for p := lo; p <= hi; p++ {
				if !seen[p] {
					seen[p] = true
					out = append(out, p)
				}
			}
			continue
		}
		p, err := strconv.Atoi(part)
		if err != nil || p <= 0 || p > 65535 {
			return nil, fmt.Errorf("端口无效: %s", part)
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("没有解析到端口")
	}
	if len(out) > portProbeMaxPorts {
		return nil, fmt.Errorf("端口数 %d 超过上限 %d；请列出确切端口", len(out), portProbeMaxPorts)
	}
	sort.Ints(out)
	return out, nil
}

// splitHTTP separates an optional status line, the headers, and the body. It
// tolerates a body-only input so the tool is usable on a bare HTML snippet.
func splitHTTP(text string) (string, map[string]string, string) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	headers := map[string]string{}
	status := ""
	body := text
	if idx := strings.Index(text, "\n\n"); idx >= 0 {
		head, rest := text[:idx], text[idx+2:]
		lines := strings.Split(head, "\n")
		if len(lines) > 0 && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(lines[0])), "HTTP/") {
			status = strings.TrimSpace(lines[0])
			lines = lines[1:]
		}
		for _, line := range lines {
			k, v, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			key := strings.ToLower(strings.TrimSpace(k))
			val := strings.TrimSpace(v)
			if prev, exists := headers[key]; exists {
				headers[key] = prev + "; " + val
			} else {
				headers[key] = val
			}
		}
		body = rest
	} else if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(text)), "HTTP/") {
		status = strings.TrimSpace(strings.SplitN(text, "\n", 2)[0])
		body = ""
	}
	return status, headers, body
}

func headerMap(h map[string][]string) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		out[strings.ToLower(k)] = strings.Join(v, "; ")
	}
	return out
}

var reTitle = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

func htmlTitle(body string) string {
	if m := reTitle.FindStringSubmatch(body); len(m) == 2 {
		return strings.Join(strings.Fields(m[1]), " ")
	}
	return ""
}

var reForm = regexp.MustCompile(`(?is)<form[^>]*>`)
var reFormAttr = regexp.MustCompile(`(?i)(action|method)\s*=\s*["']([^"']*)["']`)

func htmlForms(body string) []string {
	var out []string
	for _, tag := range reForm.FindAllString(body, 12) {
		action, method := "", "GET"
		for _, m := range reFormAttr.FindAllStringSubmatch(tag, -1) {
			if strings.EqualFold(m[1], "action") {
				action = m[2]
			} else {
				method = strings.ToUpper(m[2])
			}
		}
		out = append(out, fmt.Sprintf("%s %s", method, orDash(action)))
	}
	return out
}

var reInput = regexp.MustCompile(`(?i)<(?:input|select|textarea)[^>]*\bname\s*=\s*["']([^"']+)["']`)
var reQueryKey = regexp.MustCompile(`[?&]([a-zA-Z0-9_\-\[\]]{1,40})=`)

func htmlParamNames(body string) []string {
	seen := map[string]bool{}
	var out []string
	for _, re := range []*regexp.Regexp{reInput, reQueryKey} {
		for _, m := range re.FindAllStringSubmatch(body, 200) {
			name := strings.TrimSpace(m[1])
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

type techSig struct {
	name  string
	score func(headers map[string]string, body string) string
}

func headerHas(headers map[string]string, key, needle string) string {
	if v, ok := headers[key]; ok && strings.Contains(strings.ToLower(v), strings.ToLower(needle)) {
		return fmt.Sprintf("%s: %s", key, clip(v, 80))
	}
	return ""
}

func bodyHas(body, needle string) string {
	if strings.Contains(strings.ToLower(body), strings.ToLower(needle)) {
		return fmt.Sprintf("body ~ %q", needle)
	}
	return ""
}

// techSigs is deliberately small and evidence-first: every entry names the exact
// string that fired, so a finding never claims a stack it cannot show.
var techSigs = []techSig{
	{"nginx", func(h map[string]string, b string) string { return headerHas(h, "server", "nginx") }},
	{"apache", func(h map[string]string, b string) string { return headerHas(h, "server", "apache") }},
	{"iis", func(h map[string]string, b string) string {
		if e := headerHas(h, "server", "microsoft-iis"); e != "" {
			return e
		}
		return headerHas(h, "x-aspnet-version", ".")
	}},
	{"php", func(h map[string]string, b string) string {
		if e := headerHas(h, "x-powered-by", "php"); e != "" {
			return e
		}
		return headerHas(h, "set-cookie", "phpsessid")
	}},
	{"asp.net", func(h map[string]string, b string) string {
		if e := headerHas(h, "set-cookie", "asp.net_sessionid"); e != "" {
			return e
		}
		return headerHas(h, "x-aspnetmvc-version", ".")
	}},
	{"java", func(h map[string]string, b string) string { return headerHas(h, "set-cookie", "jsessionid") }},
	{"tomcat", func(h map[string]string, b string) string {
		if e := headerHas(h, "server", "tomcat"); e != "" {
			return e
		}
		return bodyHas(b, "Apache Tomcat")
	}},
	{"spring", func(h map[string]string, b string) string {
		if e := bodyHas(b, "Whitelabel Error Page"); e != "" {
			return e
		}
		return headerHas(h, "x-application-context", ".")
	}},
	{"cloudflare", func(h map[string]string, b string) string {
		if e := headerHas(h, "server", "cloudflare"); e != "" {
			return e
		}
		return headerHas(h, "cf-ray", ".")
	}},
	{"akamai", func(h map[string]string, b string) string { return headerHas(h, "server", "akamaighost") }},
	{"vue", func(h map[string]string, b string) string {
		if e := bodyHas(b, "data-v-"); e != "" {
			return e
		}
		return bodyHas(b, "__VUE__")
	}},
	{"react", func(h map[string]string, b string) string { return bodyHas(b, "data-reactroot") }},
	{"angular", func(h map[string]string, b string) string { return bodyHas(b, "ng-version") }},
	{"jquery", func(h map[string]string, b string) string { return bodyHas(b, "jquery") }},
	{"bootstrap", func(h map[string]string, b string) string { return bodyHas(b, "bootstrap.min.css") }},
	{"swagger", func(h map[string]string, b string) string {
		if e := bodyHas(b, "swagger-ui"); e != "" {
			return e
		}
		return bodyHas(b, "\"swagger\":")
	}},
	{"grafana", func(h map[string]string, b string) string { return bodyHas(b, "grafana") }},
	{"jenkins", func(h map[string]string, b string) string { return headerHas(h, "x-jenkins", ".") }},
	{"shiro", func(h map[string]string, b string) string { return headerHas(h, "set-cookie", "rememberme=") }},
	{"weblogic", func(h map[string]string, b string) string { return bodyHas(b, "WebLogic Server") }},
	{"nacos", func(h map[string]string, b string) string { return bodyHas(b, "nacos") }},
	{"druid", func(h map[string]string, b string) string { return bodyHas(b, "Druid Stat Index") }},
	{"express", func(h map[string]string, b string) string { return headerHas(h, "x-powered-by", "express") }},
	{"nextjs", func(h map[string]string, b string) string { return bodyHas(b, "__NEXT_DATA__") }},
	{"wordpress", func(h map[string]string, b string) string { return bodyHas(b, "wp-content") }},
	// 服务端与中间件
	{"openresty", func(h map[string]string, b string) string { return headerHas(h, "server", "openresty") }},
	{"tengine", func(h map[string]string, b string) string { return headerHas(h, "server", "tengine") }},
	{"caddy", func(h map[string]string, b string) string { return headerHas(h, "server", "caddy") }},
	{"lighttpd", func(h map[string]string, b string) string { return headerHas(h, "server", "lighttpd") }},
	{"jetty", func(h map[string]string, b string) string {
		if e := headerHas(h, "server", "jetty"); e != "" {
			return e
		}
		return headerHas(h, "server", "jetty(")
	}},
	{"jboss", func(h map[string]string, b string) string {
		if e := headerHas(h, "x-powered-by", "jboss"); e != "" {
			return e
		}
		return bodyHas(b, "JBoss Web")
	}},
	{"websphere", func(h map[string]string, b string) string { return headerHas(h, "server", "websphere") }},
	{"kestrel", func(h map[string]string, b string) string { return headerHas(h, "server", "kestrel") }},
	{"werkzeug", func(h map[string]string, b string) string { return headerHas(h, "server", "werkzeug") }},
	{"gunicorn", func(h map[string]string, b string) string { return headerHas(h, "server", "gunicorn") }},
	{"envoy", func(h map[string]string, b string) string { return headerHas(h, "server", "envoy") }},
	{"haproxy", func(h map[string]string, b string) string { return headerHas(h, "server", "haproxy") }},
	{"traefik", func(h map[string]string, b string) string { return headerHas(h, "server", "traefik") }},
	{"kong", func(h map[string]string, b string) string { return headerHas(h, "server", "kong") }},
	{"varnish", func(h map[string]string, b string) string { return headerHas(h, "x-varnish", ".") }},
	// 框架与应用服务器指纹
	{"nodejs", func(h map[string]string, b string) string {
		if e := headerHas(h, "x-powered-by", "express"); e != "" {
			return e
		}
		return headerHas(h, "x-powered-by", "node")
	}},
	{"thinkphp", func(h map[string]string, b string) string {
		if e := bodyHas(b, "ThinkPHP"); e != "" {
			return e
		}
		return headerHas(h, "x-powered-by", "thinkphp")
	}},
	{"laravel", func(h map[string]string, b string) string { return headerHas(h, "set-cookie", "laravel_session") }},
	{"django", func(h map[string]string, b string) string {
		if e := headerHas(h, "set-cookie", "csrftoken"); e != "" {
			return e
		}
		return bodyHas(b, "csrfmiddlewaretoken")
	}},
	{"flask", func(h map[string]string, b string) string { return headerHas(h, "set-cookie", "session=ey") }},
	{"rails", func(h map[string]string, b string) string { return headerHas(h, "x-runtime", ".") }},
	{"struts2", func(h map[string]string, b string) string { return bodyHas(b, "struts.devMode") }},
	{"springboot-actuator", func(h map[string]string, b string) string { return bodyHas(b, "\"_links\"") }},
	{"nuxt", func(h map[string]string, b string) string { return bodyHas(b, "__NUXT__") }},
	{"vite", func(h map[string]string, b string) string { return bodyHas(b, "/@vite/client") }},
	{"webpack", func(h map[string]string, b string) string { return bodyHas(b, "webpackJsonp") }},
	{"layui", func(h map[string]string, b string) string { return bodyHas(b, "layui.js") }},
	{"elementui", func(h map[string]string, b string) string { return bodyHas(b, "el-button") }},
	{"antd", func(h map[string]string, b string) string { return bodyHas(b, "ant-btn") }},
	{"echarts", func(h map[string]string, b string) string { return bodyHas(b, "echarts") }},
	// 国产 OA / ERP / 统一身份认证（这些名字本身就是漏洞面线索）
	{"weaver-eoffice", func(h map[string]string, b string) string {
		if e := bodyHas(b, "/wui/index.html"); e != "" {
			return e
		}
		return bodyHas(b, "eoffice")
	}},
	{"weaver-ecology", func(h map[string]string, b string) string {
		if e := headerHas(h, "set-cookie", "ecology_j"); e != "" {
			return e
		}
		return bodyHas(b, "ecology")
	}},
	{"seeyon-oa", func(h map[string]string, b string) string { return bodyHas(b, "seeyon") }},
	{"tongda-oa", func(h map[string]string, b string) string { return bodyHas(b, "Office Anywhere") }},
	{"landray-ekp", func(h map[string]string, b string) string { return bodyHas(b, "landray") }},
	{"yonyou-nc", func(h map[string]string, b string) string {
		if e := bodyHas(b, "yonyou"); e != "" {
			return e
		}
		return bodyHas(b, "用友")
	}},
	{"kingdee", func(h map[string]string, b string) string {
		if e := bodyHas(b, "kingdee"); e != "" {
			return e
		}
		return bodyHas(b, "金蝶")
	}},
	{"wisedu-cas", func(h map[string]string, b string) string {
		if e := bodyHas(b, "authserver"); e != "" {
			return e
		}
		return bodyHas(b, "统一身份认证")
	}},
	{"cas-sso", func(h map[string]string, b string) string {
		if e := bodyHas(b, "cas/login"); e != "" {
			return e
		}
		return bodyHas(b, "Central Authentication Service")
	}},
	// 安全设备 / WAF
	{"sangfor", func(h map[string]string, b string) string {
		if e := bodyHas(b, "SANGFOR"); e != "" {
			return e
		}
		return headerHas(h, "set-cookie", "sinfor")
	}},
	{"topsec", func(h map[string]string, b string) string { return bodyHas(b, "topsec") }},
	{"hikvision", func(h map[string]string, b string) string {
		if e := bodyHas(b, "Hikvision"); e != "" {
			return e
		}
		return bodyHas(b, "ivms")
	}},
	{"dahua", func(h map[string]string, b string) string { return bodyHas(b, "Dahua") }},
	{"ruijie", func(h map[string]string, b string) string { return bodyHas(b, "ruijie") }},
	{"h3c", func(h map[string]string, b string) string { return bodyHas(b, "H3C") }},
	{"safedog", func(h map[string]string, b string) string {
		if e := bodyHas(b, "safedog"); e != "" {
			return e
		}
		return headerHas(h, "server", "safedog")
	}},
	{"btwaf", func(h map[string]string, b string) string { return headerHas(h, "server", "btwaf") }},
	{"safeline", func(h map[string]string, b string) string {
		if e := bodyHas(b, "Safeline"); e != "" {
			return e
		}
		return headerHas(h, "server", "safeline")
	}},
	{"aliyun-waf", func(h map[string]string, b string) string {
		if e := headerHas(h, "server", "aliyun"); e != "" {
			return e
		}
		return headerHas(h, "x-swift", ".")
	}},
	{"tencent-cloud", func(h map[string]string, b string) string { return headerHas(h, "server", "tencent") }},
	// 面板 / 运维 / 数据服务
	{"baota-panel", func(h map[string]string, b string) string {
		if e := bodyHas(b, "BTPanel"); e != "" {
			return e
		}
		return bodyHas(b, "宝塔面板")
	}},
	{"phpmyadmin", func(h map[string]string, b string) string { return bodyHas(b, "phpMyAdmin") }},
	{"adminer", func(h map[string]string, b string) string { return bodyHas(b, "adminer.org") }},
	{"webmin", func(h map[string]string, b string) string { return bodyHas(b, "Webmin") }},
	{"portainer", func(h map[string]string, b string) string { return bodyHas(b, "portainer") }},
	{"rancher", func(h map[string]string, b string) string { return bodyHas(b, "Rancher") }},
	{"k8s-dashboard", func(h map[string]string, b string) string { return bodyHas(b, "kubernetes-dashboard") }},
	{"kibana", func(h map[string]string, b string) string {
		if e := headerHas(h, "kbn-name", "."); e != "" {
			return e
		}
		return bodyHas(b, "kibana")
	}},
	{"elasticsearch", func(h map[string]string, b string) string { return bodyHas(b, "\"cluster_name\"") }},
	{"minio", func(h map[string]string, b string) string {
		if e := headerHas(h, "server", "minio"); e != "" {
			return e
		}
		return bodyHas(b, "MinIO")
	}},
	{"rabbitmq", func(h map[string]string, b string) string { return bodyHas(b, "RabbitMQ Management") }},
	{"zabbix", func(h map[string]string, b string) string { return bodyHas(b, "Zabbix SIA") }},
	{"prometheus", func(h map[string]string, b string) string { return bodyHas(b, "Prometheus") }},
	{"consul", func(h map[string]string, b string) string { return bodyHas(b, "Consul by HashiCorp") }},
	{"etcd", func(h map[string]string, b string) string { return bodyHas(b, "\"etcdserver\"") }},
	{"solr", func(h map[string]string, b string) string { return bodyHas(b, "SolrCore") }},
	{"ceph", func(h map[string]string, b string) string { return bodyHas(b, "Ceph Dashboard") }},
	{"nexus", func(h map[string]string, b string) string { return bodyHas(b, "Nexus Repository Manager") }},
	{"harbor", func(h map[string]string, b string) string { return bodyHas(b, "Harbor") }},
	{"gitlab", func(h map[string]string, b string) string {
		if e := headerHas(h, "set-cookie", "_gitlab_session"); e != "" {
			return e
		}
		return bodyHas(b, "GitLab")
	}},
	{"gitea", func(h map[string]string, b string) string { return bodyHas(b, "Gitea") }},
	{"gogs", func(h map[string]string, b string) string { return bodyHas(b, "Gogs") }},
	{"jira", func(h map[string]string, b string) string { return bodyHas(b, "jira") }},
	{"confluence", func(h map[string]string, b string) string { return bodyHas(b, "confluence") }},
	{"nextcloud", func(h map[string]string, b string) string { return bodyHas(b, "nextcloud") }},
	{"seafile", func(h map[string]string, b string) string { return bodyHas(b, "seafile") }},
	{"openvpn", func(h map[string]string, b string) string { return bodyHas(b, "OpenVPN") }},
	{"fortinet", func(h map[string]string, b string) string { return bodyHas(b, "FortiGate") }},
	{"citrix", func(h map[string]string, b string) string { return bodyHas(b, "Citrix") }},
	{"vmware-vsphere", func(h map[string]string, b string) string { return bodyHas(b, "VMware vSphere") }},
	// 国产 CMS（历史洞最多的一类）
	{"discuz", func(h map[string]string, b string) string {
		if e := bodyHas(b, "Discuz!"); e != "" {
			return e
		}
		return headerHas(h, "set-cookie", "discuz_")
	}},
	{"dedecms", func(h map[string]string, b string) string { return bodyHas(b, "DedeCMS") }},
	{"phpcms", func(h map[string]string, b string) string { return bodyHas(b, "PHPCMS") }},
	{"empirecms", func(h map[string]string, b string) string { return bodyHas(b, "EmpireCMS") }},
	{"zblog", func(h map[string]string, b string) string { return bodyHas(b, "Z-Blog") }},
	{"ecshop", func(h map[string]string, b string) string { return bodyHas(b, "ECSHOP") }},
	{"shopxo", func(h map[string]string, b string) string { return bodyHas(b, "ShopXO") }},
	{"maccms", func(h map[string]string, b string) string { return bodyHas(b, "MacCMS") }},
	{"xxljob", func(h map[string]string, b string) string { return bodyHas(b, "XXL-JOB") }},
}

func techHits(headers map[string]string, body string) []string {
	if headers == nil {
		headers = map[string]string{}
	}
	trimmed := body
	if len(trimmed) > 200*1024 {
		trimmed = trimmed[:200*1024]
	}
	var out []string
	for _, sig := range techSigs {
		if ev := sig.score(headers, trimmed); ev != "" {
			out = append(out, fmt.Sprintf("%s (%s)", sig.name, ev))
		}
	}
	return out
}

var (
	reAttrURL  = regexp.MustCompile(`(?i)(?:href|src|action)\s*=\s*["']([^"'#\s>]+)["']`)
	reAbsURL   = regexp.MustCompile(`(?i)https?://[a-z0-9.\-]+(?::\d+)?/[^\s"'<>\\)]*`)
	reQuoted   = regexp.MustCompile(`["'](/[a-zA-Z0-9_\-./{}$%~]{2,120})["']`)
	reAPIPath  = regexp.MustCompile("(?i)[\"'](/(?:api|v[0-9]|rest|graphql|admin|internal|actuator|swagger)[a-zA-Z0-9_\\-./{}$]*)[\"']")
	reQueryStr = regexp.MustCompile(`\?([a-zA-Z0-9_\-\[\]]{1,30})=`)
)

// collectEndpoints prefers routes the application names about itself: absolute
// URLs, quoted paths, and API-shaped paths. The order matters — API paths first,
// because that is where the interesting authorization checks live.
func collectEndpoints(text, baseURL string) []string {
	if len(text) > 2*1024*1024 {
		text = text[:2*1024*1024]
	}
	seen := map[string]bool{}
	var api, quoted, abs, attr []string
	add := func(dst *[]string, raw string) {
		raw = strings.TrimSpace(strings.TrimRight(raw, ".,;)"))
		if raw == "" || raw == "/" || strings.HasPrefix(raw, "javascript:") || strings.HasPrefix(raw, "#") {
			return
		}
		if seen[raw] {
			return
		}
		seen[raw] = true
		*dst = append(*dst, raw)
	}
	for _, m := range reAPIPath.FindAllStringSubmatch(text, 400) {
		add(&api, m[1])
	}
	for _, m := range reQuoted.FindAllStringSubmatch(text, 600) {
		add(&quoted, m[1])
	}
	for _, m := range reAbsURL.FindAllString(text, 400) {
		add(&abs, m)
	}
	for _, m := range reAttrURL.FindAllStringSubmatch(text, 600) {
		add(&attr, resolveURL(baseURL, m[1]))
	}
	out := make([]string, 0, endpointMaxOut)
	appendLimited := func(label string, rows []string) {
		if len(rows) == 0 {
			return
		}
		sort.Strings(rows)
		out = append(out, "# "+label)
		for _, r := range rows {
			if len(out) >= endpointMaxOut {
				return
			}
			out = append(out, r)
		}
	}
	appendLimited("api / 敏感路径", api)
	appendLimited("绝对 URL", abs)
	appendLimited("引号内路径", quoted)
	appendLimited("页面引用", attr)
	if len(out) > endpointMaxOut {
		out = append(out[:endpointMaxOut], fmt.Sprintf("# …(截断，共 %d 条)", len(out)))
	}
	if m := reQueryStr.FindAllStringSubmatch(text, 100); len(m) > 0 {
		names := map[string]bool{}
		var params []string
		for _, g := range m {
			if !names[g[1]] {
				names[g[1]] = true
				params = append(params, g[1])
			}
		}
		if len(params) > 0 {
			sort.Strings(params)
			out = append(out, "# 查询参数名: "+strings.Join(params, ", "))
		}
	}
	return out
}

func resolveURL(baseURL, ref string) string {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" || strings.HasPrefix(strings.ToLower(ref), "http://") || strings.HasPrefix(strings.ToLower(ref), "https://") {
		return ref
	}
	base := strings.TrimRight(baseURL, "/")
	if strings.HasPrefix(ref, "/") {
		if i := strings.Index(base, "://"); i >= 0 {
			rest := base[i+3:]
			if j := strings.Index(rest, "/"); j >= 0 {
				return base[:i+3] + rest[:j] + ref
			}
			return base + ref
		}
		return base + ref
	}
	return base + "/" + ref
}
