package agent

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

// Passive recon. Every tool here queries a third party (CT logs, wayback,
// RDAP, BGP, FOFA) or reads one well-known file — nothing sweeps the target.
// That distinction matters: the command gate exists to stop volume, and these
// tools answer "what exists" without touching the host at all.

const (
	certMaxHosts    = 300
	waybackMaxURLs  = 400
	reconAPIMaxBody = 4 * 1024 * 1024
	reconAPITimeout = 55 * time.Second
	faviconMaxBody  = 256 * 1024
	fofaMaxSize     = 100
)

// reconClient is deliberately proxy-free: these requests go to crt.sh and
// friends, not to the engagement target, and routing them through the recording
// proxy would file public-web noise into the evidence store.
func reconClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = reconAPITimeout
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
			Proxy:               http.ProxyFromEnvironment,
			MaxIdleConns:        8,
			IdleConnTimeout:     30 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}

func reconGet(ctx context.Context, client *http.Client, raw string, headers map[string]string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return 0, nil, err
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; ARTEX-recon)")
	}
	req.Header.Set("Accept", "application/json,text/plain,*/*")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, reconAPIMaxBody))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

// certSubdomains asks certificate transparency (crt.sh) what names were ever
// issued for a domain. This is the cheapest way to widen scope without sending
// a single packet at the target.
func (t *ToolSet) certSubdomains() actool.CoreTool {
	return readTool("cert_subdomains", "从证书透明度日志(crt.sh)取域名的历史证书主机名，用于被动发现子域/关联资产。只查第三方日志，不连接目标。",
		obj(map[string]any{
			"domain": str("根域名，例如 example.com"),
			"limit":  map[string]any{"type": "integer", "description": "最多返回条数，默认 200，上限 300"},
		}, "domain"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Domain string `json:"domain"`
				Limit  int    `json:"limit"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(fmt.Sprintf("参数解析失败: %v", err)), nil
			}
			domain := normalizeDomain(a.Domain)
			if domain == "" {
				return actool.Errorf("cert_subdomains 需要 domain"), nil
			}
			limit := a.Limit
			if limit <= 0 || limit > certMaxHosts {
				limit = 200
			}
			ctx, cancel := context.WithTimeout(ctx, reconAPITimeout)
			defer cancel()
			q := "https://crt.sh/?q=" + url.QueryEscape("%."+domain+"%") + "&output=json"
			var rows []struct {
				NameValue string `json:"name_value"`
				NotBefore string `json:"not_before"`
				Issuer    string `json:"issuer_name"`
			}
			// crt.sh times out under load and certspotter covers the same logs,
			// so a slow CT endpoint does not fail the whole recon step.
			src := "crt.sh"
			code, body, err := reconGet(ctx, reconClient(0), q, nil)
			switch {
			case err != nil:
				src = "certspotter（crt.sh 不可达: " + firstLineOf(err.Error()) + "）"
			case code != 200:
				src = fmt.Sprintf("certspotter（crt.sh HTTP %d）", code)
			default:
				if jerr := json.Unmarshal(body, &rows); jerr != nil {
					src = "certspotter（crt.sh 返回非 JSON，限流？）"
					rows = nil
				}
			}
			seen := map[string]string{}
			if len(rows) == 0 {
				for name, when := range certspotterNames(ctx, domain) {
					seen[name] = when
				}
			}
			for _, r := range rows {
				for _, name := range strings.Split(r.NameValue, "\n") {
					name = normalizeDomain(name)
					if name == "" || !strings.HasSuffix(name, domain) {
						continue
					}
					if _, ok := seen[name]; !ok {
						seen[name] = r.NotBefore + " " + shortIssuer(r.Issuer)
					}
				}
			}
			names := make([]string, 0, len(seen))
			for n := range seen {
				names = append(names, n)
			}
			sort.Strings(names)
			var b strings.Builder
			fmt.Fprintf(&b, "domain: %s\n来源: %s\n证书条数: %d\n唯一主机名: %d\n", domain, src, len(rows), len(names))
			for i, n := range names {
				if i >= limit {
					fmt.Fprintf(&b, "… 其余 %d 条已截断\n", len(names)-limit)
					break
				}
				fmt.Fprintf(&b, "%s  (%s)\n", n, orDash(seen[n]))
			}
			if len(names) == 0 {
				b.WriteString("未发现主机名：先确认域名拼写，或换 fofa_search / wayback_urls 交叉验证。\n")
			}
			return actool.Text(b.String()), nil
		})
}

func shortIssuer(s string) string {
	if i := strings.Index(s, "O="); i >= 0 {
		s = s[i+2:]
	}
	if i := strings.IndexAny(s, ",\n"); i > 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// certspotterNames is the fallback CT source. It returns name -> first-seen so
// the output keeps the same shape as crt.sh.
func certspotterNames(ctx context.Context, domain string) map[string]string {
	out := map[string]string{}
	q := "https://api.certspotter.com/v1/issuances?domain=" + url.QueryEscape(domain) +
		"&include_subdomains=true&expand=dns_names&expand=not_before"
	code, body, err := reconGet(ctx, reconClient(0), q, nil)
	if err != nil || code != 200 {
		return out
	}
	var rows []struct {
		DNSNames  []string `json:"dns_names"`
		NotBefore string   `json:"not_before"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return out
	}
	for _, r := range rows {
		for _, n := range r.DNSNames {
			n = normalizeDomain(n)
			if n == "" || !strings.HasSuffix(n, domain) {
				continue
			}
			if _, ok := out[n]; !ok {
				out[n] = r.NotBefore + " certspotter"
			}
		}
	}
	return out
}

// waybackURLs harvests archived URLs for a domain: old admin panels, API paths
// and debug endpoints survive in the archive long after they were removed.
func (t *ToolSet) waybackURLs() actool.CoreTool {
	return readTool("wayback_urls", "从 Wayback Machine 取域名历史 URL（含已下线接口/后台路径），用于被动梳理攻击面。只查 archive.org。",
		obj(map[string]any{
			"domain": str("域名，例如 example.com"),
			"limit":  map[string]any{"type": "integer", "description": "最多返回条数，默认 200，上限 400"},
			"filter": str("可选，只保留包含该子串的 URL，例如 .js 或 /api/"),
		}, "domain"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Domain string `json:"domain"`
				Limit  int    `json:"limit"`
				Filter string `json:"filter"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(fmt.Sprintf("参数解析失败: %v", err)), nil
			}
			domain := normalizeDomain(a.Domain)
			if domain == "" {
				return actool.Errorf("wayback_urls 需要 domain"), nil
			}
			limit := a.Limit
			if limit <= 0 || limit > waybackMaxURLs {
				limit = 200
			}
			ctx, cancel := context.WithTimeout(ctx, reconAPITimeout)
			defer cancel()
			q := "http://web.archive.org/cdx/search/cdx?url=" + url.QueryEscape(domain+"/*") +
				"&output=json&fl=original&collapse=urlkey&filter=statuscode:200&limit=2000"
			code, body, err := reconGet(ctx, reconClient(0), q, nil)
			if err != nil {
				return actool.Errorf(fmt.Sprintf("wayback 请求失败: %v", err)), nil
			}
			if code != 200 {
				return actool.Errorf(fmt.Sprintf("wayback 返回 HTTP %d", code)), nil
			}
			var rows [][]string
			if err := json.Unmarshal(body, &rows); err != nil {
				return actool.Errorf(fmt.Sprintf("wayback 返回解析失败: %v", err)), nil
			}
			seen := map[string]bool{}
			var urls []string
			for i, row := range rows {
				if i == 0 || len(row) == 0 {
					continue
				}
				u := row[0]
				if a.Filter != "" && !strings.Contains(u, a.Filter) {
					continue
				}
				if seen[u] {
					continue
				}
				seen[u] = true
				urls = append(urls, u)
			}
			sort.Strings(urls)
			var b strings.Builder
			fmt.Fprintf(&b, "domain: %s\n历史 URL: %d\n", domain, len(urls))
			for i, u := range urls {
				if i >= limit {
					fmt.Fprintf(&b, "… 其余 %d 条已截断\n", len(urls)-limit)
					break
				}
				b.WriteString(u + "\n")
			}
			if len(urls) == 0 {
				b.WriteString("无归档记录。\n")
			}
			return actool.Text(b.String()), nil
		})
}

// rdapLookup answers registration facts over HTTPS — no whois(43) socket, no
// hanging connection, and it works where port 43 is filtered.
func (t *ToolSet) rdapLookup() actool.CoreTool {
	return readTool("rdap_lookup", "查询域名/IP 的注册信息（注册商、注册/到期时间、NS、状态），走 HTTPS RDAP，不开 whois 43 端口。",
		obj(map[string]any{
			"target": str("域名或 IP，例如 example.com / 1.2.3.4"),
		}, "target"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Target string `json:"target"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(fmt.Sprintf("参数解析失败: %v", err)), nil
			}
			target := strings.TrimSpace(a.Target)
			if target == "" {
				return actool.Errorf("rdap_lookup 需要 target"), nil
			}
			kind := "domain"
			if net.ParseIP(target) != nil {
				kind = "ip"
			} else {
				target = normalizeDomain(target)
				if target == "" {
					return actool.Errorf(fmt.Sprintf("target 无效: %s", a.Target)), nil
				}
			}
			ctx, cancel := context.WithTimeout(ctx, reconAPITimeout)
			defer cancel()
			q := "https://rdap.org/" + kind + "/" + url.PathEscape(target)
			code, body, err := reconGet(ctx, reconClient(0), q, nil)
			if err != nil {
				return actool.Errorf(fmt.Sprintf("RDAP 请求失败: %v", err)), nil
			}
			if code == 404 {
				return actool.Text(fmt.Sprintf("target: %s\nRDAP 无记录（未注册或该 TLD 不提供 RDAP）。\n", target)), nil
			}
			if code != 200 {
				return actool.Errorf(fmt.Sprintf("RDAP 返回 HTTP %d", code)), nil
			}
			var doc map[string]any
			if err := json.Unmarshal(body, &doc); err != nil {
				return actool.Errorf(fmt.Sprintf("RDAP 返回解析失败: %v", err)), nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "target: %s (%s)\n", target, kind)
			if v, ok := doc["handle"].(string); ok {
				fmt.Fprintf(&b, "handle: %s\n", v)
			}
			if reg := rdapRegistrar(doc); reg != "" {
				fmt.Fprintf(&b, "注册商: %s\n", reg)
			}
			for _, ev := range rdapEvents(doc) {
				b.WriteString(ev + "\n")
			}
			if ns := rdapNameservers(doc); len(ns) > 0 {
				fmt.Fprintf(&b, "NS: %s\n", strings.Join(ns, ", "))
			}
			if st := rdapStatuses(doc); len(st) > 0 {
				fmt.Fprintf(&b, "状态: %s\n", strings.Join(st, ", "))
			}
			if cidr, ok := doc["cidr0_cidrs"].([]any); ok {
				fmt.Fprintf(&b, "CIDR: %v\n", cidr)
			}
			return actool.Text(b.String()), nil
		})
}

func rdapRegistrar(doc map[string]any) string {
	ents, ok := doc["entities"].([]any)
	if !ok {
		return ""
	}
	for _, e := range ents {
		em, ok := e.(map[string]any)
		if !ok {
			continue
		}
		roles, _ := em["roles"].([]any)
		isReg := false
		for _, r := range roles {
			if s, _ := r.(string); s == "registrar" {
				isReg = true
			}
		}
		if !isReg {
			continue
		}
		if vcard, ok := em["vcardArray"].([]any); ok && len(vcard) > 1 {
			if props, ok := vcard[1].([]any); ok {
				for _, p := range props {
					arr, ok := p.([]any)
					if !ok || len(arr) < 4 {
						continue
					}
					if name, _ := arr[0].(string); name == "fn" {
						if s, _ := arr[3].(string); s != "" {
							return s
						}
					}
				}
			}
		}
		if h, ok := em["handle"].(string); ok {
			return h
		}
	}
	return ""
}

func rdapEvents(doc map[string]any) []string {
	evs, ok := doc["events"].([]any)
	if !ok {
		return nil
	}
	order := map[string]string{
		"registration": "注册时间", "expiration": "到期时间",
		"last changed": "最后变更", "last update of RDAP database": "RDAP 更新",
	}
	var out []string
	for _, e := range evs {
		em, ok := e.(map[string]any)
		if !ok {
			continue
		}
		action, _ := em["eventAction"].(string)
		date, _ := em["eventDate"].(string)
		label := order[action]
		if label == "" {
			label = action
		}
		if date != "" {
			out = append(out, fmt.Sprintf("%s: %s", label, date))
		}
	}
	return out
}

func rdapNameservers(doc map[string]any) []string {
	ns, ok := doc["nameservers"].([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, n := range ns {
		nm, ok := n.(map[string]any)
		if !ok {
			continue
		}
		if s, _ := nm["ldhName"].(string); s != "" {
			out = append(out, strings.ToLower(s))
		}
	}
	sort.Strings(out)
	return out
}

func rdapStatuses(doc map[string]any) []string {
	st, ok := doc["status"].([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, s := range st {
		if v, ok := s.(string); ok {
			out = append(out, v)
		}
	}
	return out
}

// reverseIP answers "who else lives here" from BGP data: the ASN, the announcing
// prefix and the network's name. Turning a single host into a netblock is the
// difference between one finding and a segment.
func (t *ToolSet) reverseIP() actool.CoreTool {
	return readTool("reverse_ip", "查 IP 的 ASN、归属与宣告网段（BGPView），用于把单点扩成网段判断。只查公共 BGP 数据。",
		obj(map[string]any{
			"ip": str("IPv4/IPv6 地址"),
		}, "ip"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				IP string `json:"ip"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(fmt.Sprintf("参数解析失败: %v", err)), nil
			}
			ip := strings.TrimSpace(a.IP)
			if net.ParseIP(ip) == nil {
				return actool.Errorf(fmt.Sprintf("ip 无效: %s", ip)), nil
			}
			ctx, cancel := context.WithTimeout(ctx, reconAPITimeout)
			defer cancel()
			fallback := func(why string) (actool.Result, error) {
				return actool.Text(fmt.Sprintf("ip: %s\n（BGPView 不可用: %s）\n%s", ip, why, rdapIPInfo(ctx, ip))), nil
			}
			code, body, err := reconGet(ctx, reconClient(0), "https://api.bgpview.io/ip/"+url.PathEscape(ip), nil)
			if err != nil {
				return fallback(firstLineOf(err.Error()))
			}
			if code != 200 {
				return fallback(fmt.Sprintf("HTTP %d", code))
			}
			var resp struct {
				Status string `json:"status"`
				Data   struct {
					Prefixes []struct {
						Prefix      string `json:"prefix"`
						Description string `json:"description"`
					} `json:"prefixes"`
					RIRAllocation struct {
						Description string `json:"description"`
						Country     string `json:"country"`
					} `json:"rir_allocation"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &resp); err != nil {
				return fallback("返回非 JSON")
			}
			var b strings.Builder
			fmt.Fprintf(&b, "ip: %s\n", ip)
			if d := resp.Data.RIRAllocation.Description; d != "" {
				fmt.Fprintf(&b, "归属: %s (%s)\n", d, resp.Data.RIRAllocation.Country)
			}
			if len(resp.Data.Prefixes) == 0 {
				b.WriteString("无宣告前缀（可能未路由或数据缺失）。\n")
			}
			for _, p := range resp.Data.Prefixes {
				fmt.Fprintf(&b, "网段: %s  %s\n", p.Prefix, orDash(p.Description))
			}
			return actool.Text(b.String()), nil
		})
}


// rdapIPInfo is the RDAP fallback for reverseIP: registration range and holder
// come from the RIR record, which is enough to decide whether a host is worth
// widening into a segment.
func rdapIPInfo(ctx context.Context, ip string) string {
	code, body, err := reconGet(ctx, reconClient(0), "https://rdap.org/ip/"+url.PathEscape(ip), nil)
	if err != nil {
		return "RDAP 也不可达: " + firstLineOf(err.Error()) + "\n"
	}
	if code != 200 {
		return fmt.Sprintf("RDAP 返回 HTTP %d\n", code)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return "RDAP 返回非 JSON\n"
	}
	var b strings.Builder
	for _, k := range []string{"name", "handle", "country"} {
		if v, ok := doc[k].(string); ok && v != "" {
			fmt.Fprintf(&b, "%s: %s\n", k, v)
		}
	}
	if s, ok := doc["startAddress"].(string); ok {
		e, _ := doc["endAddress"].(string)
		fmt.Fprintf(&b, "范围: %s - %s\n", s, e)
	}
	for _, r := range rdapRemarks(doc) {
		fmt.Fprintf(&b, "备注: %s\n", r)
	}
	return b.String()
}

func rdapRemarks(doc map[string]any) []string {
	rm, ok := doc["remarks"].([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, r := range rm {
		if m, ok := r.(map[string]any); ok {
			for _, l := range asStrings(m["description"]) {
				if strings.TrimSpace(l) != "" {
					out = append(out, firstLineOf(l))
				}
			}
		}
	}
	if len(out) > 4 {
		out = out[:4]
	}
	return out
}

func asStrings(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		if s, ok := v.(string); ok {
			return []string{s}
		}
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// cdnSuffixes is the CNAME footprint of the CDNs that actually show up in SRC
// work. A hit means the edge answers, not the origin — probing it measures the
// CDN, and every "finding" on it is a false positive.
var cdnSuffixes = []struct{ Name, Suffix string }{
	{"cloudflare", ".cloudflare.net"},
	{"cloudflare", ".cdn.cloudflare.net"},
	{"akamai", ".akamaiedge.net"},
	{"akamai", ".edgekey.net"},
	{"akamai", ".edgesuite.net"},
	{"akamai", ".akamaitechnologies.com"},
	{"fastly", ".fastly.net"},
	{"cloudfront", ".cloudfront.net"},
	{"azure", ".azureedge.net"},
	{"azure", ".trafficmanager.net"},
	{"alicdn", ".alicdn.com"},
	{"aliyun-waf", ".kunlunslb.com"},
	{"aliyun-waf", ".kunlunaq.com"},
	{"aliyun-waf", ".aliyunwaf.com"},
	{"tencent-cdn", ".cdntip.com"},
	{"tencent-cdn", ".dnsv1.com"},
	{"tencent-cdn", ".tencdns.com"},
	{"qiniu", ".qiniudn.com"},
	{"qiniu", ".qnssl.com"},
	{"wangsu", ".wscdns.com"},
	{"wangsu", ".wscloudcdn.com"},
	{"baidu", ".bdydns.com"},
	{"baidu", ".bcebos.com"},
	{"huawei", ".hwclouds-dns.com"},
	{"huawei", ".cdnhwc1.com"},
	{"upyun", ".upaiyun.com"},
	{"jsdelivr", ".jsdelivr.net"},
	{"netlify", ".netlify.app"},
	{"vercel", ".vercel-dns.com"},
	{"github-pages", ".github.io"},
	{"s3", ".s3.amazonaws.com"},
}

// cdnDetect resolves the CNAME chain and says whether the name is fronted. It
// uses DNS only, so it is safe to run against any in-scope host.
func (t *ToolSet) cdnDetect() actool.CoreTool {
	return readTool("cdn_detect", "通过 CNAME 链判断域名是否在 CDN/WAF 之后，并指出是哪一家。纯 DNS 查询，不连接目标。",
		obj(map[string]any{
			"host": str("域名，例如 www.example.com"),
		}, "host"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Host string `json:"host"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(fmt.Sprintf("参数解析失败: %v", err)), nil
			}
			host := normalizeDomain(a.Host)
			if host == "" {
				return actool.Errorf("cdn_detect 需要 host"), nil
			}
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			r := net.Resolver{}
			var chain []string
			cur := host
			for i := 0; i < 8; i++ {
				c, err := r.LookupCNAME(ctx, cur)
				c = strings.TrimSuffix(c, ".")
				if err != nil || c == "" || strings.EqualFold(c, cur) {
					break
				}
				chain = append(chain, c)
				cur = c
			}
			ips, _ := r.LookupIP(ctx, "ip4", host)
			var b strings.Builder
			fmt.Fprintf(&b, "host: %s\n", host)
			if len(chain) > 0 {
				fmt.Fprintf(&b, "CNAME 链: %s\n", strings.Join(chain, " -> "))
			} else {
				b.WriteString("CNAME 链: 无（直接 A 记录，或解析失败）\n")
			}
			if len(ips) > 0 {
				fmt.Fprintf(&b, "A: %s\n", joinCap(ipStrings(ips), 16))
			}
			fronted := ""
			for _, c := range append([]string{host}, chain...) {
				low := strings.ToLower(c)
				for _, s := range cdnSuffixes {
					if strings.HasSuffix(low, s.Suffix) {
						fronted = s.Name
						break
					}
				}
				if fronted != "" {
					break
				}
			}
			if fronted != "" {
				fmt.Fprintf(&b, "判定: 在 %s 之后（边缘节点应答）。对边缘做端口/目录探测得到的是 CDN 的结果，不能当作源站证据。\n", fronted)
			} else {
				b.WriteString("判定: 未见 CDN CNAME 特征；仍不排除 WAF 反代，需结合响应头核对。\n")
			}
			return actool.Text(b.String()), nil
		})
}

// faviconHash reproduces the icon hash FOFA/Shodan pivot on: mmh3 of the
// favicon bytes base64-encoded with line breaks. Same icon across domains is a
// strong asset-pivot signal.
func (t *ToolSet) faviconHash() actool.CoreTool {
	return readTool("favicon_hash", "取站点 favicon 并计算 mmh3 图标哈希（FOFA 同款算法），用于跨域名反查同一套系统。只请求 /favicon.ico。",
		obj(map[string]any{
			"url": str("站点根地址，例如 https://example.com"),
		}, "url"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(fmt.Sprintf("参数解析失败: %v", err)), nil
			}
			base := strings.TrimSpace(a.URL)
			u, err := url.Parse(base)
			if err != nil || u.Host == "" {
				return actool.Errorf(fmt.Sprintf("url 无效: %s", base)), nil
			}
			target := u.Scheme + "://" + u.Host + "/favicon.ico"
			ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			client := t.reconTargetClient()
			code, body, err := reconGetBytes(ctx, client, target)
			if err != nil {
				return actool.Errorf(fmt.Sprintf("抓取 favicon 失败: %v", err)), nil
			}
			note := ""
			if code != 200 || len(body) == 0 {
				// Many sites ship the icon only via <link rel="icon">, so the
				// default path is a 404 even though the site has a hashable icon.
				alt := iconLinkFromPage(ctx, client, u.Scheme+"://"+u.Host+"/")
				if alt == "" {
					return actool.Text(fmt.Sprintf("url: %s\nfavicon: HTTP %d / %d 字节 —— 首页也未声明 <link rel=icon>，无图标可算。\n", u.Host, code, len(body))), nil
				}
				target = alt
				code, body, err = reconGetBytes(ctx, client, alt)
				if err != nil || code != 200 || len(body) == 0 {
					return actool.Text(fmt.Sprintf("url: %s\nfavicon: 声明了 %s 但取回失败（HTTP %d）。\n", u.Host, alt, code)), nil
				}
				note = "来源: " + alt + "\n"
			}
			if len(body) > faviconMaxBody {
				body = body[:faviconMaxBody]
			}
			b64 := base64.StdEncoding.EncodeToString(body)
			var wrapped strings.Builder
			for i := 0; i < len(b64); i += 76 {
				end := i + 76
				if end > len(b64) {
					end = len(b64)
				}
				wrapped.WriteString(b64[i:end])
				wrapped.WriteString("\n")
			}
			h := murmur3Sum32([]byte(wrapped.String()))
			var b strings.Builder
			fmt.Fprintf(&b, "url: %s\nfavicon: %d 字节\n", u.Host, len(body))
			b.WriteString(note)
			fmt.Fprintf(&b, "icon_hash: %d\n", int32(h))
			b.WriteString("用法: 把 icon_hash 丢进 FOFA/Shodan 的 icon_hash 条件，反查同一套系统的其他域名与 IP。\n")
			return actool.Text(b.String()), nil
		})
}

// reconTargetClient is the target-facing client: it uses the recording proxy
// when one is configured, so a favicon fetch lands in the evidence store.
func (t *ToolSet) reconTargetClient() *http.Client {
	c, err := t.httpClient(20, true)
	if err == nil {
		return c
	}
	return reconClient(20 * time.Second)
}

func reconGetBytes(ctx context.Context, client *http.Client, raw string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; ARTEX)")
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, faviconMaxBody))
	return resp.StatusCode, body, err
}

// murmur3Sum32 is MurmurHash3 x86 32-bit, seed 0 — the hash FOFA prints as
// icon_hash. Implemented here so the pivot works without an external binary.
func murmur3Sum32(data []byte) uint32 {
	const c1, c2 = 0xcc9e2d51, 0x1b873593
	h := uint32(0)
	n := len(data) / 4 * 4
	for i := 0; i < n; i += 4 {
		k := uint32(data[i]) | uint32(data[i+1])<<8 | uint32(data[i+2])<<16 | uint32(data[i+3])<<24
		k *= c1
		k = k<<15 | k>>17
		k *= c2
		h ^= k
		h = h<<13 | h>>19
		h = h*5 + 0xe6546b64
	}
	var k uint32
	tail := data[n:]
	switch len(tail) {
	case 3:
		k ^= uint32(tail[2]) << 16
		fallthrough
	case 2:
		k ^= uint32(tail[1]) << 8
		fallthrough
	case 1:
		k ^= uint32(tail[0])
		k *= c1
		k = k<<15 | k>>17
		k *= c2
		h ^= k
	}
	h ^= uint32(len(data))
	h ^= h >> 16
	h *= 0x85ebca6b
	h ^= h >> 13
	h *= 0xc2b2ae35
	h ^= h >> 16
	return h
}

// fofaSearch is the native FOFA client. It reads the same key pool the recon
// settings page manages (env ARTEX_FOFA_KEYS, entries "email:key"), tries them
// in order and moves on when a key is out of quota — a worker should not lose a
// whole task because key #1 hit its limit.
func (t *ToolSet) fofaSearch() actool.CoreTool {
	return readTool("fofa_search", "用 FOFA 查询互联网资产（语法同 FOFA：domain=\"x.com\"、ip=\"1.2.3.0/24\"、icon_hash=…）。自动在配置的 key 池里轮询，额度用尽自动换下一个。",
		obj(map[string]any{
			"query": str("FOFA 查询语法，例如 domain=\"example.com\" && country=\"CN\""),
			"size":  map[string]any{"type": "integer", "description": "返回条数，默认 20，上限 100"},
			"page":  map[string]any{"type": "integer", "description": "页码，默认 1"},
		}, "query"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Query string `json:"query"`
				Size  int    `json:"size"`
				Page  int    `json:"page"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(fmt.Sprintf("参数解析失败: %v", err)), nil
			}
			q := strings.TrimSpace(a.Query)
			if q == "" {
				return actool.Errorf("fofa_search 需要 query"), nil
			}
			size := a.Size
			if size <= 0 || size > fofaMaxSize {
				size = 20
			}
			page := a.Page
			if page <= 0 {
				page = 1
			}
			keys := fofaKeyPool()
			if len(keys) == 0 {
				return actool.Errorf("FOFA 未配置：到「系统设置 → 信息搜集」填入 key 池（或设置环境变量 ARTEX_FOFA_KEYS=email:key,email2:key2）后重试。"), nil
			}
			ctx, cancel := context.WithTimeout(ctx, reconAPITimeout)
			defer cancel()
			qb64 := base64.StdEncoding.EncodeToString([]byte(q))
			var errs []string
			for _, k := range keys {
				u := fmt.Sprintf("https://fofa.info/api/v1/search/all?email=%s&key=%s&qbase64=%s&size=%d&page=%d&fields=host,ip,port,protocol,title,domain,server",
					url.QueryEscape(k.Email), url.QueryEscape(k.Key), url.QueryEscape(qb64), size, page)
				code, body, err := reconGet(ctx, reconClient(0), u, nil)
				if err != nil {
					errs = append(errs, fmt.Sprintf("%s: %v", k.Email, err))
					continue
				}
				var resp struct {
					Error   bool     `json:"error"`
					ErrMsg  string   `json:"errmsg"`
					Size    int      `json:"size"`
					Results [][]any  `json:"results"`
				}
				if err := json.Unmarshal(body, &resp); err != nil {
					errs = append(errs, fmt.Sprintf("%s: HTTP %d 非 JSON", k.Email, code))
					continue
				}
				if resp.Error {
					errs = append(errs, fmt.Sprintf("%s: %s", k.Email, resp.ErrMsg))
					continue
				}
				var b strings.Builder
				fmt.Fprintf(&b, "query: %s\n命中总数: %d\n", q, resp.Size)
				for _, row := range resp.Results {
					cells := make([]string, 0, len(row))
					for _, c := range row {
						cells = append(cells, fmt.Sprint(c))
					}
					b.WriteString(strings.Join(cells, " | ") + "\n")
				}
				if len(resp.Results) == 0 {
					b.WriteString("本页无结果：换语法或缩小范围。\n")
				}
				return actool.Text(b.String()), nil
			}
			return actool.Errorf("FOFA key 池全部失败：\n" + strings.Join(errs, "\n")), nil
		})
}

type fofaKey struct{ Email, Key string }

// fofaKeyPool accepts "email:key" entries separated by comma/semicolon/newline,
// and also tolerates a bare "key" when only an API key is configured.
func fofaKeyPool() []fofaKey {
	raw := strings.TrimSpace(os.Getenv("ARTEX_FOFA_KEYS"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("FOFA_KEYS"))
	}
	if raw == "" {
		return nil
	}
	var out []fofaKey
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == ' '
	}) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if i := strings.Index(part, ":"); i > 0 {
			out = append(out, fofaKey{Email: part[:i], Key: part[i+1:]})
			continue
		}
		out = append(out, fofaKey{Email: "", Key: part})
	}
	return out
}

// robotsHarvest reads robots.txt and sitemap.xml: both are published on purpose,
// both list paths an operator forgot they had.
func (t *ToolSet) robotsHarvest() actool.CoreTool {
	return readTool("robots_harvest", "读取目标 robots.txt 与 sitemap.xml，提取被明确列出或禁止的路径与站点地图条目（共两次 GET）。",
		obj(map[string]any{
			"url": str("站点根地址，例如 https://example.com"),
		}, "url"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(fmt.Sprintf("参数解析失败: %v", err)), nil
			}
			base := strings.TrimSpace(a.URL)
			u, err := url.Parse(base)
			if err != nil || u.Host == "" {
				return actool.Errorf(fmt.Sprintf("url 无效: %s", base)), nil
			}
			root := u.Scheme + "://" + u.Host
			ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
			defer cancel()
			client := t.reconTargetClient()
			var b strings.Builder
			fmt.Fprintf(&b, "root: %s\n", root)

			code, body, err := reconGet(ctx, client, root+"/robots.txt", nil)
			if err != nil {
				fmt.Fprintf(&b, "robots.txt: 请求失败（%v）\n", err)
			} else if code == 200 {
				disallow, allow, sitemaps := parseRobots(string(body))
				fmt.Fprintf(&b, "robots.txt: 200\n  Disallow: %s\n  Allow: %s\n  Sitemap: %s\n",
					joinCap(disallow, 60), joinCap(allow, 30), joinCap(sitemaps, 10))
			} else {
				fmt.Fprintf(&b, "robots.txt: HTTP %d\n", code)
			}

			code, body, err = reconGet(ctx, client, root+"/sitemap.xml", nil)
			if err != nil {
				fmt.Fprintf(&b, "sitemap.xml: 请求失败（%v）\n", err)
			} else if code == 200 {
				locs := parseSitemapLocs(string(body), 120)
				fmt.Fprintf(&b, "sitemap.xml: 200，条目 %d\n", len(locs))
				for _, l := range locs {
					b.WriteString("  " + l + "\n")
				}
			} else {
				fmt.Fprintf(&b, "sitemap.xml: HTTP %d\n", code)
			}
			return actool.Text(b.String()), nil
		})
}

var (
	reIconRelFirst  = regexp.MustCompile("(?i)<link[^>]*rel=[\"']?[^\"'>]*icon[^\"'>]*[\"']?[^>]*href=[\"']([^\"']+)[\"']")
	reIconHrefFirst = regexp.MustCompile("(?i)<link[^>]*href=[\"']([^\"']+)[\"']?[^>]*rel=[\"']?[^\"'>]*icon")
	reRobotsLine  = regexp.MustCompile(`(?i)^\s*(disallow|allow|sitemap)\s*:\s*(\S+)`)
	reSitemapLoc  = regexp.MustCompile(`(?i)<loc>\s*([^<\s]+)\s*</loc>`)
)

func parseRobots(body string) (disallow, allow, sitemaps []string) {
	for _, line := range strings.Split(body, "\n") {
		m := reRobotsLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		switch strings.ToLower(m[1]) {
		case "disallow":
			if m[2] != "/" && !containsStr(disallow, m[2]) {
				disallow = append(disallow, m[2])
			}
		case "allow":
			if !containsStr(allow, m[2]) {
				allow = append(allow, m[2])
			}
		case "sitemap":
			if !containsStr(sitemaps, m[2]) {
				sitemaps = append(sitemaps, m[2])
			}
		}
	}
	return
}

func parseSitemapLocs(body string, limit int) []string {
	var out []string
	for _, m := range reSitemapLoc.FindAllStringSubmatch(body, limit*2) {
		if len(m) > 1 && !containsStr(out, m[1]) {
			out = append(out, m[1])
			if len(out) >= limit {
				break
			}
		}
	}
	return out
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// subdomainVerify is the gate between a fat passive dump and anything active:
// the certificate log returns names that expired years ago, and probing those
// wastes the whole budget. DNS-only, bounded, no packets to the target itself.
func (t *ToolSet) subdomainVerify() actool.CoreTool {
	return readTool("subdomain_verify", "批量校验候选子域名是否真实解析（只做 DNS 查询，不连接目标），把证书/归档里的历史残留名筛掉。",
		obj(map[string]any{
			"names": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "候选主机名列表，1~50 条",
			},
		}, "names"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Names []string `json:"names"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(fmt.Sprintf("参数解析失败: %v", err)), nil
			}
			var names []string
			seen := map[string]bool{}
			for _, n := range a.Names {
				d := normalizeDomain(n)
				if d == "" || seen[d] {
					continue
				}
				seen[d] = true
				names = append(names, d)
			}
			if len(names) == 0 {
				return actool.Errorf("subdomain_verify 需要 names（合法域名列表）"), nil
			}
			capped := false
			if len(names) > 50 {
				names = names[:50]
				capped = true
			}
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			type res struct {
				name string
				ips  []string
				err  error
			}
			out := make(chan res, len(names))
			sem := make(chan struct{}, 8)
			for _, n := range names {
				go func(name string) {
					sem <- struct{}{}
					defer func() { <-sem }()
					r := net.Resolver{}
					c, cancelOne := context.WithTimeout(ctx, 5*time.Second)
					defer cancelOne()
					ips, err := r.LookupIP(c, "ip4", name)
					out <- res{name: name, ips: ipStrings(ips), err: err}
				}(n)
			}
			var live, dead []string
			for range names {
				r := <-out
				if r.err != nil || len(r.ips) == 0 {
					dead = append(dead, r.name)
					continue
				}
				live = append(live, fmt.Sprintf("%s  %s", r.name, joinCap(r.ips, 8)))
			}
			sort.Strings(live)
			sort.Strings(dead)
			var b strings.Builder
			fmt.Fprintf(&b, "候选: %d  存活: %d  无解析: %d\n", len(names), len(live), len(dead))
			if capped {
				b.WriteString("（超过 50 条，已截断；分批再查）\n")
			}
			b.WriteString("—— 存活 ——\n")
			if len(live) == 0 {
				b.WriteString("无\n")
			}
			for _, l := range live {
				b.WriteString(l + "\n")
			}
			b.WriteString("—— 无解析 ——\n")
			b.WriteString(joinCap(dead, 50) + "\n")
			return actool.Text(b.String()), nil
		})
}

// iconLinkFromPage finds an icon declaration in the page head. Two attribute
// orders exist in the wild, so both patterns are tried.
func iconLinkFromPage(ctx context.Context, client *http.Client, pageURL string) string {
	code, body, err := reconGet(ctx, client, pageURL, nil)
	if err != nil || code != 200 || len(body) == 0 {
		return ""
	}
	page := string(body)
	if len(page) > 200*1024 {
		page = page[:200*1024]
	}
	for _, re := range []*regexp.Regexp{reIconRelFirst, reIconHrefFirst} {
		if m := re.FindStringSubmatch(page); len(m) > 1 {
			return resolveRef(pageURL, m[1])
		}
	}
	return ""
}

func resolveRef(baseURL, ref string) string {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "//") {
		return "https:" + ref
	}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref
	}
	b, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	return b.ResolveReference(r).String()
}

func normalizeDomain(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimPrefix(s, "*.")
	s = strings.TrimSuffix(s, ".")
	if s == "" || strings.ContainsAny(s, " /\\@") {
		return ""
	}
	if !strings.Contains(s, ".") {
		return ""
	}
	return s
}
